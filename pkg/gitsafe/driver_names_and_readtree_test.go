package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func dnGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dnMust(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := dnGit(t, dir, args...); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// dnCache builds a bare cache whose main holds README.md.
func dnCache(t *testing.T) (cache, root, marker, payload string) {
	t.Helper()
	root = t.TempDir()
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	dnMust(t, seed, "init", "-q", "-b", "main")
	dnMust(t, seed, "config", "user.email", "dn@example.com")
	dnMust(t, seed, "config", "user.name", "dn")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dnMust(t, seed, "add", "-A")
	dnMust(t, seed, "commit", "-qm", "base")
	cache = filepath.Join(root, "cache.git")
	dnMust(t, root, "clone", "-q", "--bare", seed, cache)
	marker = filepath.Join(root, "marker")
	payload = filepath.Join(root, "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nprintf 'HOST-EXEC\\n' >> "+marker+"\ncat\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cache, root, marker, payload
}

func dnExecs(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "HOST-EXEC")
}

// dnTwoPhase is exactly what prepareWorktree runs.
func dnTwoPhase(t *testing.T, cache, wt, branch string) error {
	t.Helper()
	argv, env, err := Plan(cache, "worktree", "add", "--no-checkout", "-b", branch, wt, "main")
	if err != nil {
		return err
	}
	add := exec.CommandContext(context.Background(), "git", argv...)
	add.Dir = cache
	add.Env = append(Sanitize(os.Environ()), env...)
	if out, err := add.CombinedOutput(); err != nil {
		return &dnErr{msg: "add: " + string(out)}
	}
	argv, env, err = Plan(wt, "read-tree", "-m", "-u", "HEAD")
	if err != nil {
		return err
	}
	pop := exec.CommandContext(context.Background(), "git", argv...)
	pop.Dir = wt
	pop.Env = append(Sanitize(os.Environ()), env...)
	if out, err := pop.CombinedOutput(); err != nil {
		return &dnErr{msg: "read-tree: " + string(out)}
	}
	return nil
}

type dnErr struct{ msg string }

func (e *dnErr) Error() string { return e.msg }

// TestSecurityFilterDrivers_UnaddressableNamesAreNeutralized covers the name
// space of filter.<name>: every name git accepts from a config file and can
// select from gitattributes must be enumerated and neutralized (the `=` case is
// the one that made `-c` overrides insufficient; the others guard the parser).
func TestSecurityFilterDrivers_UnaddressableNamesAreNeutralized(t *testing.T) {
	cases := []struct{ label, cfgName, attr string }{
		{"plain", "pwn", "pwn"},
		{"equals", "a=b", "a=b"},
		{"quote", `a\"b`, `a"b`},
		{"backslash", `a\\b`, `a\b`},
		{"trailing dot", "a.", "a."},
		{"leading dot", ".a", ".a"},
		{"inner dot", "a.b", "a.b"},
		{"colon", "a:b", "a:b"},
		{"hash", "a#b", "a#b"},
		{"star", "a*b", "a*b"},
		{"dollar", "a$b", "a$b"},
		{"uppercase", "PWN", "PWN"},
		{"keyword", "smudge", "smudge"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cache, root, marker, payload := dnCache(t)
			evil := filepath.Join(cache, "evil.cfg")
			if err := os.WriteFile(evil, []byte("[filter \""+tc.cfgName+"\"]\n\tsmudge = "+payload+"\n\trequired = true\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(cache, "info"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter="+tc.attr+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(filepath.Join(cache, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("\n[include]\n\tpath = " + evil + "\n"); err != nil {
				t.Fatal(err)
			}
			f.Close()
			drivers, derr := filterDrivers(cache)
			if derr != nil || len(drivers) == 0 {
				t.Fatalf("enumeration missed the driver (drivers=%v err=%v): a fail-open list restores the escape", drivers, derr)
			}

			teethWT := filepath.Join(root, "teeth")
			_, _ = dnGit(t, cache, "worktree", "add", "-f", "-b", "proposal/teeth", teethWT, "main")
			teeth := dnExecs(t, marker) > 0
			_ = os.Remove(marker)
			_, _ = dnGit(t, cache, "worktree", "remove", "--force", teethWT)
			_, _ = dnGit(t, cache, "branch", "-D", "proposal/teeth")
			if !teeth {
				t.Fatalf("no teeth: plain git did not run driver %q", tc.cfgName)
			}

			wt := filepath.Join(root, "wt")
			perr := dnTwoPhase(t, cache, wt, "proposal/task")
			if n := dnExecs(t, marker); n > 0 {
				t.Fatalf("ESCAPE: driver %q ran on the host %d time(s) (populateErr=%v)", tc.cfgName, n, perr)
			}
		})
	}
}

// TestSecurityFilterDrivers_ReadTreeFsmonitorIsNeutralized pins that the phase
// that materializes files has its own config-driven program path (read-tree
// invokes core.fsmonitor) and that the fixed overrides cover it, since the
// driver enumeration never sees it.
func TestSecurityFilterDrivers_ReadTreeFsmonitorIsNeutralized(t *testing.T) {
	cache, root, marker, _ := dnCache(t)
	fsm := filepath.Join(root, "fsmonitor.sh")
	if err := os.WriteFile(fsm, []byte("#!/bin/sh\nprintf 'HOST-EXEC\\n' >> "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dnMust(t, cache, "config", "core.fsmonitor", fsm)

	teethWT := filepath.Join(root, "teeth")
	dnMust(t, cache, "worktree", "add", "-q", "--no-checkout", "-b", "proposal/teeth", teethWT, "main")
	_, _ = dnGit(t, teethWT, "read-tree", "-m", "-u", "HEAD")
	teeth := dnExecs(t, marker) > 0
	_ = os.Remove(marker)
	_, _ = dnGit(t, cache, "worktree", "remove", "--force", teethWT)
	_, _ = dnGit(t, cache, "branch", "-D", "proposal/teeth")
	if !teeth {
		t.Skip("this git build does not invoke core.fsmonitor from read-tree")
	}
	if err := dnTwoPhase(t, cache, filepath.Join(root, "wt"), "proposal/task"); err != nil {
		t.Fatalf("hardened two-phase: %v", err)
	}
	if n := dnExecs(t, marker); n > 0 {
		t.Fatalf("ESCAPE: core.fsmonitor ran %d time(s) during read-tree", n)
	}
}

// TestSecurityFilterDrivers_TOCTOUWindow documents the residual window: the
// enumeration and the materialization are two separate git processes, so a
// config source written in between is not neutralized. It is deterministic
// proof of the gap, not a single-task attack: it needs a concurrent writer with
// cache write access (a sibling sandboxed task).
func TestSecurityFilterDrivers_TOCTOUWindow(t *testing.T) {
	cache, root, marker, payload := dnCache(t)
	if err := os.MkdirAll(filepath.Join(cache, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dnMust(t, cache, "config", "extensions.worktreeConfig", "true")
	dnMust(t, cache, "config", "core.repositoryFormatVersion", "1")

	wt := filepath.Join(root, "wt")
	argv, env, err := Plan(cache, "worktree", "add", "--no-checkout", "-b", "proposal/task", wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", argv...)
	add.Dir = cache
	add.Env = append(Sanitize(os.Environ()), env...)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	// Enumeration (inside Plan) has already run; write the driver now.
	argv, env, err = Plan(wt, "read-tree", "-m", "-u", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	adminDir := filepath.Join(cache, "worktrees", "wt")
	if _, statErr := os.Stat(adminDir); statErr != nil {
		t.Skipf("unexpected worktree admin layout: %v", statErr)
	}
	body := "[core]\n\tbare = false\n[filter \"pwn\"]\n\tsmudge = " + payload + "\n\trequired = true\n"
	if err := os.WriteFile(filepath.Join(adminDir, "config.worktree"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pop := exec.Command("git", argv...)
	pop.Dir = wt
	pop.Env = append(Sanitize(os.Environ()), env...)
	_, _ = pop.CombinedOutput()
	n := dnExecs(t, marker)
	if n == 0 {
		t.Skip("config.worktree written after enumeration did not execute on this git build")
	}
	t.Logf("residual TOCTOU window confirmed: %d host execution(s); enumeration and materialization must share one snapshot to close it", n)
}
