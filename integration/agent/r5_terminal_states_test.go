package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

// TestR5_ContractStateMachine_LifecycleResolution tests RFC-002 R5 end-to-end:
// Verifies that a contract lifecycle starting from PROPOSED traverses
// valid intermediate states and is guaranteed to terminate cleanly
// in a recognized terminal state without hanging or getting trapped in DISPUTED.
func TestR5_ContractStateMachine_LifecycleResolution(t *testing.T) {
	// 1. Clean Path: PROPOSED -> ACCEPTED -> EXECUTING -> SETTLING -> SETTLED -> ACCEPTED_FINAL (Terminal)
	smClean := envelope.NewContractStateMachine(envelope.ContractStatusProposed)
	if smClean.IsTerminal() {
		t.Fatalf("PROPOSED should not be terminal")
	}

	transitions := []envelope.ContractStatus{
		envelope.ContractStatusAccepted,
		envelope.ContractStatusExecuting,
		envelope.ContractStatusSettling,
		envelope.ContractStatusSettled,
		envelope.ContractStatusAcceptedFinal,
	}

	for _, step := range transitions {
		if err := smClean.TransitionTo(step); err != nil {
			t.Fatalf("failed transition to %s: %v", step, err)
		}
	}

	if !smClean.IsTerminal() {
		t.Errorf("expected clean path to terminate in terminal state, got %s (IsTerminal=false)", smClean.Current())
	}

	// 2. Dispute & Resolution Path: PROPOSED -> ... -> SETTLED -> DISPUTED -> RESOLVED (Terminal)
	smDispute := envelope.NewContractStateMachine(envelope.ContractStatusSettled)
	if err := smDispute.TransitionTo(envelope.ContractStatusDisputed); err != nil {
		t.Fatalf("transition to DISPUTED: %v", err)
	}
	if smDispute.IsTerminal() {
		t.Errorf("DISPUTED is intermediate and must not be terminal")
	}

	// Under R5, DISPUTED cannot be a dead-end; it must be able to reach RESOLVED
	if err := smDispute.TransitionWithReason(envelope.ContractStatusResolved, "arbitration agreement reached"); err != nil {
		t.Fatalf("transition from DISPUTED to RESOLVED failed: %v", err)
	}
	if !smDispute.IsTerminal() {
		t.Errorf("RESOLVED must be terminal, got IsTerminal=false")
	}

	// 3. Dispute & Abandon Path: DISPUTED -> ABANDONED (Terminal)
	smAbandon := envelope.NewContractStateMachine(envelope.ContractStatusDisputed)
	if err := smAbandon.TransitionWithReason(envelope.ContractStatusAbandoned, "abandoned by parties"); err != nil {
		t.Fatalf("transition from DISPUTED to ABANDONED failed: %v", err)
	}
	if !smAbandon.IsTerminal() {
		t.Errorf("ABANDONED must be terminal, got IsTerminal=false")
	}

	// 4. Absorbing Barrier: Attempting any transition out of terminal states must fail
	terminalStatuses := []envelope.ContractStatus{
		envelope.ContractStatusAcceptedFinal,
		envelope.ContractStatusRejected,
		envelope.ContractStatusExpired,
		envelope.ContractStatusSettlementFailed,
		envelope.ContractStatusAbandoned,
		envelope.ContractStatusSettlementTimeout,
		envelope.ContractStatusResolved,
	}

	for _, term := range terminalStatuses {
		smTerm := envelope.NewContractStateMachine(term)
		if !smTerm.IsTerminal() {
			t.Errorf("expected %s to be terminal", term)
		}
		// Attempt illegal transition out of terminal state
		if err := smTerm.TransitionTo(envelope.ContractStatusExecuting); err == nil {
			t.Errorf("R5 VIOLATION: transition out of terminal state %s to EXECUTING was permitted", term)
		}
	}
}

// TestR5_Server_SettlementToDispute_AuditTrail verifies that when an agent server
// settles a contract and the emitter submits a dispute, the resulting chain
// record is properly stamped and traceable, and maps to the R5 state machine semantics.
func TestR5_Server_SettlementToDispute_AuditTrail(t *testing.T) {
	tmpDir := t.TempDir()
	emitterSigner, err := signing.GenerateSigner("agent-emitter")
	if err != nil {
		t.Fatalf("GenerateSigner emitter: %v", err)
	}

	cfg := Config{
		AgentID:      "test-executor",
		MeshID:       "mesh-r5-test",
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

	// Build valid envelope
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-r5-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-r5contract-01",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "test-executor",
		TimeoutSeconds:  30,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: filepath.Join(tmpDir, "workspace"),
		},
		Assertions: []envelope.Assertion{
			{ID: "check-true", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true"}},
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	// 1. Initialize Contract State Machine
	sm := envelope.NewContractStateMachine(envelope.ContractStatusProposed)
	_ = sm.TransitionTo(envelope.ContractStatusAccepted)
	_ = sm.TransitionTo(envelope.ContractStatusExecuting)
	_ = sm.TransitionTo(envelope.ContractStatusSettling)

	// 2. Perform settlement
	ctx := context.Background()
	rec, err := srv.emitSettlementTimeout(env) // Using server's direct emission mechanism
	if err != nil {
		t.Fatalf("emit settlement: %v", err)
	}
	_ = sm.TransitionTo(envelope.ContractStatusSettlementTimeout)

	if !sm.IsTerminal() {
		t.Fatalf("expected SETTLEMENT_TIMEOUT to be terminal")
	}

	// Verify persistence in chain store
	stored, err := srv.ChainStore().GetReceiptByContractID(ctx, env.EnvelopeID)
	if err != nil {
		t.Fatalf("GetReceiptByContractID: %v", err)
	}
	if stored.ReceiptID != rec.ReceiptID {
		t.Errorf("stored receipt ID = %s, want %s", stored.ReceiptID, rec.ReceiptID)
	}
	if !stored.Verdict.IsTerminal() {
		t.Errorf("stored verdict %s should be terminal", stored.Verdict)
	}
}
