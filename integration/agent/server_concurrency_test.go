package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
)

// TestServer_ConcurrentLeaseWrites is an in-process concurrency test using httptest
// without Docker. It fires N concurrent POST requests across /envelopes and /leases
// to verify thread-safety of Server.leases.
//
// Under go test -race (or high concurrency), an unprotected map will trigger:
//   WARNING: DATA RACE or fatal error: concurrent map writes
func TestServer_ConcurrentLeaseWrites(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gentle-mesh-concurrency-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	cfg := Config{
		AgentID:      "test-executor",
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: workspaceDir,
		EvalTimeout:  5 * time.Second,
		MaxRemed:     1,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Build a valid envelope.
	baseEnv := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "test-executor",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: workspaceDir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "check-exit",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "true",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds:  30,
		MaxRemediations: 0,
		CreatedAt:       time.Now().UTC(),
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
	}

	client := ts.Client()
	const numGoroutines = 40
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			// Create a distinct envelope for each request to avoid any deduplication.
			env := *baseEnv
			env.EnvelopeID = fmt.Sprintf("01923456-789a-7bc0-8123-%012x", idx)
			envJSON, err := json.Marshal(&env)
			if err != nil {
				t.Errorf("marshal envelope: %v", err)
				return
			}

			switch idx % 3 {
			case 0:
				// POST /envelopes
				reqBody, _ := json.Marshal(EnvelopeRequest{
					EnvelopeJSON: envJSON,
				})
				resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(reqBody))
				if err != nil {
					t.Errorf("POST /envelopes failed: %v", err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("POST /envelopes status=%d", resp.StatusCode)
				}
			case 1:
				// POST /leases
				reqBody, _ := json.Marshal(EnvelopeRequest{
					EnvelopeJSON: envJSON,
				})
				resp, err := client.Post(ts.URL+"/leases", "application/json", bytes.NewReader(reqBody))
				if err != nil {
					t.Errorf("POST /leases failed: %v", err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("POST /leases status=%d", resp.StatusCode)
				}
			case 2:
				// Concurrent read via GetLease
				for j := 0; j < 10; j++ {
					_, _ = srv.GetLease(fmt.Sprintf("lease-%d", j))
				}
			}
		}(i)
	}

	wg.Wait()
}
