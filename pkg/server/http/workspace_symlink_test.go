package http_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	meshhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
)

// startWorkspaceServer builds a *Server in-process with the given workspaceRoot and
// no network plumbing. Workspace tests call handler methods directly on it.
func startWorkspaceServer(t *testing.T, workspaceRoot string) *meshhttp.Server {
	t.Helper()
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5_000_000_000, // 5s; value-overflow safe
		TaskTTL:          3_600_000_000_000,
		WorkspaceRoot:    workspaceRoot,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// callWorkspaceFile invokes the /v1/workspace/file handler directly and returns
// the response status plus the parsed body.
func callWorkspaceFile(t *testing.T, srv *meshhttp.Server, queryPath string) (int, meshhttp.WorkspaceFileResponse, string) {
	t.Helper()
	u := url.Values{"path": {queryPath}}
	r := httptest.NewRequest("GET", "/v1/workspace/file?"+u.Encode(), nil)
	w := httptest.NewRecorder()
	srv.HandleWorkspaceFileForTest(w, r)
	body := w.Body.String()
	var resp meshhttp.WorkspaceFileResponse
	_ = json.Unmarshal([]byte(body), &resp)
	return w.Code, resp, body
}

func callWorkspaceTree(t *testing.T, srv *meshhttp.Server, queryPath string) (int, meshhttp.WorkspaceTreeResponse, string) {
	t.Helper()
	u := url.Values{"path": {queryPath}}
	r := httptest.NewRequest("GET", "/v1/workspace/tree?"+u.Encode(), nil)
	w := httptest.NewRecorder()
	srv.HandleWorkspaceTreeForTest(w, r)
	body := w.Body.String()
	var resp meshhttp.WorkspaceTreeResponse
	_ = json.Unmarshal([]byte(body), &resp)
	return w.Code, resp, body
}

// TestWorkspaceFileRejectsSymlinkEscapingWorkspace pins the F2-05 escape.
//
// A symlink inside the workspace pointing to a file outside the root passes the
// existing lexical containment (the path stays "inside" as a string), and
// os.ReadFile follows symlinks, so the content of the host file ends up in the
// JSON response. The agent under sandbox commit a symlink; anyone who can read
// the coordinator endpoint gets the target. Fixed by resolving the symlink and
// verifying the resolved path is still inside the workspace, then opening the
// file without following the last symlink.
func TestWorkspaceFileRejectsSymlinkEscapingWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret content"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, resp, body := callWorkspaceFile(t, srv, "innocent.txt")

	if code == 200 {
		t.Fatalf("ESCAPE: the symlink resolved to an outside file and was returned (status 200). body=%s", body)
	}
	if resp.Content != "" {
		t.Fatalf("ESCAPE: a refused path still leaked content: %q", resp.Content)
	}
	if !strings.Contains(body, "outside") && code != 403 && code != 404 {
		// Either the explicit error mentions "outside" or we get a clean denial.
		t.Logf("status=%d body=%s (acceptable as long as no content leaked)", code, body)
	}
}

func TestWorkspaceFileAllowsLegitFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pkg", "auth", "jwt.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("package auth\n")
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, resp, _ := callWorkspaceFile(t, srv, "pkg/auth/jwt.go")
	if code != 200 {
		t.Fatalf("a legit file in the workspace must be returned, got %d", code)
	}
	if resp.Content != string(body) {
		t.Fatalf("legit content mismatch: got %q want %q", resp.Content, body)
	}
}

// A symlink that resolves inside the workspace is legitimate. Fixing the escape
// must not ban links: it must allow them once the resolution still lands inside.
func TestWorkspaceFileAllowsSymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("inside"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, resp, _ := callWorkspaceFile(t, srv, "link.txt")
	if code != 200 {
		t.Fatalf("a symlink that resolves inside the workspace must succeed, got %d", code)
	}
	if resp.Content != "inside" {
		t.Fatalf("symlink content mismatch: got %q", resp.Content)
	}
}

// handleWorkspaceTree used the same resolver: a symlinked subdirectory pointing
// outside the workspace must not be listed, because os.ReadDir would otherwise
// walk into it and the response would describe the contents of an arbitrary
// directory on the host.
func TestWorkspaceTreeRejectsSymlinkedDirectoryEscaping(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.txt"), []byte("should not appear"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, _, body := callWorkspaceTree(t, srv, "linked")
	if code == 200 {
		t.Fatalf("ESCAPE: symlinked directory outside the workspace was listed. body=%s", body)
	}
	if strings.Contains(body, "private.txt") {
		t.Fatalf("ESCAPE: a refused tree response leaked the outside filename: %s", body)
	}
}

func TestWorkspaceTreeAllowsLegitDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, resp, _ := callWorkspaceTree(t, srv, "sub")
	if code != 200 {
		t.Fatalf("a legit subdirectory must be listed, got %d", code)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v, want one a.txt", resp.Entries)
	}
}

// Workspace root itself can legitimately be a symlink (the operator may keep
// the cache in another location and symlink it). Resolution must not break that.
func TestWorkspaceFileAllowsRootBeingSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.txt")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	linkRoot := t.TempDir() + "/symlinked"
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := startWorkspaceServer(t, linkRoot)
	code, resp, _ := callWorkspaceFile(t, srv, "real.txt")
	if code != 200 {
		t.Fatalf("a real file inside a symlinked root must be reachable, got %d", code)
	}
	if resp.Content != "data" {
		t.Fatalf("content mismatch: got %q", resp.Content)
	}
}

// Dangling symlink: a link whose target does not exist. The fix should not
// crash, and the response is a clean 404 (no escape, no leak).
func TestWorkspaceFileDanglingSymlinkIs404(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "nonexistent"), filepath.Join(root, "dangling")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	srv := startWorkspaceServer(t, root)
	code, resp, _ := callWorkspaceFile(t, srv, "dangling")
	if code != 404 {
		t.Fatalf("dangling symlink must surface as 404, got %d", code)
	}
	if resp.Content != "" {
		t.Fatalf("404 must not carry content, got %q", resp.Content)
	}
}
