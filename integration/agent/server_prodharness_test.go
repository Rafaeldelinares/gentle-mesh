//go:build !testharness
// +build !testharness

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEndpointsAbsentInProduction verifies that when the agent server is compiled
// WITHOUT the testharness build tag, the /execute and /inject-receipt endpoints
// are NOT registered in the HTTP routing table and return 404.
//
// How it works:
//   - server_harness.go (//go:build testharness) registers /execute and /inject-receipt.
//   - server_harness_stub.go (//go:build !testharness) does NOT register them.
//   - This test is compiled ONLY in non-testharness builds; it calls registerHandlers
//     which is the stub in this build (no-op), so the routes are absent.
//
// Regression test for the 404 regression: building without -tags testharness
// must NOT expose the /execute and /inject-receipt endpoints.
func TestEndpointsAbsentInProduction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gentle-mesh-prod-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	dbPath := filepath.Join(tmpDir, "chain.db")
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	cfg := Config{
		AgentID:      "prodharness-test",
		MeshID:       "gentle-mesh-test",
		Role:         RoleExecutor,
		ChainDBPath:  dbPath,
		WorkspaceDir: workspaceDir,
		EvalTimeout:  5 * time.Second,
		MaxRemed:     1,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux) // in non-testharness build: stub (no-op)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	baseURL := httpSrv.URL

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/execute"},
		{http.MethodPost, "/inject-receipt"},
	} {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), tc.method, baseURL+tc.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("got status %d, want 404 — %s %s should NOT be registered in non-testharness build",
					resp.StatusCode, tc.method, tc.path)
			}
		})
	}
}
