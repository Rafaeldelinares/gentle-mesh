package runner_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/runner"
)

// TestGentleShellRunner_HostSideGitIgnoresSandboxWrittenFilterDriver is the
// regression test for the bypass that survived the first round of hardening.
//
// Filter drivers are a dynamic git config key: filter.<name>.smudge. No blanket
// -c flag can disable them because the name is chosen by whoever writes the
// config, and both halves of the attack live in the sandbox-writable cache:
// info/attributes ("* filter=x", highest gitattributes precedence) plus the
// driver definition in the cache config. git worktree add performs a checkout,
// so it runs the smudge command on the host. Verified against the previous
// hardening: every -c override was present and the payload still ran.
func TestGentleShellRunner_HostSideGitIgnoresSandboxWrittenFilterDriver(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "pwned")
	// exit 0 and passthrough on stdout: a real smudge filter forwards the blob
	// unchanged, so the only difference from a clean checkout is the side effect.
	payload := filepath.Join(cache, "smudge-payload.sh")
	script := "#!/bin/sh\necho pwned >> " + shellQuote(marker) + "\ncat\nexit 0\n"
	if err := os.WriteFile(payload, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}

	// Everything below is what an agent can do from inside the sandbox, because
	// the cache is bound read-write there by design.
	if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter=x\n"), 0o644); err != nil {
		t.Fatalf("failed to write info/attributes: %v", err)
	}
	for _, kv := range [][2]string{
		// A working filter defines both sides: clean=cat keeps the agent's own
		// git add/commit functioning inside the sandbox, smudge is the payload
		// that fires on the host's checkout.
		{"filter.x.clean", "cat"},
		{"filter.x.smudge", payload},
		{"filter.x.required", "true"},
	} {
		cmd := exec.Command("git", "-C", cache, "config", kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to set %s: %v\n%s", kv[0], err, out)
		}
	}

	// Teeth: unhardened git DOES execute it, so a green result below cannot be
	// explained by git ignoring smudge drivers on this platform.
	sanityMarker := filepath.Join(markerDir, "unhardened")
	sanityPayload := filepath.Join(cache, "smudge-sanity.sh")
	if err := os.WriteFile(sanityPayload, []byte("#!/bin/sh\necho pwned >> "+shellQuote(sanityMarker)+"\ncat\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to write sanity payload: %v", err)
	}
	if out, err := exec.Command("git", "-C", cache, "config", "filter.x.smudge", sanityPayload).CombinedOutput(); err != nil {
		t.Fatalf("failed to repoint driver: %v\n%s", err, out)
	}
	sanityWT := filepath.Join(markerDir, "sanity-wt")
	_ = exec.Command("git", "-C", cache, "worktree", "add", "-f", sanityWT, "-b", "sanity", "main").Run()
	if _, err := os.Stat(sanityMarker); err != nil {
		t.Skipf("git on this platform does not run smudge filters from info/attributes; cannot prove the bypass: %v", err)
	}
	// Restore the payload the runner must survive.
	if out, err := exec.Command("git", "-C", cache, "config", "filter.x.smudge", payload).CombinedOutput(); err != nil {
		t.Fatalf("failed to restore driver: %v\n%s", err, out)
	}

	bwrapScript := writeFakeBwrap(t, sandboxedGitPrefix+passBwrapScript)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(
		filepath.Join(t.TempDir(), "gs-argv.txt"), gentleShellHappyStream, "", 0))

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
		TaskID: "task-filter",
		Agent:  "worker",
		Task:   "worktree add must not run a sandbox-defined smudge filter",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ESCAPE: the sandbox-defined smudge filter ran on the host %d time(s): %s",
			bytes.Count(data, []byte("pwned")), strings.TrimSpace(string(data)))
	}
}

// TestGentleShellRunner_HostSideGitIgnoresSandboxWrittenConditionalDriver is the
// regression test for the enumeration/execution mismatch, exercised through the
// runner's real path (Run -> prepareWorktree) rather than through a hand-written
// sequence.
//
// All three halves live in the read-write cache: the driver, the info/attributes
// that select it, and an includeIf that makes it conditional on the current
// branch. The condition `onbranch:proposal/*` is inactive while the cache's HEAD
// is `main`, so enumerating against the cache finds nothing — but the runner
// creates the worktree with `-b proposal/<task_id>`, which moves HEAD to the new
// branch before checking out, at which point the include is live and the smudge
// runs on the host. `proposal/<task_id>` is exactly the branch shape the runner
// uses, so a wildcard match is fully general.
func TestGentleShellRunner_HostSideGitIgnoresSandboxWrittenConditionalDriver(t *testing.T) {
	cache, worktreesRoot := seedBareCache(t)

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "pwned")
	payload := filepath.Join(cache, "smudge-payload.sh")
	script := "#!/bin/sh\necho \"HOST-EXEC $(id -un)@$(hostname)\" >> " + shellQuote(marker) + "\ncat\nexit 0\n"
	if err := os.WriteFile(payload, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	evil := filepath.Join(cache, "evil.cfg")
	evilBody := "[filter \"pwn\"]\n\tsmudge = " + payload + "\n\trequired = true\n"
	if err := os.WriteFile(evil, []byte(evilBody), 0o644); err != nil {
		t.Fatalf("failed to write evil.cfg: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "info"), 0o755); err != nil {
		t.Fatalf("failed to create info dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter=pwn\n"), 0o644); err != nil {
		t.Fatalf("failed to write info/attributes: %v", err)
	}
	cfgFile := filepath.Join(cache, "config")
	cfg, err := os.OpenFile(cfgFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("failed to open cache config: %v", err)
	}
	if _, err := cfg.WriteString("\n[includeIf \"onbranch:proposal/*\"]\n\tpath = " + evil + "\n"); err != nil {
		t.Fatalf("failed to append includeIf: %v", err)
	}
	cfg.Close()

	// Teeth: plain git on the same cache runs it, so a pass below means something.
	if out, err := exec.Command("git", "-C", cache, "worktree", "add", "-f",
		filepath.Join(markerDir, "teeth"), "-b", "proposal/teeth", "main").CombinedOutput(); err != nil {
		t.Fatalf("teeth worktree add failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Skipf("git does not activate the conditional driver here; cannot prove the escape: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("failed to clear marker: %v", err)
	}

	bwrapScript := writeFakeBwrap(t, sandboxedGitPrefix+passBwrapScript)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(
		filepath.Join(t.TempDir(), "gs-argv.txt"), gentleShellHappyStream, "", 0))

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
	_ = r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-conditional",
		Agent:  "worker",
		Task:   "conditional filter driver must not reach the host",
	}, sink)

	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ESCAPE: the conditional smudge driver ran on the host: %s", strings.TrimSpace(string(data)))
	}
}
