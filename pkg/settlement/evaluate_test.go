package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
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
