package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// setupZeroConfigServer creates an executor server with ZERO capability configuration.
// No granted capabilities are configured (GrantedCapabilities: nil).
func setupZeroConfigServer(t *testing.T) (*Server, *httptest.Server, signing.Signer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	emitterSigner, err := signing.GenerateSigner("agent-emitter")
	if err != nil {
		t.Fatalf("GenerateSigner emitter: %v", err)
	}

	// Zero-configuration: GrantedCapabilities is nil (no capabilities declared by host)
	cfg := Config{
		AgentID:             "zero-config-executor",
		MeshID:              "mesh-a1-test",
		Role:                RoleExecutor,
		ChainDBPath:         filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir:        workspaceDir,
		EvalTimeout:         5 * time.Second,
		MaxRemed:            1,
		GrantedCapabilities: nil, // RFC-002 A1: zero-configuration default
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

	return srv, ts, emitterSigner, workspaceDir
}

// TestA1_Server_ZeroConfigLowRiskTaskSettlesSuccessfully verifies RFC-002 A1:
// "Un emisor puede encargar una tarea de bajo riesgo sin declarar capacidades ni conocer nada del ejecutor."
// Flow:
// 1. Emitter creates envelope with no capability declarations (nil/minimal profile).
// 2. Zero-config executor accepts lease without prior capability setup.
// 3. Executor settles low-risk assertions to SETTLED_CLEAN.
// 4. Receipt is chained, signed, counter-signed (S7), and verified through /verify and /verify-chain.
func TestA1_Server_ZeroConfigLowRiskTaskSettlesSuccessfully(t *testing.T) {
	srv, ts, emitterSigner, workspaceDir := setupZeroConfigServer(t)
	client := ts.Client()

	// 1. Setup low-risk workspace artifact: a deterministic file inside workspace.
	testFile := filepath.Join(workspaceDir, "output.txt")
	testContent := []byte("deterministic safe computation")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	h := sha256.Sum256(testContent)
	expectedHash := hex.EncodeToString(h[:])

	// Initialize git repo in workspace so git clean check also passes
	cmdInit := exec.Command("git", "init")
	cmdInit.Dir = workspaceDir
	_ = cmdInit.Run()
	cmdConfigEmail := exec.Command("git", "config", "user.email", "test@example.com")
	cmdConfigEmail.Dir = workspaceDir
	_ = cmdConfigEmail.Run()
	cmdConfigName := exec.Command("git", "config", "user.name", "Test User")
	cmdConfigName.Dir = workspaceDir
	_ = cmdConfigName.Run()
	cmdAdd := exec.Command("git", "add", "output.txt")
	cmdAdd.Dir = workspaceDir
	_ = cmdAdd.Run()
	cmdCommit := exec.Command("git", "commit", "-m", "initial commit")
	cmdCommit.Dir = workspaceDir
	_ = cmdCommit.Run()

	// 2. Build envelope with NO capability declarations (low-risk minimal profile).
	// Emitter knows nothing about executor's configuration and declares no capabilities.
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-a1-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-a1test000001",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "zero-config-executor",
		Territory: envelope.Territory{
			Repository:    "github.com/org/lowrisk-repo",
			Branch:        "main",
			WorkspacePath: workspaceDir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "verify-hash",
				Type: envelope.AssertionFileHashEquals,
				Params: envelope.AssertionParams{
					FilePath:       "output.txt",
					ExpectedSHA256: expectedHash,
				},
			},
			{
				ID:   "verify-clean",
				Type: envelope.AssertionGitCleanWorktree,
			},
		},
		TimeoutSeconds:  30,
		MaxRemediations: 0,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
		Capabilities:    nil, // A1: no capability declarations
	}

	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// 3. Submit envelope to POST /envelopes -> lease must be accepted without configuration.
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200. Body: %s", resp.StatusCode, string(body))
	}
	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}
	if !envResp.Accepted {
		t.Fatalf("A1 VIOLATION: zero-config executor rejected low-risk task without capabilities: %s", envResp.Error)
	}
	if envResp.LeaseID == "" {
		t.Fatal("expected non-empty lease ID")
	}

	// Verify lease state in executor
	lease, ok := srv.GetLease(envResp.LeaseID)
	if !ok {
		t.Fatalf("lease %s not found in executor store", envResp.LeaseID)
	}
	if !lease.Accepted {
		t.Fatalf("lease accepted=false, want true. RejectionReason: %s", lease.RejectionReason)
	}

	// 4. Settle task via POST /settle -> must execute low-risk assertions cleanly.
	settleReq := &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      envResp.LeaseID,
	}
	respSettle, bodySettle := postJSON(t, client, ts.URL+"/settle", settleReq)
	if respSettle.StatusCode != http.StatusOK {
		t.Fatalf("POST /settle status=%d, want 200. Body: %s", respSettle.StatusCode, string(bodySettle))
	}

	var settleResp SettleResponse
	if err := json.Unmarshal(bodySettle, &settleResp); err != nil {
		t.Fatalf("unmarshal SettleResponse: %v", err)
	}
	if settleResp.ReceiptID == "" {
		t.Fatal("expected non-empty receipt ID from settle")
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(settleResp.ReceiptJSON, &rec); err != nil {
		t.Fatalf("unmarshal SettlementReceipt: %v", err)
	}

	if rec.Verdict != receipt.VerdictSettledClean {
		t.Fatalf("expected verdict %s, got %s", receipt.VerdictSettledClean, rec.Verdict)
	}
	if len(rec.Assertions) != 2 {
		t.Fatalf("expected 2 assertion results, got %d", len(rec.Assertions))
	}
	for _, a := range rec.Assertions {
		if a.Result != "PASS" {
			t.Errorf("assertion %s failed: result=%s, message=%s", a.AssertionID, a.Result, a.Message)
		}
	}

	// 5. Verify receipt via POST /verify.
	verifyReq := &VerifyRequest{ReceiptJSON: settleResp.ReceiptJSON}
	respVerify, bodyVerify := postJSON(t, client, ts.URL+"/verify", verifyReq)
	if respVerify.StatusCode != http.StatusOK {
		t.Fatalf("POST /verify status=%d, want 200. Body: %s", respVerify.StatusCode, string(bodyVerify))
	}
	var verifyResp VerifyResponse
	if err := json.Unmarshal(bodyVerify, &verifyResp); err != nil {
		t.Fatalf("unmarshal VerifyResponse: %v", err)
	}
	if !verifyResp.ExecutorSigOK {
		t.Errorf("executor signature invalid on settled receipt: %v", verifyResp.Errors)
	}
	if !verifyResp.ChainValid {
		t.Errorf("chain invalid on settled receipt: %v", verifyResp.ChainErrors)
	}

	// 6. Emitter accepts receipt (S7 counter-signature) via POST /accept.
	recCopy := rec
	if err := receipt.AcceptReceipt(&recCopy, emitterSigner, rec.ExecutorSignedAt); err != nil {
		t.Fatalf("AcceptReceipt: %v", err)
	}

	acceptReq := &AcceptRequest{
		ReceiptJSON:         settleResp.ReceiptJSON,
		ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339Nano),
		EmitterSignature:    recCopy.EmitterSignature,
	}
	respAccept, bodyAccept := postJSON(t, client, ts.URL+"/accept", acceptReq)
	if respAccept.StatusCode != http.StatusOK {
		t.Fatalf("POST /accept status=%d, want 200. Body: %s", respAccept.StatusCode, string(bodyAccept))
	}
	var acceptResp AcceptResponse
	if err := json.Unmarshal(bodyAccept, &acceptResp); err != nil {
		t.Fatalf("unmarshal AcceptResponse: %v", err)
	}
	if !acceptResp.Accepted {
		t.Fatalf("expected accepted=true, got false")
	}

	// 7. Verify full chain via POST /verify-chain.
	verifyChainReq := &VerifyChainRequest{
		EmitterID:   "agent-emitter",
		ExecutorID:  "zero-config-executor",
		ExecutorKey: hex.EncodeToString(srv.Signer().PublicKey()),
		EmitterKey:  hex.EncodeToString(emitterSigner.PublicKey()),
	}
	respChain, bodyChain := postJSON(t, client, ts.URL+"/verify-chain", verifyChainReq)
	if respChain.StatusCode != http.StatusOK {
		t.Fatalf("POST /verify-chain status=%d, want 200. Body: %s", respChain.StatusCode, string(bodyChain))
	}
	var chainResp VerifyChainResponse
	if err := json.Unmarshal(bodyChain, &chainResp); err != nil {
		t.Fatalf("unmarshal VerifyChainResponse: %v", err)
	}
	if !chainResp.AllValid {
		t.Fatalf("chain verification failed: %+v", chainResp.Results)
	}
	if len(chainResp.Results) != 1 {
		t.Fatalf("expected 1 chain result, got %d", len(chainResp.Results))
	}
	if !chainResp.Results[0].ExecutorSigValid || !chainResp.Results[0].EmitterSigValid || !chainResp.Results[0].PreviousHashValid {
		t.Fatalf("chain result signatures invalid: %+v", chainResp.Results[0])
	}
}

// TestA1_Server_ZeroConfig_RejectsHighRiskTasksWithoutExplicitCapabilities verifies
// that zero-config executors enforce "Seguro por defecto" (A1):
// If an envelope requests non-minimal capabilities (e.g. net or exec), the zero-config
// executor refuses the lease with REJECTED_CAPABILITY and denies settlement with HTTP 403.
func TestA1_Server_ZeroConfig_RejectsHighRiskTasksWithoutExplicitCapabilities(t *testing.T) {
	_, ts, emitterSigner, workspaceDir := setupZeroConfigServer(t)
	client := ts.Client()

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-a1-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-a1test000002",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "zero-config-executor",
		Territory: envelope.Territory{
			Repository:    "github.com/org/highrisk-repo",
			Branch:        "main",
			WorkspacePath: workspaceDir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "clean",
				Type: envelope.AssertionGitCleanWorktree,
			},
		},
		TimeoutSeconds: 30,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		Capabilities:   []envelope.Capability{envelope.CapabilityNet}, // high-risk capability requested
	}

	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)

	// 1. Lease attempt must fail with REJECTED_CAPABILITY
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200", resp.StatusCode)
	}
	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}
	if envResp.Accepted {
		t.Errorf("A1 SECURITY DEFECT: zero-config executor accepted high-risk task requesting 'net'")
	}
	if !strings.Contains(envResp.Error, envelope.RejectionReasonRejectedCapability) {
		t.Errorf("expected error to contain %q, got %q", envelope.RejectionReasonRejectedCapability, envResp.Error)
	}

	// 2. Direct settlement attempt must fail with 403 Forbidden
	respSettle, bodySettle := postJSON(t, client, ts.URL+"/settle", &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      "fake-lease",
	})
	if respSettle.StatusCode != http.StatusForbidden {
		t.Errorf("POST /settle status=%d, want 403 Forbidden. Body: %s", respSettle.StatusCode, string(bodySettle))
	}
}
