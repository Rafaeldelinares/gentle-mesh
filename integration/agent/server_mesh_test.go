package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func setupTestServerWithMesh(t *testing.T, meshID string) (*Server, *httptest.Server, *signing.BasicSigner) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "gentle-mesh-server-*")
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
		MeshID:       meshID,
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

	return srv, ts, srv.signer
}

func makeValidEnvelope(meshID string) *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          meshID,
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "test-executor",
		TimeoutSeconds:  60,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
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
	}
}

// 1. Leases version and mesh ID: Test that all server-issued leases set
// ProtocolVersion = CurrentProtocolVersion and MeshID = env.MeshID and pass ValidateLease.
func TestServer_IssuedLeases_HaveProtocolVersionAndMeshID(t *testing.T) {
	srv, ts, _ := setupTestServerWithMesh(t, "mesh-alpha")
	client := ts.Client()

	env := makeValidEnvelope("mesh-alpha")
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Test handleSubmitEnvelope (POST /envelopes)
	envelopeReqBody, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envJSON})
	resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(envelopeReqBody))
	if err != nil {
		t.Fatalf("POST /envelopes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /envelopes status=%d body=%s", resp.StatusCode, string(body))
	}

	var envResp EnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&envResp); err != nil {
		t.Fatalf("decode EnvelopeResponse: %v", err)
	}

	lease, ok := srv.GetLease(envResp.LeaseID)
	if !ok {
		t.Fatalf("lease not found in server: %s", envResp.LeaseID)
	}
	if lease.ProtocolVersion != envelope.CurrentProtocolVersion {
		t.Errorf("handleEnvelope lease ProtocolVersion = %q, want %q", lease.ProtocolVersion, envelope.CurrentProtocolVersion)
	}
	if lease.MeshID != "mesh-alpha" {
		t.Errorf("handleEnvelope lease MeshID = %q, want %q", lease.MeshID, "mesh-alpha")
	}
	if err := envelope.ValidateLease(lease); err != nil {
		t.Errorf("handleEnvelope lease failed ValidateLease: %v", err)
	}

	// Test handleCreateLease (POST /leases)
	leaseReqBody, _ := json.Marshal(map[string]any{"envelope_json": envJSON})
	leaseResp, err := client.Post(ts.URL+"/leases", "application/json", bytes.NewReader(leaseReqBody))
	if err != nil {
		t.Fatalf("POST /leases: %v", err)
	}
	defer leaseResp.Body.Close()

	if leaseResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(leaseResp.Body)
		t.Fatalf("POST /leases status=%d body=%s", leaseResp.StatusCode, string(body))
	}

	var createdLease envelope.Lease
	if err := json.NewDecoder(leaseResp.Body).Decode(&createdLease); err != nil {
		t.Fatalf("decode createdLease: %v", err)
	}
	if createdLease.ProtocolVersion != envelope.CurrentProtocolVersion {
		t.Errorf("handleCreateLease lease ProtocolVersion = %q, want %q", createdLease.ProtocolVersion, envelope.CurrentProtocolVersion)
	}
	if createdLease.MeshID != "mesh-alpha" {
		t.Errorf("handleCreateLease lease MeshID = %q, want %q", createdLease.MeshID, "mesh-alpha")
	}
	if err := envelope.ValidateLease(&createdLease); err != nil {
		t.Errorf("handleCreateLease lease failed ValidateLease: %v", err)
	}
}

// 2. Downgrade prevention in POST /leases: Must reject empty or "1.0" versions or invalid envelopes.
func TestServer_DowngradePrevention_PostLeases(t *testing.T) {
	_, ts, _ := setupTestServerWithMesh(t, "mesh-alpha")
	client := ts.Client()

	tests := []struct {
		name            string
		protocolVersion string
		meshID          string
	}{
		{"empty protocol version", "", "mesh-alpha"},
		{"v1.0 protocol version", "1.0", "mesh-alpha"},
		{"v1 protocol version", "1", "mesh-alpha"},
		{"v3 protocol version", "3", "mesh-alpha"},
		{"empty mesh_id", envelope.CurrentProtocolVersion, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := makeValidEnvelope(tt.meshID)
			env.ProtocolVersion = tt.protocolVersion

			envJSON, _ := json.Marshal(env)
			reqBody, _ := json.Marshal(map[string]any{"envelope_json": envJSON})

			resp, err := client.Post(ts.URL+"/leases", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("POST /leases: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("POST /leases with %s: got status %d, want %d (BadRequest)", tt.name, resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

// 4. Mesh isolation: Nodes reject envelopes or receipts from other networks with ErrMeshMismatch.
func TestServer_MeshIsolation(t *testing.T) {
	_, ts, signer := setupTestServerWithMesh(t, "mesh-alpha")
	client := ts.Client()

	// (a) POST /envelopes with mismatched mesh_id
	envBeta := makeValidEnvelope("mesh-beta")
	envBetaJSON, _ := json.Marshal(envBeta)
	envReqBody, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envBetaJSON})

	resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(envReqBody))
	if err != nil {
		t.Fatalf("POST /envelopes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /envelopes mismatched mesh: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), envelope.ErrMeshMismatch.Error()) {
		t.Errorf("POST /envelopes body = %q, want containing %q", string(body), envelope.ErrMeshMismatch.Error())
	}

	// (b) POST /leases with mismatched mesh_id
	leaseReqBody, _ := json.Marshal(map[string]any{"envelope_json": envBetaJSON})
	respLease, err := client.Post(ts.URL+"/leases", "application/json", bytes.NewReader(leaseReqBody))
	if err != nil {
		t.Fatalf("POST /leases: %v", err)
	}
	defer respLease.Body.Close()
	if respLease.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /leases mismatched mesh: status=%d, want %d", respLease.StatusCode, http.StatusBadRequest)
	}

	// (c) POST /settle with mismatched mesh_id
	settleReqBody, _ := json.Marshal(&SettleRequest{
		EnvelopeJSON: envBetaJSON,
		LeaseID:      "lease-dummy",
	})
	respSettle, err := client.Post(ts.URL+"/settle", "application/json", bytes.NewReader(settleReqBody))
	if err != nil {
		t.Fatalf("POST /settle: %v", err)
	}
	defer respSettle.Body.Close()
	if respSettle.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /settle mismatched mesh: status=%d, want %d", respSettle.StatusCode, http.StatusBadRequest)
	}

	// (d) POST /accept with mismatched receipt mesh_id
	recBeta := &receipt.SettlementReceipt{
		ProtocolVersion:  receipt.CurrentProtocolVersion,
		MeshID:           "mesh-beta",
		ReceiptID:        "rcpt-beta-001",
		ContractID:       "contract-001",
		EnvelopeHash:     "abc123",
		EmitterAgentID:   "agent-emitter",
		ExecutorAgentID:  "test-executor",
		Verdict:          receipt.VerdictSettledClean,
		ExecutorSignedAt: time.Now().UTC(),
	}
	_ = receipt.SignReceipt(recBeta, signer)
	recBetaJSON, _ := json.Marshal(recBeta)

	acceptReqBody, _ := json.Marshal(&AcceptRequest{
		ReceiptJSON:         recBetaJSON,
		ExecutorSignedAtRFC: recBeta.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "dummy-sig",
	})
	respAccept, err := client.Post(ts.URL+"/accept", "application/json", bytes.NewReader(acceptReqBody))
	if err != nil {
		t.Fatalf("POST /accept: %v", err)
	}
	defer respAccept.Body.Close()
	if respAccept.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /accept mismatched mesh: status=%d, want %d", respAccept.StatusCode, http.StatusBadRequest)
	}

	// (e) POST /dispute with mismatched receipt mesh_id
	disputeReqBody, _ := json.Marshal(&DisputeRequest{
		ReceiptJSON:         recBetaJSON,
		ExecutorSignedAtRFC: recBeta.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "dummy-sig",
		DisputeReason:       "mismatched mesh",
	})
	respDispute, err := client.Post(ts.URL+"/dispute", "application/json", bytes.NewReader(disputeReqBody))
	if err != nil {
		t.Fatalf("POST /dispute: %v", err)
	}
	defer respDispute.Body.Close()
	if respDispute.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /dispute mismatched mesh: status=%d, want %d", respDispute.StatusCode, http.StatusBadRequest)
	}
}

// 5. Ingress validation: Test client-side CreateLease and receipt methods validate returned data.
func TestClient_CreateLease_IngressValidation(t *testing.T) {
	_, ts, _ := setupTestServerWithMesh(t, "mesh-alpha")
	client := NewHTTPClient(ts.URL)

	env := makeValidEnvelope("mesh-alpha")
	envJSON, _ := json.Marshal(env)

	lease, err := client.CreateLease(context.Background(), envJSON)
	if err != nil {
		t.Fatalf("CreateLease failed: %v", err)
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}
	if err := envelope.ValidateLease(lease); err != nil {
		t.Errorf("returned lease failed ValidateLease: %v", err)
	}
}

func TestClient_IngressValidation_RejectsInvalidReceipt(t *testing.T) {
	// Stand up a mock server returning invalid receipt JSON
	invalidReceiptServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Receipt with invalid protocol_version
		badReceipt := receipt.SettlementReceipt{
			ProtocolVersion: "1.0",
			MeshID:          "mesh-alpha",
			ReceiptID:       "rcpt-1",
			ContractID:      "contract-1",
			EnvelopeHash:    "hash",
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: "agent-b",
		}
		raw, _ := json.Marshal(badReceipt)
		resp := ReceiptResponse{
			ReceiptID:   "rcpt-1",
			ReceiptJSON: raw,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer invalidReceiptServer.Close()

	client := NewHTTPClient(invalidReceiptServer.URL)
	_, err := client.GetReceipt(context.Background(), "rcpt-1")
	if err == nil {
		t.Fatal("expected error from GetReceipt on invalid receipt, got nil")
	}
	if !strings.Contains(err.Error(), "validate receipt") {
		t.Errorf("expected validate receipt error, got: %v", err)
	}
}

// 6. TestServer_EmptyMeshID_FailsStartup verifies that Server requires a non-empty MeshID.
func TestServer_EmptyMeshID_FailsStartup(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		AgentID:      "test-agent",
		MeshID:       "", // Empty MeshID MUST be rejected
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: tmpDir,
	}
	_, err := NewServer(cfg)
	if err == nil {
		t.Fatal("NewServer with empty MeshID should have returned error, got nil")
	}
}

// 7. TestServer_LegacyReceipt_Rejected verifies that receipts without protocol_version
// or mesh_id are rejected on accept, dispute, and verify per S9 and N8.
func TestServer_LegacyReceipt_Rejected(t *testing.T) {
	srv, ts, _ := setupTestServerWithMesh(t, "mesh-alpha")
	httpClient := &http.Client{Timeout: 5 * time.Second}

	now := time.Now().UTC()
	// Legacy receipt without protocol_version and mesh_id
	legacyRec := &receipt.SettlementReceipt{
		ProtocolVersion:  "",
		MeshID:           "",
		ReceiptID:        "legacy-rcpt-001",
		ContractID:       "contract-legacy-1",
		EnvelopeHash:     "legacy-hash-1",
		EmitterAgentID:   "agent-emitter",
		ExecutorAgentID:  "test-executor",
		Verdict:          receipt.VerdictSettledClean,
		ExecutorSignedAt: now,
	}
	legacyJSON, _ := json.Marshal(legacyRec)

	// 1. POST /accept must be rejected with 400
	acceptReqBody, _ := json.Marshal(&AcceptRequest{
		ReceiptJSON:         legacyJSON,
		ExecutorSignedAtRFC: legacyRec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "dummy-emitter-sig",
	})
	respAccept, err := httpClient.Post(ts.URL+"/accept", "application/json", bytes.NewReader(acceptReqBody))
	if err != nil {
		t.Fatalf("POST /accept: %v", err)
	}
	defer respAccept.Body.Close()
	if respAccept.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /accept status=%d, want 400 for legacy receipt", respAccept.StatusCode)
	}

	// 2. POST /dispute must be rejected with 400
	disputeReqBody, _ := json.Marshal(&DisputeRequest{
		ReceiptJSON:         legacyJSON,
		ExecutorSignedAtRFC: legacyRec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "dummy-emitter-sig",
		DisputeReason:       "legacy dispute reason",
	})
	respDispute, err := httpClient.Post(ts.URL+"/dispute", "application/json", bytes.NewReader(disputeReqBody))
	if err != nil {
		t.Fatalf("POST /dispute: %v", err)
	}
	defer respDispute.Body.Close()
	if respDispute.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /dispute status=%d, want 400 for legacy receipt", respDispute.StatusCode)
	}

	// 3. ChainStore.UpdateReceipt must fail for legacy receipt
	if err := srv.chainStore.UpdateReceipt(context.Background(), legacyRec); err == nil {
		t.Error("chainStore.UpdateReceipt on legacy receipt should have failed, got nil")
	}
}

func TestShellServer_MeshAndAgentValidation(t *testing.T) {
	tmpDir := t.TempDir()
	chainDB := filepath.Join(tmpDir, "chain.db")

	if _, err := NewShellServer("", "test-mesh", tmpDir, chainDB, 5*time.Second); err == nil {
		t.Fatal("expected error on empty agent_id")
	}
	if _, err := NewShellServer("agent-1", "", tmpDir, chainDB, 5*time.Second); err == nil {
		t.Fatal("expected error on empty mesh_id")
	}

	srv, err := NewShellServer("agent-1", "test-mesh", tmpDir, chainDB, 5*time.Second)
	if err != nil {
		t.Fatalf("NewShellServer: %v", err)
	}
	defer srv.Close()

	assertions := []envelope.Assertion{{ID: "a1", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true"}}}
	rec, err := srv.executeLocal(context.Background(), assertions)
	if err != nil || rec.MeshID != "test-mesh" || rec.ProtocolVersion != receipt.CurrentProtocolVersion {
		t.Fatalf("executeLocal failed or mismatch: rec=%+v, err=%v", rec, err)
	}

	srvNoMesh := &ShellServer{agentID: "agent-1", shell: &shellWrapper{workspace: tmpDir, evalTimeout: time.Second}}
	if _, err = srvNoMesh.executeLocal(context.Background(), assertions); err == nil {
		t.Fatal("expected error for empty meshID on executeLocal")
	}
}
