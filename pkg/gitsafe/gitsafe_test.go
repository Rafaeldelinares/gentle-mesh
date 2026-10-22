package gitsafe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo creates a bare repo with one commit on main.
func seedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cache := filepath.Join(root, "cache.git")
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatalf("mkdir seed: %v", err)
	}

	for _, args := range [][]string{
		{"init", "-b", "main", "--bare", cache},
		{"init", "-b", "main", seed},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = seed
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("contenido\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = seed
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "f.txt")
	run("commit", "-m", "init")
	run("remote", "add", "origin", cache)
	run("push", "-u", "origin", "main")
	return cache
}

func gitConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

func TestDriverName(t *testing.T) {
	cases := map[string]struct {
		key  string
		want string
		ok   bool
	}{
		"simple":           {"filter.x.smudge", "x", true},
		"dots in name":     {"filter.my.driver.smudge", "my.driver", true},
		"clean":            {"filter.x.clean", "x", true},
		"process":          {"filter.x.process", "x", true},
		"unknown attr":     {"filter.x.required", "", false},
		"not a filter":     {"core.pager", "", false},
		"no attr":          {"filter.x", "", false},
		"empty name":       {"filter..smudge", "", false},
		"trailing dot":     {"filter.x.", "", false},
		"prefix lookalike": {"xfilter.x.smudge", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := driverName(tc.key)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("driverName(%q) = (%q, %v), want (%q, %v)", tc.key, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFilterDriversEnumeratesConfiguredOnes(t *testing.T) {
	cache := seedRepo(t)

	drivers, err := filterDrivers(cache)
	if err != nil {
		t.Fatalf("filterDrivers on a clean repo: %v", err)
	}
	if len(drivers) != 0 {
		t.Fatalf("expected no drivers, got %v", drivers)
	}

	gitConfig(t, cache, "filter.x.smudge", "cat")
	gitConfig(t, cache, "filter.my.driver.clean", "cat")
	gitConfig(t, cache, "core.pager", "cat") // not a filter: must be ignored

	drivers, err = filterDrivers(cache)
	if err != nil {
		t.Fatalf("filterDrivers: %v", err)
	}
	if len(drivers) != 2 {
		t.Fatalf("expected 2 drivers, got %v", drivers)
	}
	if drivers[0] != "x" || drivers[1] != "my.driver" {
		t.Fatalf("unexpected drivers: %v", drivers)
	}
}

// The neutralization must travel in the environment, not in -c: git splits -c at
// the first equals sign, so a driver named "a=b" is inexpressible there.
func TestPlanNeutralizesEveryDiscoveredDriverInEnv(t *testing.T) {
	cache := seedRepo(t)
	gitConfig(t, cache, "filter.x.smudge", "payload")
	gitConfig(t, cache, "filter.x.required", "true")
	gitConfig(t, cache, "filter.zz.clean", "payload")
	gitConfig(t, cache, "filter.a=b.smudge", "payload")

	argv, env, err := Plan(cache, "worktree", "add", "-b", "b", "/tmp/x", "main")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "filter.") {
		t.Errorf("argv must not carry filter overrides (they would split on '='): %v", argv)
	}

	joined := strings.Join(env, " ")
	for _, want := range []string{
		"=filter.x.clean", "=filter.x.smudge", "=filter.x.process", "=filter.x.required",
		"=filter.zz.clean",
		// The '='-named driver is the whole point: it is only addressable here.
		"=filter.a=b.smudge", "=filter.a=b.clean", "=filter.a=b.required",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing a GIT_CONFIG_KEY with %q\n%s", want, joined)
		}
	}
	// Count must match the number of pairs, or git ignores the tail.
	count := 0
	values := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_CONFIG_KEY_") {
			count++
		}
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") {
			values++
		}
	}
	if count != values {
		t.Fatalf("key/value pair mismatch: %d keys, %d values", count, values)
	}
	if !strings.Contains(joined, fmt.Sprintf("GIT_CONFIG_COUNT=%d", count)) {
		t.Errorf("GIT_CONFIG_COUNT does not match the %d pairs: %s", count, joined)
	}
	// The required flags must be relaxed to false, everything else empty.
	if !strings.Contains(joined, "GIT_CONFIG_VALUE_3=false") {
		t.Errorf("required must be relaxed: %s", joined)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") && !strings.HasSuffix(kv, "=false") && !strings.HasSuffix(kv, "=") {
			t.Errorf("unexpected non-empty neutralization value: %q", kv)
		}
	}
}

// The fixed keys travel in argv and must survive for repos with no drivers.
func TestPlanAlwaysCarriesFixedOverridesInArgv(t *testing.T) {
	cache := seedRepo(t)
	argv, _, err := Plan(cache, "status", "--porcelain")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-c core.hooksPath=", "-c core.fsmonitor=false", "-c core.pager=cat",
		"-c diff.external=", "-c credential.helper=", "-c core.gitProxy=",
		"-c protocol.file.allow=never", "--no-optional-locks",
		"-C " + cache,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q", want)
		}
	}
}

func TestCheckDiffArgsRefusesBareDiff(t *testing.T) {
	if err := checkDiffArgs([]string{"diff", "main..HEAD"}); err == nil {
		t.Fatal("a bare diff must be refused")
	}
	if err := checkDiffArgs([]string{"diff", "--no-ext-diff", "main..HEAD"}); err == nil {
		t.Fatal("diff with only --no-ext-diff must be refused")
	}
	if err := checkDiffArgs([]string{"diff", "--no-ext-diff", "--no-textconv", "main..HEAD"}); err != nil {
		t.Fatalf("fully guarded diff must be accepted: %v", err)
	}
	if err := checkDiffArgs([]string{"status", "--porcelain"}); err != nil {
		t.Fatalf("non-diff invocations must pass through: %v", err)
	}
}

// End-to-end proof at the package level: a poisoned driver defined entirely in
// the repository config must not run, and the checkout must still be correct.
func TestCommand_NeutralizesPoisonedFilterDriver(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	marker := filepath.Join(root, "marker")
	payload := filepath.Join(cache, "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho pwned >> "+marker+"\ncat\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "info"), 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "info", "attributes"), []byte("* filter=x\n"), 0o644); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	gitConfig(t, cache, "filter.x.clean", "cat")
	gitConfig(t, cache, "filter.x.smudge", payload)
	gitConfig(t, cache, "filter.x.required", "true")

	wt := filepath.Join(root, "wt")
	cmd, err := Command(context.Background(), cache, "worktree", "add", "-b", "prop", wt, "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened worktree add failed: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ESCAPE: the repository-defined smudge filter ran")
	}
	got, err := os.ReadFile(filepath.Join(wt, "f.txt"))
	if err != nil {
		t.Fatalf("read checked out file: %v", err)
	}
	if string(got) != "contenido\n" {
		t.Fatalf("neutralization changed the checkout content: %q", got)
	}
}

// Hardening must not change what the runner reads: diff output has to match
// plain git byte for byte, otherwise the proposal the operator reviews is not
// the proposal the agent produced.
func TestCommand_DiffOutputMatchesPlainGit(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	seed := filepath.Join(root, "seed2")

	// Clone instead of init+push: the cache already carries main from seedRepo,
	// so a fresh history would be rejected as non-fast-forward.
	if out, err := exec.Command("git", "clone", cache, seed).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("f.txt", "contenido\n")
	write("otro.txt", "otro\n")
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return string(out)
	}
	run(seed, "add", ".")
	run(seed, "commit", "-m", "segundo")
	run(seed, "push", "-u", "origin", "main")

	wt := filepath.Join(root, "wt-diff")
	cmd, err := Command(context.Background(), cache, "worktree", "add", "-b", "prop", wt, "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("contenido cambiado\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}
	run(wt, "add", ".")
	run(wt, "commit", "-m", "cambio")

	plain := run(wt, "diff", "--no-ext-diff", "--no-textconv", "main..HEAD")
	safe, err := Command(context.Background(), wt, "diff", "--no-ext-diff", "--no-textconv", "main..HEAD")
	if err != nil {
		t.Fatalf("Command diff: %v", err)
	}
	var buf bytes.Buffer
	safe.Stdout = &buf
	safe.Stderr = &buf
	if err := safe.Run(); err != nil {
		t.Fatalf("hardened diff failed: %v\n%s", err, buf.String())
	}
	if buf.String() != plain {
		t.Fatalf("hardened diff differs from plain git\nplain: %q\nsafe:  %q", plain, buf.String())
	}
}
