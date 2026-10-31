package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

func setupCapabilityServer(t *testing.T, grantedCaps []envelope.Capability) (*Server, *httptest.Server, signing.Signer, signing.Signer) {
	t.Helper()
	tmpDir := t.TempDir()

	emitterSigner, _ := signing.GenerateSigner("agent-emitter")
	executorSigner, _ := signing.GenerateSigner("test-executor")

	cfg := Config{
		AgentID:             "test-executor",
		MeshID:              "mesh-s3-test",
		Role:                RoleExecutor,
		ChainDBPath:         filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir:        filepath.Join(tmpDir, "workspace"),
		EvalTimeout:         5 * time.Second,
		MaxRemed:            1,
		GrantedCapabilities: grantedCaps,
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

func makeEnvelopeWithCaps(meshID, emitterID, executorID string, caps []envelope.Capability) *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          meshID,
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  emitterID,
		ExecutorAgentID: executorID,
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/srv/workspace/repo",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "clean",
				Type: envelope.AssertionGitCleanWorktree,
			},
		},
		TimeoutSeconds: 60,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		Capabilities:   caps,
	}
}

// TestS3_Server_DenyByDefault_RejectsUngrantedCapability verifies that an executor
// with default (empty) configuration rejects an envelope requesting non-minimal capabilities
// with accepted: false and REJECTED_CAPABILITY (RFC-002 S3).
func TestS3_Server_DenyByDefault_RejectsUngrantedCapability(t *testing.T) {
	// Server with NO granted capabilities (deny-by-default)
	_, ts, emitterSigner, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{envelope.CapabilityNet})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200", resp.StatusCode)
	}

	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}

	if envResp.Accepted {
		t.Errorf("SECURITY VULNERABILITY (RFC-002 S3): default executor accepted envelope requesting ungranted capability %q", envelope.CapabilityNet)
	}
	if !strings.Contains(envResp.Error, envelope.RejectionReasonRejectedCapability) {
		t.Errorf("expected error to contain %q, got %q", envelope.RejectionReasonRejectedCapability, envResp.Error)
	}
}

// TestS3_Server_Lease_RejectsUngrantedCapability verifies that POST /leases
// denies contracts that request ungranted capabilities under deny-by-default (RFC-002 S3).
func TestS3_Server_Lease_RejectsUngrantedCapability(t *testing.T) {
	_, ts, emitterSigner, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{envelope.CapabilityExec})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/leases", struct {
		EnvelopeJSON []byte `json:"envelope_json"`
	}{EnvelopeJSON: envJSON})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /leases status=%d, want 200", resp.StatusCode)
	}

	var lease envelope.Lease
	if err := json.Unmarshal(body, &lease); err != nil {
		t.Fatalf("unmarshal lease: %v", err)
	}

	if lease.Accepted {
		t.Errorf("SECURITY VULNERABILITY (RFC-002 S3): POST /leases issued accepted lease for ungranted capability %q", envelope.CapabilityExec)
	}
	if !strings.Contains(lease.RejectionReason, envelope.RejectionReasonRejectedCapability) {
		t.Errorf("expected rejection reason to contain %q, got %q", envelope.RejectionReasonRejectedCapability, lease.RejectionReason)
	}
}

// TestS3_Server_MinimalProfile_AllowsEnvelopeWithoutCapabilities verifies that an envelope
// without declared capabilities runs under the minimal profile and is accepted by default
// (RFC-002 S3 and A1: low-risk tasks without capability declarations).
func TestS3_Server_MinimalProfile_AllowsEnvelopeWithoutCapabilities(t *testing.T) {
	_, ts, emitterSigner, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	// No capabilities declared (minimal profile)
	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", nil)
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200", resp.StatusCode)
	}

	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}

	if !envResp.Accepted {
		t.Errorf("RFC-002 A1/S3: minimal profile envelope without capabilities should be accepted, got error: %s", envResp.Error)
	}
}

// TestS3_Server_GrantedCapability_Accepted verifies that an executor with explicitly
// granted capabilities accepts an envelope requesting only granted capabilities.
func TestS3_Server_GrantedCapability_Accepted(t *testing.T) {
	// Executor explicitly grants 'exec'
	_, ts, emitterSigner, _ := setupCapabilityServer(t, []envelope.Capability{envelope.CapabilityExec})
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{envelope.CapabilityExec})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200", resp.StatusCode)
	}

	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}

	if !envResp.Accepted {
		t.Errorf("granted capability %q should be accepted, got error: %s", envelope.CapabilityExec, envResp.Error)
	}
}

// TestS3_Server_PartialGrant_RejectsMissingCapability verifies that if an envelope requests
// multiple capabilities and any of them is not granted, the entire contract is rejected.
func TestS3_Server_PartialGrant_RejectsMissingCapability(t *testing.T) {
	// Executor grants 'exec' but NOT 'net'
	_, ts, emitterSigner, _ := setupCapabilityServer(t, []envelope.Capability{envelope.CapabilityExec})
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{
		envelope.CapabilityExec,
		envelope.CapabilityNet,
	})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /envelopes status=%d, want 200", resp.StatusCode)
	}

	var envResp EnvelopeResponse
	if err := json.Unmarshal(body, &envResp); err != nil {
		t.Fatalf("unmarshal EnvelopeResponse: %v", err)
	}

	if envResp.Accepted {
		t.Errorf("SECURITY VULNERABILITY (RFC-002 S3): envelope with partially ungranted capabilities was accepted")
	}
	if !strings.Contains(envResp.Error, envelope.RejectionReasonRejectedCapability) || !strings.Contains(envResp.Error, "net") {
		t.Errorf("expected rejection to mention REJECTED_CAPABILITY and 'net', got: %s", envResp.Error)
	}
}

// TestS3_Server_Settle_RejectedOnUngrantedCapability verifies that POST /settle
// refuses to execute an envelope that declares ungranted capabilities (fail-closed, 403).
func TestS3_Server_Settle_RejectedOnUngrantedCapability(t *testing.T) {
	_, ts, emitterSigner, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{envelope.CapabilityNet})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	resp, body := postJSON(t, client, ts.URL+"/settle", &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      "lease-fake",
	})

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /settle status=%d, want 403 Forbidden for ungranted capability. Body: %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), envelope.RejectionReasonRejectedCapability) {
		t.Errorf("expected 403 body to contain %q, got %q", envelope.RejectionReasonRejectedCapability, string(body))
	}
}

// TestS3_Server_Settle_RejectedOnUnacceptedLease verifies that POST /settle
// refuses to settle against a lease that was not accepted (412 Precondition Failed).
func TestS3_Server_Settle_RejectedOnUnacceptedLease(t *testing.T) {
	srv, ts, emitterSigner, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	env := makeEnvelopeWithCaps("mesh-s3-test", "agent-emitter", "test-executor", []envelope.Capability{envelope.CapabilityNet})
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	envJSON, _ := json.Marshal(env)
	// Create an unaccepted lease
	rejectedLease := &envelope.Lease{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-s3-test",
		LeaseID:         "lease-rejected-01",
		EnvelopeID:      env.EnvelopeID,
		ExecutorAgentID: "test-executor",
		Accepted:        false,
		RejectionReason: envelope.RejectionReasonRejectedCapability,
		ExpiresAt:       time.Now().Add(1 * time.Hour),
	}
	srv.leasesMu.Lock()
	srv.leases[rejectedLease.LeaseID] = rejectedLease
	srv.leasesMu.Unlock()

	resp, body := postJSON(t, client, ts.URL+"/settle", &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      rejectedLease.LeaseID,
	})

	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("POST /settle on unaccepted lease status=%d, want 403 or 412. Body: %s", resp.StatusCode, string(body))
	}
}

// TestS3_Preflight_UngrantedCapability_Rejected verifies that POST /preflight
// evaluates requested capabilities and rejects ungranted ones with Passed: false and REJECTED_CAPABILITY.
func TestS3_Preflight_UngrantedCapability_Rejected(t *testing.T) {
	_, ts, _, _ := setupCapabilityServer(t, nil)
	client := ts.Client()

	req := &PreflightRequest{
		Capabilities: []envelope.Capability{envelope.CapabilityNet},
	}
	reqBytes, _ := json.Marshal(req)

	resp, err := client.Post(ts.URL+"/preflight", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /preflight: %v", err)
	}
	defer resp.Body.Close()

	var preflightResp PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&preflightResp); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}

	if preflightResp.Passed {
		t.Errorf("SECURITY VULNERABILITY (RFC-002 S3): preflight passed for ungranted capability")
	}
	if !strings.Contains(preflightResp.RejectionReason, envelope.RejectionReasonRejectedCapability) {
		t.Errorf("expected RejectionReason to contain %q, got %q", envelope.RejectionReasonRejectedCapability, preflightResp.RejectionReason)
	}
}
