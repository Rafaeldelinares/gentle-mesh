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

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
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
