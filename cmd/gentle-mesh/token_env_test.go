package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// The operator requirement is a strong bearer token that never appears in the
// process argument list, because argv is readable through /proc and ps.
// `gentle-mesh rpc` already defaults -token to $GENTLE_MESH_TOKEN; these tests
// pin the same default for `server` and `worker`, the two processes that hold
// the token in a deployment.

func TestCoordinator_TokenDefaultsToEnv(t *testing.T) {
	t.Setenv("GENTLE_MESH_TOKEN", "env-secret-token")

	addr, stop := startCoordinator(t, nil)
	defer func() { _ = stop() }()

	client := &http.Client{Timeout: 5 * time.Second}

	// Without the bearer token the API must stay closed.
	resp, err := client.Get("http://" + addr + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("unauthenticated GET /v1/mesh/nodes failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/mesh/nodes without the env token: got %d, want %d (body: %s)", resp.StatusCode, http.StatusUnauthorized, body)
	}

	// The value from the environment is the one the server actually enforces.
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/mesh/nodes", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer env-secret-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("authenticated GET /v1/mesh/nodes failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/mesh/nodes with the env token: got %d, want %d (body: %s)", resp.StatusCode, http.StatusOK, body)
	}
}

func TestWorker_TokenDefaultsToEnv(t *testing.T) {
	t.Setenv("GENTLE_MESH_TOKEN", "env-secret-token")

	var (
		mu             sync.Mutex
		joinToken      string
		heartbeatToken string
	)
	heartbeatSeen := make(chan struct{})
	var closeOnce sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/mesh/join":
			var joinReq protocol.NodeJoinRequest
			_ = json.NewDecoder(r.Body).Decode(&joinReq)
			mu.Lock()
			joinToken = r.Header.Get("Authorization")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.NodeInfo{NodeID: joinReq.NodeID, Status: protocol.NodeStatusOnline})
		case "/v1/mesh/heartbeat":
			var hb protocol.NodeHeartbeatRequest
			_ = json.NewDecoder(r.Body).Decode(&hb)
			mu.Lock()
			heartbeatToken = r.Header.Get("Authorization")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})
			closeOnce.Do(func() { close(heartbeatSeen) })
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		// No -token flag: the credential must come from the environment alone.
		errCh <- runCLI(ctx, []string{
			"worker",
			"-coordinator", ts.URL,
			"-node-id", "env-token-worker",
			"-endpoint", "http://127.0.0.1:18081",
			"-addr", "127.0.0.1:18081",
			"-heartbeat-interval", "50ms",
		}, &stdout, &stderr)
	}()

	select {
	case <-heartbeatSeen:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the worker heartbeat. Stderr: %s", stderr.String())
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("worker returned unexpected error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down within timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if joinToken != "Bearer env-secret-token" {
		t.Errorf("join Authorization header: got %q, want %q", joinToken, "Bearer env-secret-token")
	}
	if heartbeatToken != "Bearer env-secret-token" {
		t.Errorf("heartbeat Authorization header: got %q, want %q", heartbeatToken, "Bearer env-secret-token")
	}
}
