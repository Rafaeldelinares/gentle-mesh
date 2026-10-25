package shell

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func testTLSOption(t *testing.T, srv *httptest.Server) agent.TLSClientOption {
	t.Helper()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return agent.WithCACert(caFile)
}

// TestMeshClient_Health verifies that Health() returns agent info from the remote server.
func TestMeshClient_Health(t *testing.T) {
	// Create a test server that returns a health response.
	pubKey, privKey, _ := ed25519.GenerateKey(nil)
	pubKeyHex := hex.EncodeToString(pubKey)

	signer, err := signing.NewBasicSigner(privKey, "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(agent.HealthResponse{
			Status:    "ok",
			AgentID:   "test-agent",
			PublicKey: pubKeyHex,
		})
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer)
	if err != nil {
		t.Fatal(err)
	}

	info, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want %q", info.AgentID, "test-agent")
	}
	if info.PublicKey != pubKeyHex {
		t.Errorf("PublicKey = %q, want %q", info.PublicKey, pubKeyHex)
	}
}

// TestMeshClient_SubmitEnvelope verifies that SubmitEnvelope() signs the envelope
// and sends it to the server.
func TestMeshClient_SubmitEnvelope(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	var receivedEnvelope *envelope.CognitiveTaskEnvelope
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		var req agent.EnvelopeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(req.EnvelopeJSON, &receivedEnvelope); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(agent.EnvelopeResponse{
			Accepted: true,
			LeaseID: "lease-123",
		})
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer, testTLSOption(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion:  envelope.CurrentProtocolVersion,
		MeshID:           "gentle-mesh-dev",
		EnvelopeID:      "envelope-001",
		EmitterAgentID:   "emitter",
		ExecutorAgentID:  "executor",
		TimeoutSeconds:   60,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:       "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{ID: "always-pass", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0}},
		},
	}

	resp, err := client.SubmitEnvelope(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Accepted {
		t.Error("lease should be accepted")
	}
	if resp.LeaseID != "lease-123" {
		t.Errorf("LeaseID = %q, want %q", resp.LeaseID, "lease-123")
	}
	if receivedEnvelope == nil {
		t.Fatal("server did not receive envelope")
	}
	if receivedEnvelope.EmitterSignature == "" {
		t.Error("EmitterSignature should be set")
	}
}

// TestMeshClient_Check_RemoteEvaluation_ToolMissing tests RFC-002 A2 (Issue #37):
// Check() queries the remote executor via POST /preflight. When preconditions are not met,
// Check() returns Passed: false with the remote rejection reason and detailed results.
func TestMeshClient_Check_RemoteEvaluation_ToolMissing(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	preflightCalled := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/preflight" {
			preflightCalled = true
			var req agent.PreflightRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(agent.PreflightResponse{
				Passed:          false,
				RejectionReason: `tool "nonexistent-tool-xyz-12345" available=false`,
				Results: []envelope.PreconditionResult{
					{
						PreconditionIndex: 0,
						Type:              string(envelope.PreconditionToolAvailable),
						Passed:            false,
						Message:           `tool "nonexistent-tool-xyz-12345" available=false`,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer, testTLSOption(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	report, err := client.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "nonexistent-tool-xyz-12345"}},
	})
	if err != nil {
		t.Fatalf("unexpected Check error: %v", err)
	}
	if !preflightCalled {
		t.Error("POST /preflight should have been called on remote executor")
	}
	if report.Passed {
		t.Errorf("expected report.Passed = false for missing tool, got true (fail-open regression)")
	}
	if len(report.Results) != 1 || report.Results[0].Passed {
		t.Errorf("expected 1 failing result, got: %+v", report.Results)
	}
	if report.RejectionReason == "" {
		t.Error("expected non-empty RejectionReason")
	}
}

// TestMeshClient_Check_RemoteEvaluation_Success tests that Check() returns Passed: true
// when the remote executor verifies all preconditions.
func TestMeshClient_Check_RemoteEvaluation_Success(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/preflight" {
			json.NewEncoder(w).Encode(agent.PreflightResponse{
				Passed: true,
				Results: []envelope.PreconditionResult{
					{
						PreconditionIndex: 0,
						Type:              string(envelope.PreconditionToolAvailable),
						Passed:            true,
						Message:           `tool "go" available=true`,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer, testTLSOption(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	report, err := client.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
	})
	if err != nil {
		t.Fatalf("unexpected Check error: %v", err)
	}
	if !report.Passed {
		t.Errorf("expected report.Passed = true, got false with reason: %s", report.RejectionReason)
	}
}

// TestMeshClient_Check_RemoteEvaluation_MultiplePreconditions tests that if any precondition fails,
// Check() returns Passed: false and preserves the individual results.
func TestMeshClient_Check_RemoteEvaluation_MultiplePreconditions(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/preflight" {
			json.NewEncoder(w).Encode(agent.PreflightResponse{
				Passed:          false,
				RejectionReason: `command "false" exit=1`,
				Results: []envelope.PreconditionResult{
					{
						PreconditionIndex: 0,
						Type:              string(envelope.PreconditionToolAvailable),
						Passed:            true,
						Message:           `tool "go" available=true`,
					},
					{
						PreconditionIndex: 1,
						Type:              string(envelope.PreconditionCommandExitCode),
						Passed:            false,
						Message:           `command "false" exit=1`,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer, testTLSOption(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	report, err := client.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": "false"}},
	})
	if err != nil {
		t.Fatalf("unexpected Check error: %v", err)
	}
	if report.Passed {
		t.Errorf("expected report.Passed = false, got true")
	}
	if len(report.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(report.Results))
	}
	if !report.Results[0].Passed || report.Results[1].Passed {
		t.Errorf("unexpected results state: %+v", report.Results)
	}
	if report.RejectionReason == "" {
		t.Errorf("expected non-empty RejectionReason")
	}
}

// TestMeshClient_Check_EndpointNotFound_ReturnsError verifies that if the remote executor
// does not implement /preflight (returns 404), Check() fails with an explicit error instead
// of failing open.
func TestMeshClient_Check_EndpointNotFound_ReturnsError(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewMeshClient(srv.URL, signer, testTLSOption(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
	})
	if err == nil {
		t.Fatal("expected error when /preflight returns 404, got nil")
	}
}


// TestMeshClient_CheckUnreachable returns error when executor is unreachable.
func TestMeshClient_CheckUnreachable(t *testing.T) {
	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	// Use a URL that will fail to connect.
	client, err := NewMeshClient("https://localhost:99999", signer)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
	})
	if err == nil {
		t.Error("Check() should return error for unreachable executor")
	}
}

// TestMeshClient_DefaultVerifiesTLS verifies that NewMeshClient without options
// performs standard TLS certificate verification and rejects untrusted certs.
func TestMeshClient_DefaultVerifiesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, privKey, _ := ed25519.GenerateKey(nil)
	signer, _ := signing.NewBasicSigner(privKey, "test-agent")

	// Calling NewMeshClient without options must NOT bypass TLS verification.
	client, err := NewMeshClient(srv.URL, signer)
	if err != nil {
		t.Fatalf("unexpected client creation error: %v", err)
	}

	_, err = client.Health(context.Background())
	if err == nil {
		t.Fatal("expected TLS certificate verification error for untrusted self-signed server, got nil")
	}
}

