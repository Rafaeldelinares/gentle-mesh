package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// gentleShellHappyStream is a real-shaped gentle-shell NDJSON event sequence.
// gentle-shell forwards pi's --print --mode json stream unchanged, so the
// schema matches pi's documented event format.
const gentleShellHappyStream = `{"type":"agent_start"}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"analyzing task"}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"small change"}}
{"type":"tool_execution_start","toolCallId":"call_1","toolName":"read","args":{"path":"pkg/x.go"}}
{"type":"tool_execution_end","toolCallId":"call_1","toolName":"read","result":{"content":"foo"},"isError":false}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"small change"}]}}
{"type":"agent_settled"}
`

// fakeGentleShellScript returns a POSIX shell script that:
//   - records its argv (one argument per line) to recordPath when non-empty
//   - replays a canned NDJSON stream to stdout
//   - appends stderr detail to stderr
//   - exits with exitCode
func fakeGentleShellScript(recordPath, stream, stderr string, exitCode int) string {
	var b strings.Builder
	if recordPath != "" {
		fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %s\n", shellQuote(recordPath))
	}
	if stream != "" {
		b.WriteString("cat <<'PI_EOF'\n")
		b.WriteString(stream)
		if !strings.HasSuffix(stream, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("PI_EOF\n")
	}
	if stderr != "" {
		fmt.Fprintf(&b, "printf '%%s\\n' %s >&2\n", shellQuote(stderr))
	}
	fmt.Fprintf(&b, "exit %d\n", exitCode)
	return b.String()
}

func writeFakeGentleShell(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gentle-shell harness requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-gentle-shell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("failed to write fake gentle-shell script: %v", err)
	}
	return path
}

// passBwrapScript strips everything up to the first "--" argument, then
// stages a sentinel edit and commits it before exec-ing gentle-shell.
// The sentinel commit simulates an agent that did its job: there is a
// real HEAD commit on top of BaseRef and the working tree is clean, which
// is exactly what the runner's verifyCommitProduced guard expects.
//
// It honors the runner's --chdir flag by scanning the bwrap argv for the
// pair and cd-ing into the second token before exec.
const passBwrapScript = `chdir_to=""
saw_chdir=0
for arg in "$@"; do
  if [ "$saw_chdir" = "1" ]; then
    chdir_to="$arg"
    saw_chdir=0
    continue
  fi
  if [ "$arg" = "--chdir" ]; then
    saw_chdir=1
    continue
  fi
done
while [ $# -gt 0 ] && [ "$1" != "--" ]; do
  shift
done
if [ $# -gt 0 ]; then
  shift
  if [ -n "$chdir_to" ]; then
    cd "$chdir_to" || exit 1
    git config user.email tester@example.com
    git config user.name tester
    echo "fake-agent-edit $(date +%s%N)" >> small-change.txt
    git add small-change.txt
    git commit -m "fake-agent-edit: small change"
  fi
  exec "$@"
fi
`

func writeFakeBwrap(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bwrap harness requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-bwrap")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("failed to write fake bwrap script: %v", err)
	}
	return path
}

func writePassBwrap(t *testing.T) string {
	return writeFakeBwrap(t, passBwrapScript)
}

// seedBareCache creates a local bare git repository with one commit and one
// "main" branch so the worktree add step has a valid base ref.
func seedBareCache(t *testing.T) (cachePath, worktreesRoot string) {
	t.Helper()
	root := t.TempDir()
	cachePath = filepath.Join(root, "cache.git")
	seedDir := filepath.Join(root, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatalf("failed to create seed dir: %v", err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = seedDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester",
			"GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=tester",
			"GIT_COMMITTER_EMAIL=tester@example.com",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, stderr.String())
		}
		return stdout.String()
	}
	runGit("init", "-b", "main", "--bare", cachePath)
	runGit("init", "-b", "main", seedDir)
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("failed to write seed file: %v", err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")
	runGit("remote", "add", "origin", cachePath)
	runGit("push", "-u", "origin", "main")
	worktreesRoot = filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatalf("failed to create worktrees root: %v", err)
	}
	return cachePath, worktreesRoot
}

func defaultRunnerOptions(t *testing.T, extra runner.GentleShellRunnerOptions) runner.GentleShellRunnerOptions {
	if extra.TasksDir == "" {
		extra.TasksDir = t.TempDir()
	}
	// SandboxMounts is left unset so the runner falls back to its documented
	// default whitelist; tests that need a custom layout override explicitly.
	return extra
}

func TestGentleShellRunner_HappyPathTranslatesEvents(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	bwrapArgsPath := filepath.Join(t.TempDir(), "bwrap-argv.txt")
	gsArgsPath := filepath.Join(t.TempDir(), "gs-argv.txt")

	bwrapBody := "printf '%s\\n' \"$@\" > " + shellQuote(bwrapArgsPath) + "\n" +
		"for arg in \"$@\"; do echo \"$arg\" >> " + shellQuote(gsArgsPath) + "\ndone\n" +
		passBwrapScript
	bwrapScript := writeFakeBwrap(t, bwrapBody)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(gsArgsPath, gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         bwrapScript,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		ThinkingLevel: "minimal",
		TotalTimeout:  2 * time.Minute,
		IdleTimeout:   30 * time.Second,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-43",
		Agent:  "worker",
		Task:   "wrap pi runner in localExecutionSink",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	wantSequence := []protocol.EventType{
		protocol.EventThought,
		protocol.EventToken,
		protocol.EventToolCall,
		protocol.EventToolResult,
		protocol.EventCompletion,
	}
	if got := eventTypes(sink.Events()); !reflect.DeepEqual(got, wantSequence) {
		t.Fatalf("expected event sequence %v, got %v", wantSequence, eventTypes(sink.Events()))
	}

	var completion protocol.CompletionPayload
	if err := sink.Events()[len(sink.Events())-1].UnmarshalPayload(&completion); err != nil {
		t.Fatalf("failed to unmarshal completion: %v", err)
	}
	if completion.Branch != "proposal/task-43" {
		t.Errorf("expected branch %q, got %q", "proposal/task-43", completion.Branch)
	}
	if len(completion.CommitHash) != 40 {
		t.Errorf("expected 40-char commit hash, got %q", completion.CommitHash)
	}
	if !strings.Contains(completion.Result, "small change") {
		t.Errorf("expected completion result to include assistant text, got %q", completion.Result)
	}

	bwrapArgs := readLines(t, bwrapArgsPath)
	// The default SandboxMounts must match the design document line by
	// line. Verify every system token of the documented whitelist is
	// present. Go toolchain binds are intentionally skipped because
	// they are conditional on the host path existing.
	//
	// The HOME-derived paths (--tmpfs <home>, .local, .gentle-shell) are
	// computed from os.UserHomeDir() rather than hard-coded so the test
	// follows the worker's real HOME. The runner derives the same paths
	// from os.UserHomeDir(), so any host (CI runner user, dev machine,
	// root) produces matching argv tokens without this test pinning a
	// particular user layout.
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		t.Fatalf("os.UserHomeDir returned %q, err=%v; cannot build portable wantBwrapContains", homeDir, err)
	}
	wantBwrapContains := []string{
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--symlink", "usr/sbin", "/sbin",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind-try", "/run/systemd/resolve",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", homeDir,
		"--ro-bind", filepath.Join(homeDir, ".local"), filepath.Join(homeDir, ".local"),
		"--bind", filepath.Join(homeDir, ".gentle-shell"), filepath.Join(homeDir, ".gentle-shell"),
		"--chdir",
		"--unshare-pid",
	}
	for _, want := range wantBwrapContains {
		if !containsString(bwrapArgs, want) {
			t.Errorf("expected bwrap argv to contain %q, got %v", want, bwrapArgs)
		}
	}

	gsArgs := readLines(t, gsArgsPath)
	sep := indexOf(gsArgs, "--")
	if sep < 0 {
		t.Fatalf("expected '--' separator in gentle-shell argv: %v", gsArgs)
	}
	head := gsArgs[:sep+1]
	tail := gsArgs[sep+1:]

	wantHead := []string{"--isolated", "--"}
	if !reflect.DeepEqual(head, wantHead) {
		t.Errorf("expected gentle-shell head %v, got %v", wantHead, head)
	}
	secondSep := indexOf(tail, "--")
	if secondSep < 0 {
		t.Fatalf("expected second '--' separator in gentle-shell tail: %v", tail)
	}
	piHead := tail[:secondSep+1]
	wantPiHead := []string{
		"--print", "--mode", "json",
		"--no-themes", "--no-prompt-templates",
		"--thinking", "minimal",
		"--",
	}
	if !reflect.DeepEqual(piHead, wantPiHead) {
		t.Errorf("expected pi head %v, got %v", wantPiHead, piHead)
	}
	promptParts := tail[secondSep+1:]
	if len(promptParts) == 0 {
		t.Fatal("expected at least one prompt argument, got none")
	}
	joined := strings.Join(promptParts, "\n")
	if !strings.Contains(joined, "wrap pi runner in localExecutionSink") {
		t.Errorf("expected user prompt in argv, got %q", joined)
	}
	if !strings.Contains(joined, "[runner directive] task_id=task-43") {
		t.Errorf("expected runner directive appended to prompt, got %q", joined)
	}
}

func TestGentleShellRunner_ProviderAndModelFlagsAreForwarded(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	bwrapArgsPath := filepath.Join(t.TempDir(), "bwrap-argv.txt")
	gsArgsPath := filepath.Join(t.TempDir(), "gs-argv.txt")
	bwrapBody := "printf '%s\\n' \"$@\" > " + shellQuote(bwrapArgsPath) + "\n" +
		passBwrapScript
	bwrapScript := writeFakeBwrap(t, bwrapBody)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(gsArgsPath, gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         bwrapScript,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		ThinkingLevel: "minimal",
		Provider:      "minimax",
		Model:         "MiniMax-M3",
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-provider-model",
		Agent:  "worker",
		Task:   "use a specific provider and model",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	gsArgs := readLines(t, gsArgsPath)
	sep := indexOf(gsArgs, "--")
	if sep < 0 {
		t.Fatalf("expected '--' separator: %v", gsArgs)
	}
	tail := gsArgs[sep+1:]
	secondSep := indexOf(tail, "--")
	if secondSep < 0 {
		t.Fatalf("expected second '--' separator: %v", tail)
	}
	piHead := tail[:secondSep+1]
	wantPiHead := []string{
		"--print", "--mode", "json",
		"--no-themes", "--no-prompt-templates",
		"--thinking", "minimal",
		"--provider", "minimax",
		"--model", "MiniMax-M3",
		"--",
	}
	if !reflect.DeepEqual(piHead, wantPiHead) {
		t.Errorf("expected pi head %v, got %v", wantPiHead, piHead)
	}
}

func TestGentleShellRunner_EmptyProviderAndModelDoNotAddFlags(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	gsArgsPath := filepath.Join(t.TempDir(), "gs-argv.txt")
	bwrapScript := writePassBwrap(t)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(gsArgsPath, gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         bwrapScript,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		ThinkingLevel: "minimal",
		// Provider and Model left empty on purpose.
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-empty-provider",
		Agent:  "worker",
		Task:   "no provider or model override",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	gsArgs := readLines(t, gsArgsPath)
	for _, banned := range []string{"--provider", "--model"} {
		if containsString(gsArgs, banned) {
			t.Errorf("expected argv to NOT contain %q, got %v", banned, gsArgs)
		}
	}
}

func TestGentleShellRunner_TotalTimeoutEmitsFatalError(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	// Both bwrap and gentle-shell are sleepers. The runner must honor the
	// TotalTimeout and emit a TASK_TIMEOUT error event.
	sleeper := "#!/bin/sh\nexec sleep 30\n"
	sleeperPath := filepath.Join(t.TempDir(), "sleeper")
	if err := os.WriteFile(sleeperPath, []byte(sleeper), 0o755); err != nil {
		t.Fatalf("failed to write sleeper: %v", err)
	}

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         sleeperPath,
		GentleShell:   sleeperPath,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		TotalTimeout:  200 * time.Millisecond,
		IdleTimeout:   5 * time.Second,
	}))

	start := time.Now()
	sink := newMockSink(context.Background())
	err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-timeout",
		Agent:  "worker",
		Task:   "should time out",
	}, sink)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a task that exceeded total timeout")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected error to mention timeout, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("expected runner to honor total timeout within 5s, took %v", elapsed)
	}

	events := sink.Events()
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventError {
		t.Fatalf("expected a fatal error event, got %v", eventTypes(events))
	}
	var payload protocol.ErrorPayload
	if err := events[len(events)-1].UnmarshalPayload(&payload); err != nil {
		t.Fatalf("failed to unmarshal error: %v", err)
	}
	if payload.Code != "TASK_TIMEOUT" {
		t.Errorf("expected error code TASK_TIMEOUT, got %q", payload.Code)
	}
	if !payload.Fatal {
		t.Errorf("expected fatal=true")
	}
}

// TestGentleShellRunner_TotalTimeoutKillsProcessIgnoringSIGTERM reproduces
// the real-world failure mode where bwrap and gentle-shell both ignore
// SIGTERM. The runner must escalate to SIGKILL and report TASK_TIMEOUT
// within a bounded wall-clock budget, instead of blocking in wait().
func TestGentleShellRunner_TotalTimeoutKillsContinuousChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh trap to ignore SIGTERM and setpgid")
	}
	cache, worktreesRoot := seedBareCache(t)

	// Trap SIGTERM (ignored) and emit a tick every 50ms. This mirrors
	// the bwrap+Node behaviour: they refuse SIGTERM and keep streaming
	// events, so the runner cannot rely on stream silence to detect
	// timeout. With the SIGKILL-on-cancel+WaitDelay fix the runner
	// must still return within a few seconds.
	streamer := "#!/bin/sh\n" +
		"trap '' TERM\n" +
		"i=0\n" +
		"while [ $i -lt 1000 ]; do\n" +
		"  printf 'tick %d\\n' \"$i\"\n" +
		"  i=$((i+1))\n" +
		"  sleep 0.05\n" +
		"done\n"
	streamerPath := filepath.Join(t.TempDir(), "streamer")
	if err := os.WriteFile(streamerPath, []byte(streamer), 0o755); err != nil {
		t.Fatalf("failed to write streamer script: %v", err)
	}

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         streamerPath,
		GentleShell:   streamerPath,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		TotalTimeout:  2 * time.Second,
		IdleTimeout:   30 * time.Second,
	}))

	// Bound the whole test so a regression in the kill path cannot hang
	// the suite indefinitely.
	done := make(chan struct{})
	var (
		runErr error
		events []protocol.Event
	)
	go func() {
		defer close(done)
		sink := newMockSink(context.Background())
		runErr = r.Run(context.Background(), protocol.TaskRequest{
			TaskID: "task-timeout-streamer",
			Agent:  "worker",
			Task:   "should be killed via SIGKILL while still emitting",
		}, sink)
		events = sink.Events()
	}()

	const maxWallClock = 6 * time.Second
	start := time.Now()
	select {
	case <-done:
	case <-time.After(maxWallClock):
		t.Fatalf("runner did not return within %v (continuous child not killed)", maxWallClock)
	}
	elapsed := time.Since(start)
	if elapsed > maxWallClock {
		t.Errorf("runner took too long: %v (limit %v)", elapsed, maxWallClock)
	}

	if runErr == nil {
		t.Fatal("expected an error for a task that exceeded total timeout")
	}
	if !strings.Contains(runErr.Error(), "timeout") {
		t.Errorf("expected error to mention timeout, got: %v", runErr)
	}
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventError {
		t.Fatalf("expected a fatal error event, got %v", eventTypes(events))
	}
	var payload protocol.ErrorPayload
	if err := events[len(events)-1].UnmarshalPayload(&payload); err != nil {
		t.Fatalf("failed to unmarshal error: %v", err)
	}
	if payload.Code != "TASK_TIMEOUT" {
		t.Errorf("expected TASK_TIMEOUT, got %q", payload.Code)
	}
}

func TestGentleShellRunner_IdleTimeoutEmitsFatalError(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	// Fake gentle-shell prints one event then sleeps; the runner must give
	// up on the IdleTimeout and emit TASK_INACTIVITY_TIMEOUT.
	idleBody := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"agent_start\"}'\nexec sleep 30\n"
	idlePath := filepath.Join(t.TempDir(), "idle-gs")
	if err := os.WriteFile(idlePath, []byte(idleBody), 0o755); err != nil {
		t.Fatalf("failed to write idle script: %v", err)
	}

	passBwrap := writePassBwrap(t)

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   idlePath,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		TotalTimeout:  30 * time.Second,
		IdleTimeout:   250 * time.Millisecond,
	}))

	start := time.Now()
	sink := newMockSink(context.Background())
	err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-idle",
		Agent:  "worker",
		Task:   "should be killed for inactivity",
	}, sink)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for an idle task, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("expected runner to honor idle timeout within 5s, took %v", elapsed)
	}

	events := sink.Events()
	if len(events) == 0 || events[len(events)-1].Type != protocol.EventError {
		t.Fatalf("expected a fatal error event, got %v", eventTypes(events))
	}
	var payload protocol.ErrorPayload
	if err := events[len(events)-1].UnmarshalPayload(&payload); err != nil {
		t.Fatalf("failed to unmarshal error: %v", err)
	}
	if payload.Code != "TASK_INACTIVITY_TIMEOUT" {
		t.Errorf("expected TASK_INACTIVITY_TIMEOUT, got %q", payload.Code)
	}
}

func TestGentleShellRunner_CleansUpWorktreeOnFailure(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	passBwrap := writePassBwrap(t)
	failGS := writeFakeGentleShell(t, fakeGentleShellScript("", "", "synthetic failure", 7))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   failGS,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		TotalTimeout:  30 * time.Second,
		IdleTimeout:   5 * time.Second,
	}))

	sink := newMockSink(context.Background())
	err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-cleanup",
		Agent:  "worker",
		Task:   "should clean up after failure",
	}, sink)

	if err == nil {
		t.Fatal("expected an error for a non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "synthetic failure") {
		t.Errorf("expected stderr detail in error, got: %v", err)
	}

	worktreeDir := filepath.Join(worktreesRoot, "task-cleanup")
	if _, statErr := os.Stat(worktreeDir); !os.IsNotExist(statErr) {
		t.Errorf("expected worktree dir %q to be removed, stat err: %v", worktreeDir, statErr)
	}

	listCmd := exec.Command("git", "-C", cache, "worktree", "list", "--porcelain")
	out, listErr := listCmd.Output()
	if listErr != nil {
		t.Fatalf("git worktree list failed: %v", listErr)
	}
	if strings.Contains(string(out), worktreeDir) {
		t.Errorf("expected worktree to be detached from cache, got:\n%s", string(out))
	}
	branchCmd := exec.Command("git", "-C", cache, "branch", "--list", "proposal/task-cleanup")
	branchOut, branchErr := branchCmd.Output()
	if branchErr != nil {
		t.Fatalf("git branch --list failed: %v", branchErr)
	}
	if !strings.Contains(string(branchOut), "proposal/task-cleanup") {
		t.Errorf("expected proposal branch to remain, got:\n%s", string(branchOut))
	}
}

func TestGentleShellRunner_LocalExecutionSinkMarksExecutedLocally(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	passBwrap := writePassBwrap(t)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript("", gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
	}))

	inner := newMockSink(context.Background())
	sink := localExecutionSinkWrapper{EventSink: inner}

	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-local",
		Agent:  "worker",
		Task:   "ship the local-execution marker",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	var completion protocol.CompletionPayload
	found := false
	for _, e := range inner.Events() {
		if e.Type == protocol.EventCompletion {
			if err := e.UnmarshalPayload(&completion); err != nil {
				t.Fatalf("failed to unmarshal completion: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected a completion event, got none")
	}
	if !completion.ExecutedLocally {
		t.Errorf("expected executed_locally=true when wrapped in localExecutionSink, got %+v", completion)
	}
}

func TestGentleShellRunner_WithoutLocalExecutionSinkDoesNotMark(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	passBwrap := writePassBwrap(t)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript("", gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-remote",
		Agent:  "worker",
		Task:   "do not mark executed_locally when unwrapped",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	for _, e := range sink.Events() {
		if e.Type == protocol.EventCompletion {
			var completion protocol.CompletionPayload
			if err := e.UnmarshalPayload(&completion); err != nil {
				t.Fatalf("failed to unmarshal completion: %v", err)
			}
			if completion.ExecutedLocally {
				t.Errorf("expected executed_locally=false without localExecutionSink wrapper, got %+v", completion)
			}
		}
	}
}

func TestGentleShellRunner_DiffIsIncludedWhenWorktreeChanged(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	passBwrap := writePassBwrap(t)

	// Fake gentle-shell that creates a new file in the current directory
	// (the worktree), commits it and emits a brief NDJSON stream. The runner
	// must then pick up the diff against the base ref and include it in
	// the completion result.
	gsBody := "#!/bin/sh\n" +
		"printf '%s\\n' 'added' > added.txt\n" +
		"git -c user.name=test -c user.email=test@example.com add added.txt\n" +
		"git -c user.name=test -c user.email=test@example.com commit -m 'add added.txt' >/dev/null\n" +
		"cat <<'PI_EOF'\n" +
		`{"type":"message_start","message":{"role":"assistant","content":[]}}` + "\n" +
		`{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"done"}}` + "\n" +
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n" +
		"PI_EOF\n" +
		"exit 0\n"
	gsPath := filepath.Join(t.TempDir(), "gs-with-diff")
	if err := os.WriteFile(gsPath, []byte(gsBody), 0o755); err != nil {
		t.Fatalf("failed to write gs-with-diff: %v", err)
	}

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   gsPath,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-with-diff",
		Agent:  "worker",
		Task:   "produce a small diff",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	var completion protocol.CompletionPayload
	for _, e := range sink.Events() {
		if e.Type == protocol.EventCompletion {
			_ = e.UnmarshalPayload(&completion)
		}
	}
	if !strings.Contains(completion.Result, "diff --git") {
		t.Errorf("expected inline diff in result, got %q", completion.Result)
	}
	if !strings.Contains(completion.Result, "added.txt") {
		t.Errorf("expected diff to mention added.txt, got %q", completion.Result)
	}
}

// TestGentleShellRunner_CapturesUsageInCompletion: pi's final stream event
// carries the run's usage (input/output/cacheRead/totalTokens + cost) — the
// F2-3 parallel-vs-serial cost metric. The runner must capture it and expose
// it in the completion result as a parseable "Usage:" line, not discard it.
// Zero-usage interim turns (observed in real pi streams) must not override
// the final total.
func TestGentleShellRunner_CapturesUsageInCompletion(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	passBwrap := writePassBwrap(t)

	gsBody := "#!/bin/sh\n" +
		"printf '%s\\n' 'added' > added.txt\n" +
		"git -c user.name=test -c user.email=test@example.com add added.txt\n" +
		"git -c user.name=test -c user.email=test@example.com commit -m 'add added.txt' >/dev/null\n" +
		"cat <<'PI_EOF'\n" +
		`{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"done"}}` + "\n" +
		// Interim turn: zero usage (observed in real pi streams).
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"zero"}],"usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}}` + "\n" +
		// Final turn: the run total (values from a real pi run).
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":11628,"output":18,"cacheRead":196,"cacheWrite":0,"totalTokens":11842,"cost":{"input":0.0034884,"output":0.0000216,"cacheRead":0.00001176,"cacheWrite":0,"total":0.00352176},"durationMs":2049}}}` + "\n" +
		"PI_EOF\n" +
		"exit 0\n"
	gsPath := filepath.Join(t.TempDir(), "gs-with-usage")
	if err := os.WriteFile(gsPath, []byte(gsBody), 0o755); err != nil {
		t.Fatalf("failed to write gs-with-usage: %v", err)
	}

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         passBwrap,
		GentleShell:   gsPath,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-with-usage",
		Agent:  "worker",
		Task:   "produce a small diff with usage",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	var completion protocol.CompletionPayload
	for _, e := range sink.Events() {
		if e.Type == protocol.EventCompletion {
			_ = e.UnmarshalPayload(&completion)
		}
	}
	const wantUsage = "Usage: input=11628 output=18 cacheRead=196 totalTokens=11842 cost=0.00352176"
	if !strings.Contains(completion.Result, wantUsage) {
		t.Errorf("expected parseable usage line %q in result, got %q", wantUsage, completion.Result)
	}
}

// localExecutionSinkWrapper mirrors pkg/server/runner.localExecutionSink for
// tests; the production type is unexported.
type localExecutionSinkWrapper struct {
	runner.EventSink
}

func (s localExecutionSinkWrapper) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	if eventType == protocol.EventCompletion {
		if completion, ok := payload.(protocol.CompletionPayload); ok {
			completion.ExecutedLocally = true
			payload = completion
		}
	}
	return s.EventSink.EmitEvent(eventType, payload)
}

func containsString(slice []string, want string) bool {
	for _, v := range slice {
		if v == want {
			return true
		}
	}
	return false
}

func indexOf(slice []string, want string) int {
	for i, v := range slice {
		if v == want {
			return i
		}
	}
	return -1
}

// Keep unused imports satisfied.
var _ = sync.Mutex{}
var _ = atomic.AddInt64
var _ = errors.New
var _ = json.Marshal
var _ = io.Reader(nil)
var _ = shellQuote("")

// argvContainsSequence reports whether seq appears as a contiguous run of
// tokens anywhere in argv.
func argvContainsSequence(argv, seq []string) bool {
	if len(seq) == 0 || len(argv) < len(seq) {
		return false
	}
	for i := 0; i+len(seq) <= len(argv); i++ {
		match := true
		for j, s := range seq {
			if argv[i+j] != s {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestGentleShellRunner_HomeIsDerivedFromUserHomeDir pins the portability
// contract: the HOME-derived sandbox mounts (the --tmpfs that hides HOME,
// and the .local / .gentle-shell whitelisted binds) must be derived from
// os.UserHomeDir() and therefore follow the HOME the test sets, not a
// hard-coded /home/rafael. If a future refactor regresses this, the
// forbidden-list assertions below turn red and surface the bug locally
// without needing a second machine to reproduce it.
func TestGentleShellRunner_HomeIsDerivedFromUserHomeDir(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// The runner ro-binds <HOME>/.local and binds <HOME>/.gentle-shell into
	// the sandbox. bwrap fails loudly if the host source path is missing,
	// so the test pre-creates them as empty directories.
	for _, sub := range []string{".local", ".gentle-shell"} {
		if err := os.MkdirAll(filepath.Join(fakeHome, sub), 0o755); err != nil {
			t.Fatalf("failed to create %s under fake HOME: %v", sub, err)
		}
	}

	cache, worktreesRoot := seedBareCache(t)

	// Record the full argv the runner hands to bwrap, one token per line,
	// then run the normal passBwrapScript so the runner sees a clean exit.
	bwrapArgsPath := filepath.Join(t.TempDir(), "bwrap-argv.txt")
	bwrapBody := "printf '%s\\n' \"$@\" > " + shellQuote(bwrapArgsPath) + "\n" + passBwrapScript
	bwrapScript := writeFakeBwrap(t, bwrapBody)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript("", gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         bwrapScript,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		ThinkingLevel: "minimal",
		TotalTimeout:  2 * time.Minute,
		IdleTimeout:   30 * time.Second,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-home-portable",
		Agent:  "worker",
		Task:   "verify home-derived sandbox mounts",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	raw, err := os.ReadFile(bwrapArgsPath)
	if err != nil {
		t.Fatalf("failed to read recorded bwrap argv: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	// 1. No argv token may be a hard-coded /home/rafael path. This is the
	//    regression guard: if someone reintroduces a hard-coded literal,
	//    the test names the exact token that betrayed it.
	forbidden := []string{
		"/home/rafael",
		"/home/rafael/.local",
		"/home/rafael/.gentle-shell",
	}
	for _, bad := range forbidden {
		if containsString(argv, bad) {
			t.Errorf("bwrap argv contains hard-coded path %q (HOME was %q): %v", bad, fakeHome, argv)
		}
	}

	// 2. The --tmpfs that hides HOME must target the test's fake HOME,
	//    not /tmp. The runner also emits a separate `--tmpfs /tmp`, so we
	//    look for a --tmpfs whose argument equals fakeHome specifically.
	homeTmpfsFound := false
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--tmpfs" && argv[i+1] == fakeHome {
			homeTmpfsFound = true
			break
		}
	}
	if !homeTmpfsFound {
		t.Errorf("expected `--tmpfs %s` in bwrap argv, got: %v", fakeHome, argv)
	}

	// 3. The .local and .gentle-shell whitelisted binds must also be
	//    derived from the test's fake HOME, not from /home/rafael.
	wantLocal := []string{
		"--ro-bind",
		filepath.Join(fakeHome, ".local"),
		filepath.Join(fakeHome, ".local"),
	}
	if !argvContainsSequence(argv, wantLocal) {
		t.Errorf("expected %v in bwrap argv, got: %v", wantLocal, argv)
	}

	wantGentleShell := []string{
		"--bind",
		filepath.Join(fakeHome, ".gentle-shell"),
		filepath.Join(fakeHome, ".gentle-shell"),
	}
	if !argvContainsSequence(argv, wantGentleShell) {
		t.Errorf("expected %v in bwrap argv, got: %v", wantGentleShell, argv)
	}
}
