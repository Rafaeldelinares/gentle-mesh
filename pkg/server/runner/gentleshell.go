package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/gitsafe"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

const (
	// maxGentleShellEventLine bounds a single NDJSON line read from gentle-shell so a
	// corrupted or hostile child cannot exhaust runner memory.
	maxGentleShellEventLine = 8 << 20
	// maxGentleShellStderr bounds the captured stderr kept for diagnostics.
	maxGentleShellStderr = 64 << 10
	// gentleShellDiffInlineLimit is the maximum diff size in bytes that the runner
	// embeds directly in the EventCompletion payload. Larger diffs are flushed to a
	// file under the task directory and exposed via diff_url.
	gentleShellDiffInlineLimit = 256 << 10
)

// hostGit builds a git invocation safe to run outside the sandbox against the
// RepoCache and its worktrees. See pkg/gitsafe for what "safe" covers: the cache
// config is writable by the sandboxed agent, so it neutralizes every
// config-driven program path git honours, including the dynamically named filter
// drivers a blanket -c flag cannot reach.
func hostGit(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	return gitsafe.Command(ctx, dir, args...)
}

// gitMust is hostGit for cleanup paths that cannot propagate an error. Building
// the command is the only fallible part (it refuses to guess when the filter
// drivers cannot be enumerated), and a bare /bin/false stands in for it so the
// caller's Run() simply fails, exactly as an unhardened git would have.
func gitMust(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd, err := hostGit(ctx, dir, args...)
	if err == nil {
		return cmd
	}
	fallback := exec.CommandContext(ctx, "false")
	return fallback
}

// gitRun is gitMust with the command already run, for callers that ignore the
// outcome entirely.
func gitRun(ctx context.Context, dir string, args ...string) error {
	return gitMust(ctx, dir, args...).Run()
}

// GentleShellRunnerOptions configures a GentleShellRunner.
//
// SandboxMounts is the ordered list of `--bind` / `--ro-bind` arguments that
// the runner will pass to bwrap. The runner is intentionally opinionated about
// the mount layout: it must contain enough of the system to launch the Go
// toolchain, Node, Pi, the gentle-shell runtime, and the task worktree, while
// keeping the user's HOME outside the namespace.
type GentleShellRunnerOptions struct {
	// Bwrap is the Bubblewrap executable. Empty defaults to "bwrap".
	Bwrap string
	// GentleShell is the gentle-shell CLI executable. Empty defaults to "gentle-shell".
	GentleShell string
	// TasksDir is the directory where the runner stores oversized diffs and patch
	// bundles. Required.
	TasksDir string
	// RepoCache is the path to a local bare git repository without remotes that
	// serves as the source of every worktree. Required.
	RepoCache string
	// WorktreesRoot is the parent directory where ephemeral worktrees are
	// created. Required.
	WorktreesRoot string
	// BaseRef is the commit or branch the worktree starts from. Defaults to
	// "main" when empty.
	BaseRef string
	// SandboxMounts holds the bwrap mount arguments that the runner appends to
	// the worktree and chdir flags. See the design document for the canonical
	// layout; tests use a curated subset.
	SandboxMounts []string
	// TotalTimeout bounds the entire task wall-clock duration. Zero defaults to
	// 15 minutes.
	TotalTimeout time.Duration
	// IdleTimeout bounds the time between two consecutive NDJSON events. Zero
	// defaults to 5 minutes.
	IdleTimeout time.Duration
	// MaxToolCalls caps the number of tool_call events the agent may emit. Zero
	// defaults to 15. Exceeding the cap produces a fatal error event.
	MaxToolCalls int
	// ThinkingLevel is forwarded to gentle-shell as --thinking <level>. Empty
	// omits the flag so the user's default settings apply.
	ThinkingLevel string
	// Provider is forwarded to gentle-shell as --provider <name>. Empty omits
	// the flag so gentle-shell uses its built-in default.
	Provider string
	// Model is forwarded to gentle-shell as --model <pattern>. Empty omits the
	// flag so gentle-shell uses its built-in default.
	Model string
	// ExtraArgs are appended after the configured flags but before "--" so they
	// apply to gentle-shell/pi.
	ExtraArgs []string

	// commandFactory builds the bwrap/gentle-shell command. nil uses
	// exec.CommandContext via os/exec. Tests inject a fake to assert arguments
	// without spawning a real process.
	commandFactory CommandFactory
	// runCommand starts the command, returns its stdout/stderr pipes and waits
	// for completion. nil uses the os/exec default. Tests inject a fake to feed
	// canned NDJSON.
	runCommand CommandRunner
	// clock returns the current time. nil uses time.Now. Tests inject a fake.
	clock func() time.Time
	// sanitizeSecrets, when non-nil, is applied to the diff and the completion
	// text. nil skips sanitization.
	sanitizeSecrets SanitizeSecretsFunc
}

// SanitizeSecretsFunc is the signature of a pure secret-detector. It is shared
// by the streaming and the diff/bundle paths so a single change redaction
// policy covers every output channel.
type SanitizeSecretsFunc func(in string) (out string, redacted bool)

// CommandFactory builds a runnable command. The returned command is given to
// CommandRunner; the factory must not start the process itself.
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// commandWaitDelay is the fallback that os/exec applies if cmd.Cancel
// does not cause the child to exit: Go closes the pipes by force, which
// unblocks scanner.Scan and Wait, even when the child is still alive. The
// value is short enough to keep the worker responsive and long enough
// to cover a typical SIGKILL round-trip.
const commandWaitDelay = 2 * time.Second

// buildCommand creates the *exec.Cmd for the bwrap invocation, attaching
// the process-group kill, the SIGKILL-on-cancel hook, and a short
// WaitDelay. Together they guarantee the runner never blocks in Wait
// past the configured timeout, regardless of how the child behaves.
func (r *GentleShellRunner) buildCommand(runCtx context.Context, args ...string) *exec.Cmd {
	cmd := r.opts.commandFactory(runCtx, r.opts.Bwrap, args...)
	if cmd == nil {
		return nil
	}
	// Put bwrap in its own process group so SIGKILL reaches the whole
	// tree (bwrap, the inner sandbox, gentle-shell.mjs, pi). Without this
	// the kill would land on bwrap's PID only and the Node child would
	// survive.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// On cancel, skip SIGTERM (bwrap+Node ignore it) and go straight to
	// SIGKILL on the entire group. WaitDelay closes the pipes if SIGKILL
	// somehow fails, so Wait returns either way within a bounded budget.
	cmd.WaitDelay = commandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err != nil {
			pgid = cmd.Process.Pid
		}
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return cmd
}

// CommandRunner abstracts how a built command is started and awaited. The real
// implementation uses os/exec; tests provide a deterministic fake.
type CommandRunner interface {
	Run(ctx context.Context, cmd *exec.Cmd) (stdout io.Reader, stderr *cappedBuffer, wait func() error, startErr error)
}

// defaultCommandRunner delegates to os/exec.
type defaultCommandRunner struct{}

func (defaultCommandRunner) Run(ctx context.Context, cmd *exec.Cmd) (io.Reader, *cappedBuffer, func() error, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr := &cappedBuffer{limit: maxGentleShellStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to start command: %w", err)
	}
	return stdout, stderr, cmd.Wait, nil
}

// GentleShellRunner executes tasks by spawning gentle-shell inside a bwrap
// sandbox over an ephemeral git worktree. It is the production counterpart of
// PiRunner: instead of inheriting the local filesystem, it is hermetic and
// never has write access to the mesh worker's host secrets.
//
// The runner is responsible for:
//  1. Creating the ephemeral worktree from a local bare cache (no remotes).
//  2. Building the bwrap invocation that hides the user's HOME.
//  3. Streaming and translating NDJSON events to the gentle-mesh protocol.
//  4. Extracting a sanitized diff and emitting a completion event.
//  5. Cleaning up the worktree unconditionally via defer.
type GentleShellRunner struct {
	opts GentleShellRunnerOptions
}

// defaultSystemMounts returns the bwrap whitelist documented in the design
// (section "Comando Final del Runner Acotado"), minus the Go toolchain
// entries that became conditional in 2026-10-15. It deliberately avoids
// `--ro-bind / /` so the user's HOME is hidden under a tmpfs; the runner
// only re-exposes the directories required to launch Node, Pi,
// gentle-shell and its own runtime config. The default layout is built at
// runtime from this slice, the conditional Go toolchain mounts, and the
// required RepoCache bind (see buildDefaultSandboxMounts).
//
// HOME-derived paths use the host user's HOME directory (os.UserHomeDir)
// rather than a hard-coded /home/<user>, so the runner works on machines
// where the worker user has a different home (root, custom accounts,
// non-Linux defaults, CI containers, etc.).
func defaultSystemMounts() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Last-resort fallback: keep the documented layout. A bwrap that
		// can't resolve --tmpfs <home> won't start, but that's a louder failure
		// than silently mounting the wrong path.
		home = "/home/rafael"
	}
	return []string{
		// Debian base system, mounted read-only with usr-merge symlinks.
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--symlink", "usr/sbin", "/sbin",
		"--ro-bind", "/etc", "/etc",
		// Allow resolution of /etc/resolv.conf when it is a symlink to
		// systemd-resolved's stub (/run/systemd/resolve/stub-resolv.conf) or
		// resolvconf (/run/resolvconf), common on modern Debian/Ubuntu systems.
		// Ignored if not present on the host.
		"--ro-bind-try", "/run/systemd/resolve", "/run/systemd/resolve",
		"--ro-bind-try", "/run/resolvconf", "/run/resolvconf",
		// Devices, /proc and a writable /tmp.
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		// Hide the user's HOME completely.
		"--tmpfs", home,
		// Whitelist the runtime files gentle-shell needs.
		"--ro-bind", filepath.Join(home, ".local"), filepath.Join(home, ".local"),
		"--bind", filepath.Join(home, ".gentle-shell"), filepath.Join(home, ".gentle-shell"),
	}
}

// defaultGoToolchainMountTemplates are the host paths the runner binds into the
// sandbox so the Go toolchain (`go build`, `go test`) works inside the
// sandbox when a task needs it.
//
// History: these four `--bind` / `--ro-bind` entries were originally hard
// coded into the single `defaultSandboxMounts` slice because the only
// validated end-to-end task (issue 43, gentle-shell-43-result-v2.md) used
// `go test`. Hosts that do not have a Go SDK installed at the canonical
// `~/qs-go` / `~/go/...` paths therefore failed at bwrap spawn time
// ("Can't find source path /home/rafael/qs-go") even for tasks that never
// invoked Go. The 2026-10-15 refactor (A2A trivial validation) makes
// every Go bind conditional on the host path existing, so workers without
// a Go toolchain can still launch sandboxes for non-Go tasks. The host
// paths are computed at runner-construction time from
// `os.UserHomeDir()` rather than hard-coded, so the whitelist adapts to
// the worker process that actually launches the sandbox.
var defaultGoToolchainMountTemplates = []struct {
	flag string // "--ro-bind" or "--bind"
	src  string // suffix joined onto the worker's HOME
}{
	// Go SDK on the path the design originally documented.
	{"--ro-bind", "qs-go"},
	// Go module and build caches. Modules download is read-only; the
	// pkg/mod download-cache and GOCACHE need read-write so `go build`
	// can populate them inside the sandbox.
	{"--ro-bind", "go/pkg/mod"},
	{"--bind", "go/pkg/mod/cache"},
	{"--bind", ".cache/go-build"},
}

// buildDefaultSandboxMounts composes the full default bwrap whitelist
// from three layers: the system mounts (always included), the Go
// toolchain mounts (included only when the host path exists), and the
// RepoCache bind (always included because the worktree's .git pointer
// resolves to the cache gitdir). It returns a fresh slice so callers can
// safely mutate it.
//
// The RepoCache bind is `--bind` (read-write) on purpose: git worktrees
// write their per-worktree index/HEAD/ORIG_HEAD/locks under
// `<cache>/worktrees/<task_id>/`, so a `--ro-bind` would make every
// `git add` / `git commit` fail with "filesystem read-only" even when
// the agent behaves correctly. The cache is still safe to expose RW
// because the runner strips remotes from the cache bare repository and
// the sandbox hides the user's HOME and credentials, so a runaway agent
// cannot push anything to a remote.
func buildDefaultSandboxMounts(repoCache string) []string {
	out := make([]string, 0, len(defaultSystemMounts())+3*len(defaultGoToolchainMountTemplates)+3)
	out = append(out, defaultSystemMounts()...)
	out = append(out, goToolchainMountsThatExist()...)
	if repoCache != "" {
		out = append(out, "--bind", repoCache, repoCache)
	}
	return out
}

// goToolchainMountsThatExist returns the subset of Go toolchain bind
// triples whose host source path actually exists on this host. The
// candidates are built from defaultGoToolchainMountTemplates joined onto
// the worker process's HOME directory (os.UserHomeDir). Each surviving
// entry is the standard `--<flag> <src> <dst>` triple so buildBwrapArgs
// can scan it linearly.
func goToolchainMountsThatExist() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	out := make([]string, 0, 3*len(defaultGoToolchainMountTemplates))
	for _, tmpl := range defaultGoToolchainMountTemplates {
		src := filepath.Join(home, tmpl.src)
		if !hostPathExists(src) {
			continue
		}
		out = append(out, tmpl.flag, src, src)
	}
	return out
}

// hostPathExists is the lookup used to decide whether a candidate bind
// triple survives into the default sandbox whitelist. Wrapping
// os.Stat keeps the policy overridable from tests if a future case needs
// it; today the production behaviour is "skip on missing host path".
func hostPathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// NewGentleShellRunner constructs a GentleShellRunner with default-friendly
// options. Callers are expected to provide at least TasksDir, RepoCache and
// WorktreesRoot; the rest of the fields are defaulted.
func NewGentleShellRunner(opts GentleShellRunnerOptions) *GentleShellRunner {
	if opts.Bwrap == "" {
		opts.Bwrap = "bwrap"
	}
	if opts.GentleShell == "" {
		opts.GentleShell = "gentle-shell"
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = 15 * time.Minute
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = 15
	}
	if opts.BaseRef == "" {
		opts.BaseRef = "main"
	}
	if len(opts.SandboxMounts) == 0 {
		opts.SandboxMounts = buildDefaultSandboxMounts(opts.RepoCache)
	}
	if opts.commandFactory == nil {
		opts.commandFactory = exec.CommandContext
	}
	if opts.runCommand == nil {
		opts.runCommand = defaultCommandRunner{}
	}
	if opts.clock == nil {
		opts.clock = time.Now
	}
	return &GentleShellRunner{opts: opts}
}

// Options returns a copy of the runner's configured options.
func (r *GentleShellRunner) Options() GentleShellRunnerOptions {
	return r.opts
}

// Run implements runner.Runner. It builds the worktree, launches the bwrap
// sandbox, streams the NDJSON events, and emits a single completion event
// (or a fatal error event).
func (r *GentleShellRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	if sink == nil {
		return errors.New("event sink is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.opts.TasksDir == "" {
		return r.failRun(sink, "GENTLESHELL_CONFIG_ERROR", errors.New("TasksDir is required"))
	}
	if r.opts.RepoCache == "" {
		return r.failRun(sink, "GENTLESHELL_CONFIG_ERROR", errors.New("RepoCache is required"))
	}
	if r.opts.WorktreesRoot == "" {
		return r.failRun(sink, "GENTLESHELL_CONFIG_ERROR", errors.New("WorktreesRoot is required"))
	}

	taskID := req.TaskID
	if taskID == "" {
		taskID = req.SessionID
	}
	if taskID == "" {
		return r.failRun(sink, "GENTLESHELL_CONFIG_ERROR", errors.New("TaskRequest.TaskID is required"))
	}

	prompt := req.Task
	if prompt == "" {
		prompt = req.Prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return r.failRun(sink, "GENTLESHELL_CONFIG_ERROR", errors.New("task prompt is empty"))
	}

	// Derive the worktree directory and the proposal branch.
	worktreeDir, err := r.resolveWorktreeDir(taskID)
	if err != nil {
		return r.failRun(sink, "GENTLESHELL_WORKTREE_ERROR", err)
	}
	branchName := "proposal/" + taskID

	// Cleanup is unconditional: whether the task succeeded, failed, timed out
	// or crashed, the worktree is removed and the local branch is preserved.
	defer r.cleanupWorktree(worktreeDir)

	// Prepare the worktree from the local bare cache. No remote is configured
	// on the cache, so the resulting worktree has no remote either, which is
	// the structural guarantee that "no push" provides.
	if err := r.prepareWorktree(ctx, worktreeDir, branchName); err != nil {
		return r.failRun(sink, "GENTLESHELL_WORKTREE_ERROR", err)
	}

	// Apply timeouts: total wins over any request-level value when smaller.
	totalTimeout := r.opts.TotalTimeout
	if req.TimeoutSeconds > 0 {
		if requestTimeout := time.Duration(req.TimeoutSeconds) * time.Second; requestTimeout < totalTimeout {
			totalTimeout = requestTimeout
		}
	}
	runCtx, cancelRun := context.WithTimeout(ctx, totalTimeout)
	defer cancelRun()

	// Build the prompt the agent will see. We keep the user request verbatim
	// and add a strict stop directive so the agent does not invent infinite
	// follow-up turns after MaxToolCalls is hit.
	structuredPrompt := r.buildPrompt(prompt, taskID)

	args := r.buildBwrapArgs(worktreeDir, structuredPrompt, req.SessionID)

	// buildCommand wraps commandFactory so the runner can also configure
	// the os/exec kill semantics: Setpgid so SIGKILL goes to the entire
	// process tree (bwrap + sandboxed Node + pi), and Cancel+WaitDelay so
	// the runCtx deadline tears the child down even when the child
	// ignores SIGTERM and keeps writing to stdout.
	cmd := r.buildCommand(runCtx, args...)

	stdout, stderr, wait, startErr := r.opts.runCommand.Run(runCtx, cmd)
	if startErr != nil {
		return r.failRun(sink, "GENTLESHELL_START_ERROR", startErr)
	}

	state := &gentleShellStream{
		sink:          sink,
		startedAt:     r.opts.clock(),
		idleTimeout:   r.opts.IdleTimeout,
		maxToolCalls:  r.opts.MaxToolCalls,
		toolCallsSeen: new(int64),
	}
	readErr := state.consume(runCtx, stdout)

	// Decide the cause of termination BEFORE calling wait: the child may
	// ignore SIGTERM (bwrap+Node do), so the kill must already be in flight
	// by the time we block in wait(). SIGKILL escalation is scheduled by
	// killProcessTree itself, so even a stubborn child is reaped within
	// sandboxSIGKILLGrace.
	cause := classifyExit(runCtx, readErr)
	if cause.needsKill {
		r.killProcessTree(cmd)
	}

	// Always wait so a started process is reaped.
	waitErr := wait()
	_ = waitErr // Errors are reported via the streaming context below.

	// Surface the terminal event to the caller. classifyExit decided which
	// event to emit, and the kill was issued before wait so the runner
	// never blocks indefinitely.
	switch cause.eventCode {
	case "TASK_TIMEOUT":
		return r.failRun(sink, "TASK_TIMEOUT", fmt.Errorf("task exceeded total timeout"))
	case "TASK_INACTIVITY_TIMEOUT":
		return r.failRun(sink, "TASK_INACTIVITY_TIMEOUT", fmt.Errorf("inactivity timeout exceeded"))
	case "ERR_MAX_TOOL_CALLS_EXCEEDED":
		return r.failRun(sink, "ERR_MAX_TOOL_CALLS_EXCEEDED", fmt.Errorf("max tool calls exceeded"))
	}
	// Parent-context cancellation: classifyExit requested a kill but did
	// not set an event code. Bubble the context error up to the caller.
	if runCtxErr := runCtx.Err(); runCtxErr != nil {
		return runCtxErr
	}
	if readErr != nil {
		return r.failRun(sink, "GENTLESHELL_STREAM_ERROR", readErr)
	}

	// Non-zero exit is reported as a fatal error.
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		exitErr := fmt.Errorf("gentle-shell exited with error: %w", waitErr)
		if detail != "" {
			exitErr = fmt.Errorf("%w: %s", exitErr, detail)
		}
		return r.failRun(sink, "GENTLESHELL_EXIT_ERROR", exitErr)
	}

	// Success: extract, sanitize and emit the diff.
	diff, commit, err := r.collectDiff(worktreeDir)
	if err != nil {
		return r.failRun(sink, "GENTLESHELL_DIFF_ERROR", err)
	}
	if r.opts.sanitizeSecrets != nil {
		diff, _ = r.opts.sanitizeSecrets(diff)
	}

	// NO_COMMIT_PRODUCED guard: the agent finished without error and emitted
	// a confident completion text, but the proposal is empty (HEAD still
	// at BaseRef) or the working tree has unstaged changes that were never
	// committed. Without this check, callers that key off EventCompletion
	// would mark the task as successful even though the proposal branch
	// contains no work. The contract is documented in the design (section
	// "Completion Contract") and pinned by
	// TestGentleShellRunner_EmitsNoCommitProducedWhenAgentLies.
	if verifyErr := r.verifyCommitProduced(worktreeDir, commit); verifyErr != nil {
		return r.failRun(sink, "NO_COMMIT_PRODUCED", verifyErr)
	}

	completion := protocol.CompletionPayload{
		Result:     state.fullText.String(),
		Text:       state.fullText.String(),
		Branch:     branchName,
		CommitHash: commit,
		DurationMs: r.opts.clock().Sub(state.startedAt).Milliseconds(),
	}

	if len(diff) <= gentleShellDiffInlineLimit {
		completion.Result = appendDiffToResult(completion.Result, diff)
		completion.Text = completion.Result
	} else {
		patchPath, urlErr := r.persistOversizedDiff(taskID, diff)
		if urlErr != nil {
			return r.failRun(sink, "GENTLESHELL_DIFF_ERROR", urlErr)
		}
		completion.Result = appendDiffURLToResult(completion.Result, patchPath)
		completion.Text = completion.Result
	}

	// Usage exposure: F2-3 reads tokens/cost from the task state without
	// re-parsing pi's NDJSON stream.
	completion.Result = appendUsageToResult(completion.Result, state.usage)
	completion.Text = completion.Result

	_, emitErr := sink.EmitEvent(protocol.EventCompletion, completion)
	if emitErr != nil {
		return fmt.Errorf("failed to emit completion event: %w", emitErr)
	}
	return nil
}

// resolveWorktreeDir returns the absolute path of the ephemeral worktree for
// the given task. It uses WorktreesRoot as the parent and a per-task leaf
// name; the directory is not created here, that happens in prepareWorktree.
func (r *GentleShellRunner) resolveWorktreeDir(taskID string) (string, error) {
	clean := filepath.Clean(taskID)
	if clean == "" || clean == "." || strings.ContainsAny(clean, "/\\") {
		return "", fmt.Errorf("invalid task id %q for worktree path", taskID)
	}
	abs, err := filepath.Abs(filepath.Join(r.opts.WorktreesRoot, clean))
	if err != nil {
		return "", fmt.Errorf("failed to resolve worktree path: %w", err)
	}
	return abs, nil
}

// prepareWorktree creates the worktree directory and the proposal branch
// from RepoCache. The cache is a local bare repository with no remotes, so
// the resulting worktree cannot push anywhere unless a remote is added
// explicitly, which the agent cannot do because /home/rafael/.gitconfig is
// hidden by the bwrap sandbox.
//
// The worktree is built in two phases on purpose. `git worktree add -b <branch>`
// moves HEAD to the new branch *before* it checks files out, and the cache config
// can make a filter driver conditional on the current branch
// (`includeIf "onbranch:proposal/*"`). Enumerating the drivers before that point
// sees the cache with HEAD still on the base branch, where such an include is
// inactive, so the enumeration would report no driver while the checkout then
// runs it on the host. So the worktree is created with --no-checkout (which
// checks nothing out, so no filter can fire), the drivers are enumerated *inside
// the new worktree* where git will actually read them, and only then are the
// files materialized with that enumeration's neutralization in place.
func (r *GentleShellRunner) prepareWorktree(ctx context.Context, worktreeDir, branch string) error {
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return fmt.Errorf("failed to create worktrees root: %w", err)
	}
	// --force would mask a stale directory; we explicitly refuse to overwrite so
	// two concurrent tasks do not stomp on each other.
	gitArgs, gitEnv, err := gitsafe.Plan(r.opts.RepoCache,
		"worktree", "add", "--no-checkout", "-b", branch, worktreeDir, r.opts.BaseRef)
	if err != nil {
		return fmt.Errorf("build git worktree add: %w", err)
	}
	cmd := r.opts.commandFactory(ctx, "git", gitArgs...)
	if cmd == nil {
		return errors.New("build git worktree add: command factory returned no command")
	}
	cmd.Env = append(gitsafe.Sanitize(os.Environ()), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Second phase: enumerate where git will read the config, then materialize
	// the files with that enumeration neutralized. read-tree is plumbing, so it
	// populates the working tree without moving HEAD and without the
	// "HEAD is now at ..." output reset --hard would print.
	argv, env, err := gitsafe.Plan(worktreeDir, "read-tree", "-m", "-u", "HEAD")
	if err != nil {
		return fmt.Errorf("build git read-tree for %s: %w", worktreeDir, err)
	}
	populate := exec.CommandContext(ctx, "git", argv...)
	populate.Env = append(gitsafe.Sanitize(os.Environ()), env...)
	if out, err := populate.CombinedOutput(); err != nil {
		return fmt.Errorf("git read-tree failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cleanupWorktree removes the ephemeral worktree and prunes any leftover
// metadata in RepoCache. The proposal branch is preserved in the cache so
// Rafael can inspect it later via the coordinator's /v1/tasks/{id}/bundle
// endpoint.
func (r *GentleShellRunner) cleanupWorktree(worktreeDir string) {
	if worktreeDir == "" {
		return
	}
	// The repo cache path is recovered from the worktree's .git pointer file.
	gitFile := filepath.Join(worktreeDir, ".git")
	data, err := os.ReadFile(gitFile)
	if err != nil {
		// Either the worktree never existed or it was already removed.
		_ = os.RemoveAll(worktreeDir)
		return
	}
	pointer := strings.TrimSpace(string(data))
	cachePath := strings.TrimPrefix(pointer, "gitdir: ")

	// Use git's own machinery to remove the worktree, falling back to a raw
	// recursive remove if git itself fails. This runs from a defer on the
	// task's way out, so it gets its own context rather than the task's.
	ctx := context.Background()
	if cachePath != "" && cachePath != gitFile {
		remove := gitMust(ctx, cachePath, "worktree", "remove", "--force", worktreeDir)
		if err := remove.Run(); err != nil {
			_ = os.RemoveAll(worktreeDir)
		}
		_ = gitRun(ctx, cachePath, "worktree", "prune")
	} else {
		_ = os.RemoveAll(worktreeDir)
	}
}

// collectDiff returns the unified diff of the worktree against the base ref
// and the HEAD commit hash. It runs in the worker daemon's filesystem, not
// inside the sandbox, so the agent's output is read-only and trusted.
func (r *GentleShellRunner) collectDiff(worktreeDir string) (string, string, error) {
	diffCmd, err := hostGit(context.Background(), worktreeDir,
		"diff", "--no-ext-diff", "--no-textconv", r.opts.BaseRef+"..HEAD")
	if err != nil {
		return "", "", fmt.Errorf("build git diff: %w", err)
	}
	var diffOut bytes.Buffer
	diffCmd.Stdout = &diffOut
	if err := diffCmd.Run(); err != nil {
		return "", "", fmt.Errorf("git diff failed: %w", err)
	}

	revCmd, err := hostGit(context.Background(), worktreeDir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("build git rev-parse: %w", err)
	}
	revOut, err := revCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	commit := strings.TrimSpace(string(revOut))
	return diffOut.String(), commit, nil
}

// verifyCommitProduced enforces the completion contract that the proposal
// branch contains at least one new commit on top of BaseRef AND the working
// tree is clean. Both checks fire the same NO_COMMIT_PRODUCED error so the
// caller sees a single cause regardless of which one tripped.
func (r *GentleShellRunner) verifyCommitProduced(worktreeDir, headCommit string) error {
	baseSHA, err := r.resolveBaseSHA()
	if err != nil {
		return fmt.Errorf("verify base ref: %w", err)
	}
	if headCommit == baseSHA {
		return fmt.Errorf("agent reported done but no new commit was produced on branch (HEAD still at base ref %s)", shortSHA(baseSHA))
	}
	statusCmd, err := hostGit(context.Background(), worktreeDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("build git status: %w", err)
	}
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	if err := statusCmd.Run(); err != nil {
		return fmt.Errorf("verify working tree: %w", err)
	}
	if trimmed := strings.TrimSpace(statusOut.String()); trimmed != "" {
		return fmt.Errorf("agent reported done but working tree has uncommitted changes:\n%s", trimmed)
	}
	return nil
}

// resolveBaseSHA returns the SHA-1 of BaseRef as resolved against the
// local bare RepoCache.
func (r *GentleShellRunner) resolveBaseSHA() (string, error) {
	cmd, err := hostGit(context.Background(), r.opts.RepoCache, "rev-parse", r.opts.BaseRef)
	if err != nil {
		return "", fmt.Errorf("build git rev-parse %s: %w", r.opts.BaseRef, err)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", r.opts.BaseRef, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// persistOversizedDiff writes a diff to <tasks-dir>/<task_id>/proposal.patch
// and returns the absolute path. The endpoint /v1/tasks/{id}/diff serves
// that file with the same secret-sanitization filter applied at the
// coordinator boundary.
func (r *GentleShellRunner) persistOversizedDiff(taskID, diff string) (string, error) {
	dir := filepath.Join(r.opts.TasksDir, taskID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create task dir: %w", err)
	}
	patchPath := filepath.Join(dir, "proposal.patch")
	if err := os.WriteFile(patchPath, []byte(diff), 0600); err != nil {
		return "", fmt.Errorf("failed to write proposal patch: %w", err)
	}
	return patchPath, nil
}

// buildPrompt wraps the user prompt with the strict tool-call budget
// directive and with the completion contract. The agent must emit a
// summary and stop after MaxToolCalls actions, which the runtime enforces
// in parallel, and it must leave its work as a commit on the proposal
// branch: the harness verifies HEAD against the base ref deterministically
// and fails the task with NO_COMMIT_PRODUCED when the proposal is empty,
// however confident the agent's final message sounds.
func (r *GentleShellRunner) buildPrompt(userPrompt, taskID string) string {
	constraint := fmt.Sprintf(
		"\n\n[runner directive] task_id=%s: you have a hard budget of %d tool calls. After that budget is exhausted, emit a single final summary text and stop."+
			"\n\n[runner directive] commit before you finish: `git add` your changes and create a commit with `git commit -m` on the current branch (the proposal branch is already checked out)."+
			" The harness only accepts work that exists as a commit; if you stop without one the task fails with NO_COMMIT_PRODUCED no matter what you say in your final message.",
		taskID, r.opts.MaxToolCalls,
	)
	return userPrompt + constraint
}

// buildBwrapArgs assembles the bwrap command line. The bwrap call performs
// the filesystem whitelist, hides the user's HOME and the host's git
// credentials, sets the worktree as the working directory, and finally
// launches gentle-shell with the structured prompt.
// sandboxGitIdentityName and sandboxGitIdentityEmail are the fixed identity
// exported into every sandbox. They are deliberately not the host user's
// name or address: the sandbox never sees the host git configuration, and
// the proposal commit is attributed to the agent, not to the operator.
const (
	sandboxGitIdentityName  = "gentle-mesh-agent"
	sandboxGitIdentityEmail = "agent@gentle-mesh.invalid"
)

func (r *GentleShellRunner) buildBwrapArgs(worktreeDir, prompt, sessionID string) []string {
	args := []string{}

	// Whitelisted mounts defined by the design (no --ro-bind / /).
	args = append(args, r.opts.SandboxMounts...)

	// Bind the worktree itself as the only writable code path and chdir into
	// it so the agent's relative paths resolve there.
	args = append(args, "--bind", worktreeDir, worktreeDir)
	args = append(args, "--chdir", worktreeDir)
	args = append(args, "--unshare-pid")

	// The sandbox hides the user's HOME under a tmpfs and mounts /etc
	// read-only, and hosts commonly ship no /etc/gitconfig, so `git commit`
	// inside the sandbox would otherwise fail with "Author identity
	// unknown" and the agent could never satisfy the completion contract.
	// A fixed, non-personal identity is exported instead: the sandbox has no
	// credentials to leak and the harness does not attribute the commit to a
	// person, it only needs HEAD to move past the base ref.
	args = append(args,
		"--setenv", "GIT_AUTHOR_NAME", sandboxGitIdentityName,
		"--setenv", "GIT_AUTHOR_EMAIL", sandboxGitIdentityEmail,
		"--setenv", "GIT_COMMITTER_NAME", sandboxGitIdentityName,
		"--setenv", "GIT_COMMITTER_EMAIL", sandboxGitIdentityEmail,
		// Never let the agent push: the cache has no remotes on purpose.
		"--setenv", "GIT_TERMINAL_PROMPT", "0",
	)

	args = append(args, "--")

	// gentle-shell --isolated -- <pi-args> -- <prompt>
	gsArgs := []string{r.opts.GentleShell, "--isolated", "--"}

	piArgs := []string{"--print", "--mode", "json", "--no-themes", "--no-prompt-templates"}
	if sessionID != "" {
		piArgs = append(piArgs, "--session-id", sessionID)
	}
	if r.opts.ThinkingLevel != "" {
		piArgs = append(piArgs, "--thinking", r.opts.ThinkingLevel)
	}
	if r.opts.Provider != "" {
		piArgs = append(piArgs, "--provider", r.opts.Provider)
	}
	if r.opts.Model != "" {
		piArgs = append(piArgs, "--model", r.opts.Model)
	}
	piArgs = append(piArgs, r.opts.ExtraArgs...)
	piArgs = append(piArgs, "--")

	gsArgs = append(gsArgs, piArgs...)
	gsArgs = append(gsArgs, prompt)
	return append(args, gsArgs...)
}

// sandboxSIGKILLGrace is the wait between SIGTERM and SIGKILL when the
// process refuses to exit. Three seconds is short enough to keep the worker
// responsive and long enough for Node and bwrap to flush their pipes.
const sandboxSIGKILLGrace = 3 * time.Second

// killProcessTree sends SIGTERM to the child process and escalates to SIGKILL
// after a short grace period so a child that ignores SIGTERM does not leak.
// SIGKILL of a bwrap process group also kills the gentle-shell process tree
// because bwrap already detached it. The function is asynchronous: it
// returns immediately and the SIGKILL escalation runs in a goroutine.
func (r *GentleShellRunner) killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	go func(pid int) {
		time.Sleep(sandboxSIGKILLGrace)
		if proc, err := os.FindProcess(pid); err == nil && proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}(cmd.Process.Pid)
}

// exitCause bundles the run-classification produced by classifyExit so
// the runner can decide whether to kill the child and which event to emit
// before blocking on wait.
type exitCause struct {
	needsKill bool
	eventCode string // empty when the run is healthy
}

// classifyExit inspects runCtx and readErr and returns the run classification
// the runner needs to handle the termination. The order is important:
// the TotalTimeout check runs first because a silent child (e.g. a hung
// bwrap) only manifests through the context; an explicit inactivity or
// tool-call limit surfaces as the more specific cause.
func classifyExit(runCtx context.Context, readErr error) exitCause {
	if runCtxErr := runCtx.Err(); runCtxErr != nil {
		if errors.Is(runCtxErr, context.DeadlineExceeded) {
			return exitCause{needsKill: true, eventCode: "TASK_TIMEOUT"}
		}
		// Caller cancellation: stop the child cleanly and bubble up.
		return exitCause{needsKill: true}
	}
	if readErr != nil {
		if errors.Is(readErr, errMaxToolCalls) {
			return exitCause{needsKill: true, eventCode: "ERR_MAX_TOOL_CALLS_EXCEEDED"}
		}
		if errors.Is(readErr, errInactivityTimeout) {
			return exitCause{needsKill: true, eventCode: "TASK_INACTIVITY_TIMEOUT"}
		}
	}
	return exitCause{needsKill: false}
}

// failRun records a fatal error event and returns the underlying failure.
func (r *GentleShellRunner) failRun(sink EventSink, code string, err error) error {
	if _, emitErr := sink.EmitEvent(protocol.EventError, protocol.ErrorPayload{
		Code:    code,
		Message: err.Error(),
		Fatal:   true,
	}); emitErr != nil {
		return fmt.Errorf("%w (failed to emit error event: %v)", err, emitErr)
	}
	return err
}

// gentleShellStream translates gentle-shell NDJSON events into gentle-mesh
// protocol events while enforcing the inactivity and tool-call budgets.
type gentleShellStream struct {
	sink          EventSink
	fullText      strings.Builder
	usage         *piUsage
	startedAt     time.Time
	idleTimeout   time.Duration
	maxToolCalls  int
	toolCallsSeen *int64
}

var errMaxToolCalls = errors.New("max tool calls exceeded")
var errInactivityTimeout = errors.New("inactivity timeout")

func (s *gentleShellStream) consume(ctx context.Context, out io.Reader) error {
	// Pump NDJSON lines through a channel so the inactivity timer can fire
	// even when the child is silent. Without this, a child that prints one
	// line and then sleeps would never trigger the idle check because the
	// blocking scanner.Scan would only return when the next line arrives.
	lines := make(chan []byte, 16)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 0, 64*1024), maxGentleShellEventLine)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			select {
			case lines <- line:
			case <-ctx.Done():
				scanErr <- ctx.Err()
				return
			}
		}
		if err := scanner.Err(); err != nil {
			scanErr <- err
		}
	}()

	idleTimer := time.NewTimer(s.idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idleTimer.C:
			return errInactivityTimeout
		case err := <-scanErr:
			return err
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(s.idleTimeout)

			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var evt gentleShellEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			if err := s.handle(evt); err != nil {
				return err
			}
		}
	}
}

func (s *gentleShellStream) handle(evt gentleShellEvent) error {
	switch evt.Type {
	case gentleShellEventMessageUpdate:
		return s.handleMessageUpdate(evt)
	case gentleShellEventMessageEnd:
		s.handleMessageEnd(evt)
		return nil
	case gentleShellEventToolExecutionStart:
		count := atomic.AddInt64(s.toolCallsSeen, 1)
		if int(count) > s.maxToolCalls {
			return fmt.Errorf("%w: %d > %d", errMaxToolCalls, count, s.maxToolCalls)
		}
		args := evt.Args
		if args == nil {
			args = map[string]any{}
		}
		_, err := s.sink.EmitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
			CallID: evt.ToolCallID,
			Tool:   evt.ToolName,
			Args:   args,
		})
		return err
	case gentleShellEventToolExecutionEnd:
		_, err := s.sink.EmitEvent(protocol.EventToolResult, protocol.ToolResultPayload{
			CallID:  evt.ToolCallID,
			Output:  toolResultText(evt.Result),
			IsError: evt.IsError,
		})
		return err
	default:
		return nil
	}
}

func (s *gentleShellStream) handleMessageUpdate(evt gentleShellEvent) error {
	update := evt.AssistantMessageEvent
	if update == nil || update.Delta == "" {
		return nil
	}
	switch update.Type {
	case "thinking_delta":
		_, err := s.sink.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: update.Delta})
		return err
	case "text_delta":
		s.fullText.WriteString(update.Delta)
		_, err := s.sink.EmitEvent(protocol.EventToken, protocol.TokenPayload{Text: update.Delta})
		return err
	default:
		return nil
	}
}

func (s *gentleShellStream) handleMessageEnd(evt gentleShellEvent) {
	// Usage capture: the final message_end carries the run total; interim
	// turns observed reporting zeros, so only non-zero totals overwrite.
	if evt.Message != nil && evt.Message.Usage != nil && evt.Message.Usage.TotalTokens > 0 {
		s.usage = evt.Message.Usage
	}
	if s.fullText.Len() > 0 || evt.Message == nil || evt.Message.Role != "assistant" {
		return
	}
	for _, part := range evt.Message.Content {
		if part.Type == "text" {
			s.fullText.WriteString(part.Text)
		}
	}
}

// gentleShellEvent is the subset of the gentle-shell NDJSON stream translated
// by GentleShellRunner. The schema matches pi's, since gentle-shell simply
// forwards pi's stream.
type gentleShellEvent struct {
	Type                  string              `json:"type"`
	AssistantMessageEvent *gentleShellEventD  `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string              `json:"toolCallId,omitempty"`
	ToolName              string              `json:"toolName,omitempty"`
	Args                  map[string]any      `json:"args,omitempty"`
	Result                json.RawMessage     `json:"result,omitempty"`
	IsError               bool                `json:"isError,omitempty"`
	Message               *gentleShellMessage `json:"message,omitempty"`
}

type gentleShellEventD struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}

type gentleShellMessage struct {
	Role    string                   `json:"role,omitempty"`
	Content []gentleShellContentPart `json:"content,omitempty"`
	Usage   *piUsage                 `json:"usage,omitempty"`
}

type gentleShellContentPart struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

const (
	gentleShellEventMessageUpdate      = "message_update"
	gentleShellEventMessageEnd         = "message_end"
	gentleShellEventToolExecutionStart = "tool_execution_start"
	gentleShellEventToolExecutionEnd   = "tool_execution_end"
)

// appendDiffToResult appends a diff to a completion result message so the
// viewer that already knows the result field can show it inline. The result
// is kept short enough to fit in the inline budget; larger diffs use
// appendDiffURLToResult instead.
func appendDiffToResult(result, diff string) string {
	if diff == "" {
		return result
	}
	if result == "" {
		return "Proposal diff:\n" + diff
	}
	return result + "\n\nProposal diff:\n" + diff
}

// appendDiffURLToResult records a local patch path in the completion result.
func appendDiffURLToResult(result, patchPath string) string {
	if result == "" {
		return "Proposal diff (oversized, stored on disk): " + patchPath
	}
	return result + "\n\nProposal diff (oversized, stored on disk): " + patchPath
}
