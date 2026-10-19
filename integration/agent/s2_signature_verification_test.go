package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func postJSON(t *testing.T, client *http.Client, url string, body any) (*http.Response, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func setupS2Server(t *testing.T) (*Server, *httptest.Server, signing.Signer, signing.Signer) {
	t.Helper()
	tmpDir := t.TempDir()

	emitterSigner, _ := signing.GenerateSigner("agent-emitter")
	executorSigner, _ := signing.GenerateSigner("test-executor")

	cfg := Config{
		AgentID:      "test-executor",
		MeshID:       "mesh-s2-test",
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: filepath.Join(tmpDir, "workspace"),
		EvalTimeout:  5 * time.Second,
		MaxRemed:     1,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_ = srv.RegisterAgentKey("agent-emitter", emitterSigner.PublicKey())

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return srv, ts, emitterSigner, executorSigner
}

func makeS2Envelope(meshID, emitterID, executorID string) *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          meshID,
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  emitterID,
		ExecutorAgentID: executorID,
		TimeoutSeconds:  30,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{ID: "check-exit", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true"}},
		},
		CreatedAt: time.Now().UTC(),
	}
}

// TestS2_Server_RejectsUnsignedEnvelope demonstrates S2: an unsigned envelope is rejected (401).
func TestS2_Server_RejectsUnsignedEnvelope(t *testing.T) {
	_, ts, _, _ := setupS2Server(t)
	client := ts.Client()

	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	envJSON, _ := json.Marshal(env)

	// 1. POST /envelopes without signature -> 401
	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /envelopes unsigned status=%d, want 401", resp.StatusCode)
	}

	// 2. POST /leases without signature -> 401
	if resp, _ := postJSON(t, client, ts.URL+"/leases", map[string]any{"envelope_json": envJSON}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /leases unsigned status=%d, want 401", resp.StatusCode)
	}

	// 3. POST /settle without signature -> 401
	if resp, _ := postJSON(t, client, ts.URL+"/settle", &SettleRequest{EnvelopeJSON: envJSON, LeaseID: "lease-fake"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /settle unsigned status=%d, want 401", resp.StatusCode)
	}
}

// TestS2_Server_RejectsCorruptedEnvelopeSignature demonstrates S2: tampered signature is rejected (401).
func TestS2_Server_RejectsCorruptedEnvelopeSignature(t *testing.T) {
	_, ts, emitterSigner, _ := setupS2Server(t)
	client := ts.Client()

	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	_ = envelope.SignEnvelope(env, emitterSigner)
	env.EmitterSignature = "corrupted-sig-content"
	envJSON, _ := json.Marshal(env)

	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /envelopes with corrupt sig status=%d, want 401", resp.StatusCode)
	}
}

// TestS2_Server_RejectsUnregisteredEmitterKey demonstrates S2: envelope from unknown agent is rejected (401).
func TestS2_Server_RejectsUnregisteredEmitterKey(t *testing.T) {
	_, ts, _, _ := setupS2Server(t)
	client := ts.Client()

	unknownSigner, _ := signing.GenerateSigner("agent-unregistered")
	env := makeS2Envelope("mesh-s2-test", "agent-unregistered", "test-executor")
	_ = envelope.SignEnvelope(env, unknownSigner)
	envJSON, _ := json.Marshal(env)

	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /envelopes with unregistered emitter status=%d, want 401", resp.StatusCode)
	}
}

// TestS2_Server_AcceptsValidSignedEnvelope demonstrates S2: valid envelope signature is accepted (200).
func TestS2_Server_AcceptsValidSignedEnvelope(t *testing.T) {
	_, ts, emitterSigner, _ := setupS2Server(t)
	client := ts.Client()

	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	_ = envelope.SignEnvelope(env, emitterSigner)
	envJSON, _ := json.Marshal(env)

	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes valid status=%d, want 200", resp.StatusCode)
	}
}

// TestS2_Server_RejectsInvalidAcceptanceSignature demonstrates S2: invalid acceptance signature is rejected (401).
func TestS2_Server_RejectsInvalidAcceptanceSignature(t *testing.T) {
	srv, ts, emitterSigner, _ := setupS2Server(t)
	client := ts.Client()

	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	_ = envelope.SignEnvelope(env, emitterSigner)
	envJSON, _ := json.Marshal(env)

	_, body := postJSON(t, client, ts.URL+"/settle", &SettleRequest{EnvelopeJSON: envJSON, LeaseID: "lease-01"})
	var settleResp SettleResponse
	_ = json.Unmarshal(body, &settleResp)
	var rec receipt.SettlementReceipt
	_ = json.Unmarshal(settleResp.ReceiptJSON, &rec)

	// 1. Call POST /accept with forged signature -> 401
	acceptForged := &AcceptRequest{
		ReceiptJSON:         settleResp.ReceiptJSON,
		ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "forged-emitter-signature",
	}
	if resp, _ := postJSON(t, client, ts.URL+"/accept", acceptForged); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /accept with forged sig status=%d, want 401", resp.StatusCode)
	}

	stored, _ := srv.ChainStore().GetReceipt(context.Background(), rec.ReceiptID)
	if stored.EmitterAcceptance != "" {
		t.Errorf("stored receipt was accepted despite forged signature: %s", stored.EmitterAcceptance)
	}

	// 2. Call POST /accept with valid signature -> 200
	recCopy := rec
	_ = receipt.AcceptReceipt(&recCopy, emitterSigner, rec.ExecutorSignedAt)
	acceptValid := &AcceptRequest{
		ReceiptJSON:         settleResp.ReceiptJSON,
		ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    recCopy.EmitterSignature,
	}
	if resp, _ := postJSON(t, client, ts.URL+"/accept", acceptValid); resp.StatusCode != http.StatusOK {
		t.Errorf("POST /accept valid status=%d, want 200", resp.StatusCode)
	}

	storedAfter, _ := srv.ChainStore().GetReceipt(context.Background(), rec.ReceiptID)
	if storedAfter.EmitterAcceptance != receipt.AcceptanceAccepted {
		t.Errorf("stored receipt expected ACCEPTED, got: %s", storedAfter.EmitterAcceptance)
	}
}

// TestS2_Server_RejectsInvalidDisputeSignature demonstrates S2: invalid dispute signature is rejected (401).
func TestS2_Server_RejectsInvalidDisputeSignature(t *testing.T) {
	srv, ts, emitterSigner, _ := setupS2Server(t)
	client := ts.Client()

	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	_ = envelope.SignEnvelope(env, emitterSigner)
	envJSON, _ := json.Marshal(env)

	_, body := postJSON(t, client, ts.URL+"/settle", &SettleRequest{EnvelopeJSON: envJSON, LeaseID: "lease-01"})
	var settleResp SettleResponse
	_ = json.Unmarshal(body, &settleResp)
	var rec receipt.SettlementReceipt
	_ = json.Unmarshal(settleResp.ReceiptJSON, &rec)

	// Call POST /dispute with forged signature -> 401
	disputeForged := &DisputeRequest{
		ReceiptJSON:         settleResp.ReceiptJSON,
		ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    "forged-emitter-signature",
		DisputeReason:       "fraudulent output",
	}
	if resp, _ := postJSON(t, client, ts.URL+"/dispute", disputeForged); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /dispute with forged sig status=%d, want 401", resp.StatusCode)
	}

	stored, _ := srv.ChainStore().GetReceipt(context.Background(), rec.ReceiptID)
	if stored.EmitterAcceptance != "" {
		t.Errorf("stored receipt was disputed despite forged signature: %s", stored.EmitterAcceptance)
	}

	// Call POST /dispute with valid signature -> 200
	recCopy := rec
	_ = receipt.DisputeReceipt(&recCopy, emitterSigner, "real issue", rec.ExecutorSignedAt)
	disputeValid := &DisputeRequest{
		ReceiptJSON:         settleResp.ReceiptJSON,
		ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    recCopy.EmitterSignature,
		DisputeReason:       "real issue",
	}
	if resp, _ := postJSON(t, client, ts.URL+"/dispute", disputeValid); resp.StatusCode != http.StatusOK {
		t.Errorf("POST /dispute valid status=%d, want 200", resp.StatusCode)
	}

	storedAfter, _ := srv.ChainStore().GetReceipt(context.Background(), rec.ReceiptID)
	if storedAfter.EmitterAcceptance != receipt.AcceptanceDisputed {
		t.Errorf("stored receipt expected DISPUTED, got: %s", storedAfter.EmitterAcceptance)
	}
}

// TestS2_Server_WithKeystoreIntegration verifies S2 using pkg/keystore as PublicKeyResolver.
func TestS2_Server_WithKeystoreIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	emitterSigner, _ := signing.GenerateSigner("agent-emitter")

	cfg := Config{
		AgentID:      "test-executor",
		MeshID:       "mesh-s2-test",
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: filepath.Join(tmpDir, "workspace"),
		KeystoreDir:  filepath.Join(tmpDir, "keystore"),
		EvalTimeout:  5 * time.Second,
		MaxRemed:     1,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer with KeystoreDir: %v", err)
	}
	if err := srv.RegisterAgentKey("agent-emitter", emitterSigner.PublicKey()); err != nil {
		t.Fatalf("RegisterAgentKey: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := ts.Client()

	// 1. Signed with registered emitter -> 200
	env := makeS2Envelope("mesh-s2-test", "agent-emitter", "test-executor")
	_ = envelope.SignEnvelope(env, emitterSigner)
	envJSON, _ := json.Marshal(env)

	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes with keystore resolver status=%d, want 200", resp.StatusCode)
	}

	// 2. Signed with unregistered emitter -> 401
	unregisteredSigner, _ := signing.GenerateSigner("agent-unknown")
	envUnk := makeS2Envelope("mesh-s2-test", "agent-unknown", "test-executor")
	_ = envelope.SignEnvelope(envUnk, unregisteredSigner)
	envUnkJSON, _ := json.Marshal(envUnk)

	if resp, _ := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envUnkJSON}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /envelopes with unregistered emitter status=%d, want 401", resp.StatusCode)
	}
}

