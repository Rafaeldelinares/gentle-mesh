package settlement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// evalPath runs one file-touching assertion against an evaluator rooted at root
// and returns the result.
func evalPath(t *testing.T, root string, kind AssertionType, params map[string]any) *AssertionResult {
	t.Helper()
	return NewEvaluator(root).Evaluate(context.Background(), &Assertion{
		ID:     "a1",
		Type:   kind,
		Params: params,
	}, 0)
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// F2-01: absPath returned absolute paths untouched and joined relative ones
// without verifying containment, so an assertion could stat or read any file
// the worker user can read. Containment must be real: absolute paths refused,
// ".." refused, and symlinks resolved before the check so a link inside the
// workspace cannot point out of it.
func TestAbsPath_RejectsAbsolutePathOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret"), 0o644); err != nil {
		t.Fatalf("failed to write secret: %v", err)
	}

	for _, kind := range []AssertionType{AssertionFileModified, AssertionFileHashEquals} {
		params := map[string]any{"path": secret, "expected_hash": sha256Of(t, secret)}
		got := evalPath(t, root, kind, params)

		if got.Result != ResultError {
			t.Fatalf("%s: an absolute path outside the workspace must be refused, got %s (%s)",
				kind, got.Result, got.Message)
		}
		// The refusal must happen before any read: no evidence may be produced.
		if got.Evidence.FileExists || got.Evidence.ActualHash != "" {
			t.Fatalf("%s: refused path still produced evidence: %+v", kind, got.Evidence)
		}
	}
}

func TestAbsPath_RejectsDotDotTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("host secret"), 0o644); err != nil {
		t.Fatalf("failed to write secret: %v", err)
	}

	rel, err := filepath.Rel(root, target)
	if err != nil {
		t.Fatalf("failed to build relative path: %v", err)
	}
	got := evalPath(t, root, AssertionFileHashEquals, map[string]any{
		"path":          rel,
		"expected_hash": sha256Of(t, target),
	})

	if got.Result != ResultError {
		t.Fatalf("a %q traversal must be refused, got %s (%s)", rel, got.Result, got.Message)
	}
	if got.Evidence.ActualHash != "" {
		t.Fatalf("refused traversal still hashed the file: %+v", got.Evidence)
	}
}

// The lexical check (Clean + ".." prefix + filepath.Rel) cannot see a symlink:
// the path stays inside the workspace as a string while the bytes come from
// somewhere else. This is the case that made the fix necessary at all.
func TestAbsPath_RejectsSymlinkEscapingWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("host secret"), 0o644); err != nil {
		t.Fatalf("failed to write secret: %v", err)
	}
	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	got := evalPath(t, root, AssertionFileHashEquals, map[string]any{
		"path":          "innocent.txt",
		"expected_hash": sha256Of(t, target),
	})

	if got.Result != ResultError {
		t.Fatalf("a symlink pointing outside the workspace must be refused, got %s (%s)", got.Result, got.Message)
	}
	if got.Evidence.ActualHash != "" {
		t.Fatalf("refused symlink still hashed the file: %+v", got.Evidence)
	}
}

// A symlink that stays INSIDE the workspace is legitimate and must keep working:
// containment is about the target, not about banning links.
func TestAbsPath_AllowsSymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("inside"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	got := evalPath(t, root, AssertionFileHashEquals, map[string]any{
		"path":          "link.txt",
		"expected_hash": sha256Of(t, real),
	})
	if got.Result != ResultPass {
		t.Fatalf("a symlink inside the workspace must still pass, got %s (%s)", got.Result, got.Message)
	}
}

// The ordinary case: containment must not turn into a blanket denial.
func TestAbsPath_AllowsRelativePathInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pkg", "auth")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	target := filepath.Join(nested, "jwt.go")
	if err := os.WriteFile(target, []byte("package auth\n"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	got := evalPath(t, root, AssertionFileHashEquals, map[string]any{
		"path":          filepath.Join("pkg", "auth", "jwt.go"),
		"expected_hash": sha256Of(t, target),
	})
	if got.Result != ResultPass {
		t.Fatalf("a relative path inside the workspace must pass, got %s (%s)", got.Result, got.Message)
	}
}

// cwd is the other consumer: it becomes the working directory of a host-side
// `git status`, so it needs the same containment. The escape target has to be a
// real git repository, otherwise the check passes for the wrong reason (git
// failing with "not a repository" already yields an error result).
func TestAbsPath_RejectsCwdEscapingWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if out, err := exec.Command("git", "-C", outside, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, out)
	}
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatalf("failed to build relative path: %v", err)
	}

	got := evalPath(t, root, AssertionGitCleanWorktree, map[string]any{"cwd": rel})
	if got.Result != ResultError {
		t.Fatalf("a cwd escaping the workspace must be refused, got %s (%s)", got.Result, got.Message)
	}
}

// The counterpart: cwd inside the workspace is a legitimate use of the same
// assertion, so the containment fix cannot have disabled it.
func TestAbsPath_AllowsCwdInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, out)
	}

	got := evalPath(t, root, AssertionGitCleanWorktree, map[string]any{"cwd": "."})
	if got.Result != ResultPass {
		t.Fatalf("a cwd inside the workspace must still pass, got %s (%s)", got.Result, got.Message)
	}
}

// requireBinary skips the test when the helper binary is unavailable.
func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not available: %v", name, err)
	}
}

func evalExitCode(t *testing.T, e *Evaluator, command string) *AssertionResult {
	t.Helper()
	return e.Evaluate(context.Background(), &Assertion{
		ID:     "a1",
		Type:   AssertionCommandExitCode,
		Params: map[string]any{"command": command, "expected_code": 1},
	}, 0)
}

// F2-02: NewEvaluator captured os.Environ() and handed it to every command the
// assertions ran, so `go test` inside a settlement could read the worker's API
// keys, tokens and agent configuration. The environment must be an allowlist:
// deny by default, with WithEnv as the explicit escape hatch for callers that
// genuinely need one more variable.
func TestEvaluator_CommandDoesNotInheritHostEnvironment(t *testing.T) {
	requireBinary(t, "printenv")
	// A stand-in for the secrets a real worker carries (API keys, agent config).
	t.Setenv("GENTLE_MESH_FAKE_SECRET", "s3cr3t")

	got := evalExitCode(t, NewEvaluator(t.TempDir()), "printenv GENTLE_MESH_FAKE_SECRET")

	// printenv exits 1 when the variable is absent, which is what the
	// assertion expects. If the host environment were inherited it would exit
	// 0 and the leak would show up as a failure here.
	if got.Result != ResultPass {
		t.Fatalf("the host environment must not reach the command: %s (%s)", got.Result, got.Message)
	}
}

// Containment of variables must not degrade into running with no environment at
// all: the toolchain assertions actually execute (go, git) need these.
func TestEvaluator_CommandKeepsToolchainEnvironment(t *testing.T) {
	requireBinary(t, "printenv")
	root := t.TempDir()

	for _, name := range []string{"PATH", "HOME"} {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		result := NewEvaluator(root).Evaluate(context.Background(), &Assertion{
			ID:     "a1",
			Type:   AssertionCommandExitCode,
			Params: map[string]any{"command": "printenv " + name, "expected_code": 0},
		}, 0)
		if result.Result != ResultPass {
			t.Fatalf("%s must survive the allowlist: %s (%s)", name, result.Result, result.Message)
		}
	}
}

// WithEnv stays available for callers that need one extra variable, and it
// still wins over the inherited environment for that name.
func TestEvaluator_WithEnvAddsExplicitVariables(t *testing.T) {
	requireBinary(t, "printenv")
	root := t.TempDir()

	result := NewEvaluator(root).WithEnv([]string{"GENTLE_MESH_EXPLICIT=1"}).
		Evaluate(context.Background(), &Assertion{
			ID:     "a1",
			Type:   AssertionCommandExitCode,
			Params: map[string]any{"command": "printenv GENTLE_MESH_EXPLICIT", "expected_code": 0},
		}, 0)
	if result.Result != ResultPass {
		t.Fatalf("WithEnv must add the requested variable: %s (%s)", result.Result, result.Message)
	}
}

// F2-03 (same class, different component): the Evaluator's git_clean_worktree
// check runs `git status` on the host against the workspace the agent worked
// in. If that workspace's git config is writable by the agent, the same
// config-driven execution paths exist here: a poisoned core.fsmonitor in the
// workspace config runs on the host.
func TestEvaluator_GitCleanWorktreeIgnoresWorkspaceConfig(t *testing.T) {
	workspace := t.TempDir()
	if out, err := exec.Command("git", "-C", workspace, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, out)
	}

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "pwned")
	// Outside the workspace: a script inside it would show up in
	// git status --porcelain as untracked and make the worktree dirty.
	payload := filepath.Join(markerDir, "payload.sh")
	script := "#!/bin/sh\necho pwned >> " + shellQuote(marker) + "\nexit 1\n"
	if err := os.WriteFile(payload, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	if out, err := exec.Command("git", "-C", workspace, "config", "core.fsmonitor", payload).CombinedOutput(); err != nil {
		t.Fatalf("failed to poison workspace config: %v\n%s", err, out)
	}

	// Teeth: unhardened git DOES execute it in this workspace.
	sanityMarker := filepath.Join(markerDir, "unhardened")
	sanityPayload := filepath.Join(markerDir, "sanity.sh")
	if err := os.WriteFile(sanityPayload, []byte("#!/bin/sh\necho pwned >> "+shellQuote(sanityMarker)+"\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("failed to write sanity payload: %v", err)
	}
	if out, err := exec.Command("git", "-C", workspace, "config", "core.fsmonitor", sanityPayload).CombinedOutput(); err != nil {
		t.Fatalf("failed to repoint fsmonitor: %v\n%s", err, out)
	}
	_ = exec.Command("git", "-C", workspace, "status", "--porcelain").Run()
	if _, err := os.Stat(sanityMarker); err != nil {
		t.Skipf("git does not execute core.fsmonitor here; cannot prove the escape: %v", err)
	}
	if out, err := exec.Command("git", "-C", workspace, "config", "core.fsmonitor", payload).CombinedOutput(); err != nil {
		t.Fatalf("failed to restore fsmonitor: %v\n%s", err, out)
	}

	got := NewEvaluator(workspace).Evaluate(context.Background(), &Assertion{
		ID:     "a1",
		Type:   AssertionGitCleanWorktree,
		Params: map[string]any{},
	}, 0)

	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("ESCAPE: the workspace core.fsmonitor ran on the host %d time(s): %s",
			bytes.Count(data, []byte("pwned")), strings.TrimSpace(string(data)))
	}
	if got.Result != ResultPass {
		t.Fatalf("a clean worktree must still pass, got %s (%s)", got.Result, got.Message)
	}
}

// shellQuote single-quotes a path for safe interpolation into a shell payload.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// printenvExit runs `printenv <name>` through an assertion and returns the
// command's exit code: 0 when the variable reached the child, 1 when it did not.
// Reading the exit code directly keeps the assertions below readable.
func printenvExit(t *testing.T, e *Evaluator, name string) int {
	t.Helper()
	// expected_code 1 never matches, so the assertion reports FAIL and the
	// evidence carries the real exit code instead of being discarded.
	got := e.Evaluate(context.Background(), &Assertion{
		ID:     "a1",
		Type:   AssertionCommandExitCode,
		Params: map[string]any{"command": "printenv " + name, "expected_code": 1},
	}, 0)
	return got.Evidence.ExitCode
}

// The operator escape hatch for variables a worker legitimately needs behind a
// proxy or with a private module registry (GOPROXY, HTTPS_PROXY, GOPRIVATE).
// The default stays a strict allowlist, so the exception is something the
// operator asks for on purpose, the same shape as -local-fallback.
func TestEvaluator_EnvAllowExtendsOnlyTheNamedVariables(t *testing.T) {
	requireBinary(t, "printenv")
	root := t.TempDir()
	t.Setenv("GENTLE_MESH_FAKE_OPTIONAL", "on")
	t.Setenv("GENTLE_MESH_FAKE_OTHER", "on")

	if code := printenvExit(t, NewEvaluator(root), "GENTLE_MESH_FAKE_OPTIONAL"); code == 0 {
		t.Fatal("by default a variable outside the allowlist must not reach the command")
	}

	extended := NewEvaluator(root).WithEnvAllow("GENTLE_MESH_FAKE_OPTIONAL")
	if code := printenvExit(t, extended, "GENTLE_MESH_FAKE_OPTIONAL"); code != 0 {
		t.Fatalf("an explicitly allowed variable must reach the command, exit %d", code)
	}
	// Naming one variable must not open its neighbours.
	if code := printenvExit(t, extended, "GENTLE_MESH_FAKE_OTHER"); code == 0 {
		t.Fatal("allowing one variable must not leak another one that was not named")
	}
}

// A name must be matched exactly: a prefix of a real variable is not that
// variable, and WithEnvAllow is the operator's escape hatch, so a sloppy match
// here would be a hole.
func TestEvaluator_EnvAllowMatchesNamesExactly(t *testing.T) {
	requireBinary(t, "printenv")
	root := t.TempDir()
	t.Setenv("GENTLE_MESH_EXACT", "on")
	t.Setenv("GENTLE_MESH_EXTRALONGER", "on")

	extended := NewEvaluator(root).WithEnvAllow("GENTLE_MESH_EXACT")
	if code := printenvExit(t, extended, "GENTLE_MESH_EXACT"); code != 0 {
		t.Fatalf("the named variable must reach the command, exit %d", code)
	}
	if code := printenvExit(t, extended, "GENTLE_MESH_EXTRALONGER"); code == 0 {
		t.Fatal("a variable whose name merely starts with an allowed name must stay out")
	}
}
