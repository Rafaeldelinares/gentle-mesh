package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func ifxGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// ifxBareCache builds a bare cache whose HEAD is the trusted branch and whose
// committed tree contains no attributes and no filter driver.
func ifxBareCache(t *testing.T) (cache, root, marker string) {
	t.Helper()
	root = t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	ifxGit(t, seed, "init", "-q", "-b", "main")
	ifxGit(t, seed, "config", "user.email", "s@example.com")
	ifxGit(t, seed, "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "payload.txt"), []byte("TRUSTED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ifxGit(t, seed, "add", "-A")
	ifxGit(t, seed, "commit", "-qm", "base")
	cache = filepath.Join(root, "cache.git")
	ifxGit(t, root, "clone", "-q", "--bare", seed, cache)
	return cache, root, filepath.Join(root, "HOSTMARKER")
}

func ifxAppendConfig(t *testing.T, cache, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(cache, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func ifxPayload(t *testing.T, path, marker string) {
	t.Helper()
	body := "#!/bin/sh\necho \"HOST-EXEC $(id -un)@$(hostname)\" >> " + marker + "\ncat\nexit 0\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSecurityFilterDriver_IncludeIfOnBranchDoesNotExecute pins the MECHANISM,
// at the package level: enumerating inside the target worktree (where HEAD is
// already the branch the includeIf selects) is what makes the driver visible to
// the enumeration, and neutralizing from there stops it.
//
// The regression test for the production path is
// TestGentleShellRunner_HostSideGitIgnoresSandboxWrittenConditionalDriver in the
// runner package, which goes through Run -> prepareWorktree. This one would pass
// even if prepareWorktree did not do the two-phase sequence, which is why it is
// not the one that failed first.
func TestSecurityFilterDriver_IncludeIfOnBranchDoesNotExecute(t *testing.T) {
	cache, root, marker := ifxBareCache(t)

	pwn := filepath.Join(root, "pwn.sh")
	ifxPayload(t, pwn, marker)
	evil := filepath.Join(cache, "evil.cfg")
	if err := os.WriteFile(evil, []byte("[filter \"pwn\"]\n\tsmudge = "+pwn+"\n\trequired = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ifxAppendConfig(t, cache, "\n[includeIf \"onbranch:proposal/*\"]\n\tpath = "+evil+"\n")

	// Teeth: plain git does execute it, so a pass below means something.
	ifxGit(t, cache, "worktree", "add", "-q", "-b", "proposal/teeth", filepath.Join(root, "teeth"), "main")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("teeth: plain git did not run the driver, the test would prove nothing: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(root, "wt")
	argv, env, err := Plan(cache, "worktree", "add", "--no-checkout", "-b", "proposal/task-1", wt, "main")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	add := exec.CommandContext(context.Background(), "git", argv...)
	add.Dir = cache
	add.Env = append(os.Environ(), env...)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("hardened worktree add: %v\n%s", err, out)
	}

	// The second phase runs where git will actually read the config: inside the
	// new worktree, whose HEAD is now the branch the includeIf selects.
	argv, env, err = Plan(wt, "read-tree", "-m", "-u", "HEAD")
	if err != nil {
		t.Fatalf("Plan for the checkout phase: %v", err)
	}
	populate := exec.CommandContext(context.Background(), "git", argv...)
	populate.Dir = wt
	populate.Env = append(os.Environ(), env...)
	if out, err := populate.CombinedOutput(); err != nil {
		t.Fatalf("hardened checkout phase: %v\n%s", err, out)
	}

	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ESCAPE: the driver named by includeIf onbranch ran on the host: %s", strings.TrimSpace(string(data)))
	}
	content, err := os.ReadFile(filepath.Join(wt, "payload.txt"))
	if err != nil {
		t.Fatalf("the worktree must still be populated: %v", err)
	}
	if strings.TrimSpace(string(content)) != "TRUSTED" {
		t.Fatalf("checkout content = %q, want TRUSTED", content)
	}
}
