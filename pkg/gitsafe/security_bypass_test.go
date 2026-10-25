package gitsafe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// payloadWithSideEffect writes a script that records that it ran and then
// behaves like `cat`, so a poisoned driver still produces a correct checkout and
// the only observable difference is the marker.
func payloadWithSideEffect(t *testing.T, dir, tag string) (payload, marker string) {
	t.Helper()
	marker = filepath.Join(dir, "marker-"+tag)
	payload = filepath.Join(dir, "payload-"+tag+".sh")
	body := "#!/bin/sh\necho pwned >> " + marker + "\ncat\nexit 0\n"
	if err := os.WriteFile(payload, []byte(body), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return payload, marker
}

// selectFilterForEveryPath writes the highest-precedence gitattributes, so every
// blob in the repository is routed through the driver.
func selectFilterForEveryPath(t *testing.T, repo, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, "info"), 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "info", "attributes"), []byte("* filter="+value+"\n"), 0o644); err != nil {
		t.Fatalf("write info/attributes: %v", err)
	}
}

func runPlain(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	return b.String(), err
}

// A driver name containing '=' is a valid git subsection and a valid
// gitattributes value, and it defeats -c based neutralization: git splits -c at
// the FIRST equals sign, so `-c filter.a=b.smudge=` sets the key `filter.a`
// instead of `filter.a=b.smudge`. The neutralization therefore travels in
// GIT_CONFIG_KEY_n entries, which are not split and outrank the repo config.
//
// Reproduced against the -c implementation: enumeration found "a=b" and the
// payload still ran.
func TestSecurityFilterDriver_NameWithEqualsDoesNotExecuteOnCheckout(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	payload, marker := payloadWithSideEffect(t, root, "eq")
	selectFilterForEveryPath(t, cache, "a=b")
	gitConfig(t, cache, "filter.a=b.smudge", payload)
	gitConfig(t, cache, "filter.a=b.clean", "cat")
	gitConfig(t, cache, "filter.a=b.required", "true")

	// Teeth: plain git does execute it, so a pass below is meaningful.
	teethMarker := filepath.Join(root, "marker-teeth")
	teethPayload := filepath.Join(root, "payload-teeth.sh")
	if err := os.WriteFile(teethPayload, []byte("#!/bin/sh\necho pwned >> "+teethMarker+"\ncat\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write teeth payload: %v", err)
	}
	gitConfig(t, cache, "filter.a=b.smudge", teethPayload)
	if out, err := runPlain(t, cache, "worktree", "add", "-f", "-b", "teeth", filepath.Join(root, "wt-teeth"), "main"); err != nil {
		t.Skipf("plain git worktree add failed here, cannot establish teeth: %v\n%s", err, out)
	}
	if _, err := os.Stat(teethMarker); err != nil {
		t.Skipf("git does not run a driver whose name contains '=' on this platform: %v", err)
	}
	gitConfig(t, cache, "filter.a=b.smudge", payload)
	_ = os.Remove(marker)

	cmd, err := Command(context.Background(), cache, "worktree", "add", "-b", "eq", filepath.Join(root, "wt-eq"), "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened worktree add failed: %v\n%s", err, b.String())
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ESCAPE: '='-named driver ran on the host (%d time(s)) during hardened worktree add",
			bytes.Count(data, []byte("pwned")))
	}
}

// The same driver reached from the clean side, which is the path the
// Evaluator, pkg/shell and the integration agent use (`git status`).
func TestSecurityFilterDriver_NameWithEqualsDoesNotExecuteOnStatus(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	payload, marker := payloadWithSideEffect(t, root, "clean")
	selectFilterForEveryPath(t, cache, "a=b")
	gitConfig(t, cache, "filter.a=b.clean", payload)
	gitConfig(t, cache, "filter.a=b.smudge", "cat")
	gitConfig(t, cache, "filter.a=b.required", "true")

	wt := filepath.Join(root, "wt")
	add, err := Command(context.Background(), cache, "worktree", "add", "-b", "st", wt, "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var b bytes.Buffer
	add.Stdout = &b
	add.Stderr = &b
	if err := add.Run(); err != nil {
		t.Fatalf("hardened worktree add failed: %v\n%s", err, b.String())
	}
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("CONTENIDO\n"), 0o644); err != nil {
		t.Fatalf("edit worktree file: %v", err)
	}
	_ = os.Remove(marker)

	status, err := Command(context.Background(), wt, "status", "--porcelain")
	if err != nil {
		t.Fatalf("Command status: %v", err)
	}
	var sb bytes.Buffer
	status.Stdout = &sb
	status.Stderr = &sb
	if err := status.Run(); err != nil {
		t.Fatalf("hardened status failed: %v\n%s", err, sb.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ESCAPE: '='-named clean driver ran on the host during hardened git status")
	}
	if !strings.Contains(sb.String(), "f.txt") {
		t.Fatalf("status must still report the edit, got %q", sb.String())
	}
}

// A whitespace-named driver used to be dropped by the enumeration, because
// --get-regexp prints "key value" and the key was cut at the first space. That
// is a fail-open parse: the driver stayed live and unenumerated. gitattributes
// cannot select such a name today, so this pins the enumeration, not an
// exploitable path.
func TestSecurityFilterDrivers_EnumeratesWhitespaceNamedDriver(t *testing.T) {
	cache := seedRepo(t)
	gitConfig(t, cache, "filter.a b.smudge", "cat")

	drivers, err := filterDrivers(cache)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(drivers) != 1 || drivers[0] != "a b" {
		t.Fatalf("whitespace-named driver was dropped from the enumeration: %q", drivers)
	}

	_, env, err := Plan(cache, "status", "--porcelain")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !strings.Contains(strings.Join(env, " "), "=filter.a b.smudge") {
		t.Fatalf("whitespace-named driver was not neutralized: %v", env)
	}
}

// A driver that only exists in an included config file is still the
// repository's driver.
func TestSecurityCommand_NeutralizesDriverFromIncludedConfig(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	payload, marker := payloadWithSideEffect(t, root, "inc")
	extra := filepath.Join(root, "extra.cfg")
	cfg := "[filter \"inc\"]\n\tsmudge = " + payload + "\n\tclean = cat\n\trequired = true\n"
	if err := os.WriteFile(extra, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write include: %v", err)
	}
	gitConfig(t, cache, "include.path", extra)
	selectFilterForEveryPath(t, cache, "inc")

	cmd, err := Command(context.Background(), cache, "worktree", "add", "-b", "inc", filepath.Join(root, "wt"), "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened worktree add failed: %v\n%s", err, b.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ESCAPE: driver defined only in an included config ran during hardened worktree add")
	}
}

// Enumeration must look at the repository the command will touch, not at
// whatever GIT_DIR the caller's environment points at.
func TestSecurityFilterDrivers_IgnoresHostGitDir(t *testing.T) {
	cache := seedRepo(t)
	other := seedRepo(t)
	gitConfig(t, other, "filter.elsewhere.smudge", "cat")
	gitConfig(t, cache, "filter.here.smudge", "cat")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	drivers, err := filterDrivers(cache)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(drivers) != 1 || drivers[0] != "here" {
		t.Fatalf("enumeration followed the host GIT_DIR instead of the target repo: %q", drivers)
	}
}

// Hardening must not change what the operator reviews.
func TestSecurityCommand_ParityWithPlainGit(t *testing.T) {
	cache := seedRepo(t)
	root := filepath.Dir(cache)
	plain, safe := filepath.Join(root, "wtA"), filepath.Join(root, "wtB")

	if out, err := runPlain(t, cache, "worktree", "add", "-b", "a", plain, "main"); err != nil {
		t.Fatalf("plain add: %v\n%s", err, out)
	}
	add, err := Command(context.Background(), cache, "worktree", "add", "-b", "b", safe, "main")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var ab bytes.Buffer
	add.Stdout = &ab
	add.Stderr = &ab
	if err := add.Run(); err != nil {
		t.Fatalf("safe add: %v\n%s", err, ab.String())
	}

	norm := func(s, dir string) string { return strings.ReplaceAll(s, dir, "WTPATH") }
	plainGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("plain git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	safeGit := func(t *testing.T, dir string, args ...string) (string, error) {
		t.Helper()
		cmd, err := Command(context.Background(), dir, args...)
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		cmd.Stdout = &b
		cmd.Stderr = &b
		err = cmd.Run()
		return b.String(), err
	}

	for _, args := range [][]string{
		{"rev-parse", "--show-toplevel"},
		{"rev-parse", "HEAD"},
		{"status", "--porcelain"},
		{"diff", "--no-ext-diff", "--no-textconv"},
	} {
		want := plainGit(plain, args...)
		got, err := safeGit(t, safe, args...)
		if err != nil {
			t.Fatalf("hardened %v failed: %v", args, err)
		}
		if norm(want, plain) != norm(got, safe) {
			t.Fatalf("%v differs from plain git\nplain: %q\nsafe:  %q", args, want, got)
		}
	}
}

// A directory that is not a repository must still fail with git's own error.
func TestSecurityCommand_NonGitDirKeepsGitError(t *testing.T) {
	dir := t.TempDir()
	plainOut, plainErr := runPlain(t, dir, "status", "--porcelain")
	if plainErr == nil {
		t.Fatal("expected plain git to fail on a non-repository")
	}

	cmd, err := Command(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if err := cmd.Run(); err == nil {
		t.Fatal("expected hardened git to fail on a non-repository")
	}
	if plainOut != b.String() {
		t.Fatalf("error output changed\nplain: %q\nsafe:  %q", plainOut, b.String())
	}
}
