package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
)

func setupPreflightTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	tmpDir := t.TempDir()
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	cfg := Config{
		AgentID:      "test-executor-a2",
		MeshID:       "mesh-a2-test",
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: workspaceDir,
		EvalTimeout:  5 * time.Second,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return srv, ts
}

// TestA2_Preflight_ToolMissing verifies RFC-002 A2 behavior:
// An executor that does not satisfy required tools reports Passed: false
// and includes structured rejection reasons without creating a lease.
func TestA2_Preflight_ToolMissing(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	req := &PreflightRequest{
		Preconditions: []envelope.Precondition{
			{
				Type:   envelope.PreconditionToolAvailable,
				Params: map[string]string{"tool": "nonexistent-tool-a2-99999"},
			},
		},
	}
	reqBytes, _ := json.Marshal(req)

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	var preflightResp PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
		t.Fatalf("decode PreflightResponse: %v", err)
	}

	if preflightResp.Passed {
		t.Errorf("expected preflightResp.Passed = false for missing tool, got true (fail-open regression)")
	}
	if len(preflightResp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(preflightResp.Results))
	}
	if preflightResp.Results[0].Passed {
		t.Errorf("result[0].Passed should be false")
	}
	if preflightResp.RejectionReason == "" {
		t.Errorf("expected non-empty rejection reason")
	}
}

// TestA2_Preflight_ToolAvailable verifies that available tools return Passed: true.
func TestA2_Preflight_ToolAvailable(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	req := &PreflightRequest{
		Preconditions: []envelope.Precondition{
			{
				Type:   envelope.PreconditionToolAvailable,
				Params: map[string]string{"tool": "go"},
			},
		},
	}
	reqBytes, _ := json.Marshal(req)

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	var preflightResp PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
		t.Fatalf("decode PreflightResponse: %v", err)
	}

	if !preflightResp.Passed {
		t.Errorf("expected preflightResp.Passed = true for 'go' tool, got false: %s", preflightResp.RejectionReason)
	}
	if len(preflightResp.Results) != 1 || !preflightResp.Results[0].Passed {
		t.Errorf("expected result[0].Passed = true, got: %+v", preflightResp.Results)
	}
}

// TestA2_Preflight_CommandExitCode verifies command exit code evaluation in preflight.
func TestA2_Preflight_CommandExitCode(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	tests := []struct {
		name       string
		cmd        string
		wantPassed bool
	}{
		{name: "exit code 0 passes", cmd: "true", wantPassed: true},
		{name: "exit code 1 fails", cmd: "false", wantPassed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &PreflightRequest{
				Preconditions: []envelope.Precondition{
					{
						Type:   envelope.PreconditionCommandExitCode,
						Params: map[string]string{"command": tt.cmd},
					},
				},
			}
			reqBytes, _ := json.Marshal(req)

			resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
			if err != nil {
				t.Fatalf("POST /preflight: %v", err)
			}
			defer resp.Body.Close()

			var preflightResp PreflightResponse
			if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
				t.Fatalf("decode PreflightResponse: %v", err)
			}

			if preflightResp.Passed != tt.wantPassed {
				t.Errorf("command %q: got Passed=%v, want %v", tt.cmd, preflightResp.Passed, tt.wantPassed)
			}
		})
	}
}

// TestA2_Preflight_MultipleMixedPreconditions verifies that one failing precondition
// causes overall Passed=false while preserving individual result breakdowns.
func TestA2_Preflight_MultipleMixedPreconditions(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	req := &PreflightRequest{
		Preconditions: []envelope.Precondition{
			{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
			{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "missing-tool-xyz"}},
		},
	}
	reqBytes, _ := json.Marshal(req)

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	var preflightResp PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
		t.Fatalf("decode PreflightResponse: %v", err)
	}

	if preflightResp.Passed {
		t.Error("expected overall Passed=false when one precondition fails")
	}
	if len(preflightResp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(preflightResp.Results))
	}
	if !preflightResp.Results[0].Passed {
		t.Errorf("expected result[0] ('go') Passed=true")
	}
	if preflightResp.Results[1].Passed {
		t.Errorf("expected result[1] ('missing-tool-xyz') Passed=false")
	}
}

// TestA2_Preflight_EmptyPreconditions verifies that empty preconditions pass.
func TestA2_Preflight_EmptyPreconditions(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	req := &PreflightRequest{
		Preconditions: []envelope.Precondition{},
	}
	reqBytes, _ := json.Marshal(req)

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	var preflightResp PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
		t.Fatalf("decode PreflightResponse: %v", err)
	}

	if !preflightResp.Passed {
		t.Errorf("empty preconditions should pass, got false")
	}
	if len(preflightResp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(preflightResp.Results))
	}
}

// TestA2_Preflight_InvalidJSON verifies 400 Bad Request on malformed body.
func TestA2_Preflight_InvalidJSON(t *testing.T) {
	_, ts := setupPreflightTestServer(t)
	client := ts.Client()

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader([]byte("{invalid-json")))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 Bad Request, got %d", resp.StatusCode)
	}
}

// TestA2_Preflight_HTTPClientMethod verifies that HTTPClient.Preflight method
// correctly interacts with the POST /preflight endpoint.
func TestA2_Preflight_HTTPClientMethod(t *testing.T) {
	_, ts := setupPreflightTestServer(t)

	client, err := NewHTTPClientTLS(ts.URL, WithDevInsecureTLS(true))
	if err != nil {
		t.Fatalf("NewHTTPClientTLS: %v", err)
	}

	// 1. Missing tool -> Passed=false
	respFail, err := client.Preflight(context.Background(), &PreflightRequest{
		Preconditions: []envelope.Precondition{
			{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "completely-nonexistent-tool-12345"}},
		},
	})
	if err != nil {
		t.Fatalf("client.Preflight error: %v", err)
	}
	if respFail.Passed {
		t.Errorf("expected Passed=false for missing tool, got true")
	}
	if respFail.RejectionReason == "" {
		t.Errorf("expected non-empty RejectionReason")
	}

	// 2. Available tool -> Passed=true
	respPass, err := client.Preflight(context.Background(), &PreflightRequest{
		Preconditions: []envelope.Precondition{
			{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
		},
	})
	if err != nil {
		t.Fatalf("client.Preflight error: %v", err)
	}
	if !respPass.Passed {
		t.Errorf("expected Passed=true for 'go', got false: %s", respPass.RejectionReason)
	}
}
