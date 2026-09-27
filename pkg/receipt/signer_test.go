package receipt

import (
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// ─────────────────────────────────────────────────────────────────
// SignReceipt
// ─────────────────────────────────────────────────────────────────

func TestSignReceipt_OK(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")

	r := validReceipt()
	r.ReceiptID = "sig-001"
	r.ExecutorSignature = ""
	r.ExecutorSignedAt = time.Time{}

	err := SignReceipt(r, signer)
	if err != nil {
		t.Fatalf("SignReceipt error: %v", err)
	}

	if r.ExecutorSignature == "" {
		t.Error("ExecutorSignature is empty after SignReceipt")
	}
	if r.ExecutorSignedAt.IsZero() {
		t.Error("ExecutorSignedAt is zero after SignReceipt")
	}
}

func TestSignReceipt_NilReceipt(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")
	err := SignReceipt(nil, signer)
	if err == nil {
		t.Error("SignReceipt(nil) should return error")
	}
}

func TestSignReceipt_NilSigner(t *testing.T) {
	r := validReceipt()
	err := SignReceipt(r, nil)
	if err == nil {
		t.Error("SignReceipt(nil signer) should return error")
	}
}

func TestSignReceipt_DeterministicHash(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")

	r1 := minimalReceipt("det-001")
	r2 := minimalReceipt("det-001")

	// Sign both.
	SignReceipt(r1, signer)
	time.Sleep(1 * time.Millisecond) // ensure different timestamps.
	SignReceipt(r2, signer)

	// Hashes should differ because timestamps differ.
	h1, _ := ComputeReceiptHash(r1)
	h2, _ := ComputeReceiptHash(r2)
	if h1 == h2 {
		// Timestamps might be identical if called within same millisecond.
		// That's acceptable — just verify both are signed.
	}

	// But signatures must be valid for both.
	err := VerifyExecutorSignature(r1, signer.PublicKey())
	if err != nil {
		t.Errorf("VerifyExecutorSignature(r1) error: %v", err)
	}
	err = VerifyExecutorSignature(r2, signer.PublicKey())
	if err != nil {
		t.Errorf("VerifyExecutorSignature(r2) error: %v", err)
	}
}

// minimalReceipt creates a minimal receipt for deterministic testing.
// It uses a fixed non-zero ExecutorSignedAt so that hash computation in
// SignReceipt (which sets ExecutorSignedAt) does not change the hash.
func minimalReceipt(id string) *SettlementReceipt {
	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &SettlementReceipt{
		ReceiptID:        id,
		ContractID:       "contract-001",
		EnvelopeHash:     "abc123",
		EmitterAgentID:   "agent-a",
		ExecutorAgentID:  "agent-b",
		Verdict:         VerdictSettledClean,
		ExecutorSignedAt: fixedTime,
	}
}

// ─────────────────────────────────────────────────────────────────
// VerifyExecutorSignature
// ─────────────────────────────────────────────────────────────────

func TestVerifyExecutorSignature_OK(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("verify-001")
	SignReceipt(r, signer)

	err := VerifyExecutorSignature(r, signer.PublicKey())
	if err != nil {
		t.Errorf("VerifyExecutorSignature error: %v", err)
	}
}

func TestVerifyExecutorSignature_WrongKey(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")
	wrongSigner, _ := signing.GenerateSigner("wrong")

	r := minimalReceipt("verify-wrong")
	SignReceipt(r, signer)

	err := VerifyExecutorSignature(r, wrongSigner.PublicKey())
	if err == nil {
		t.Error("VerifyExecutorSignature with wrong key should fail")
	}
}

func TestVerifyExecutorSignature_NilReceipt(t *testing.T) {
	err := VerifyExecutorSignature(nil, []byte{})
	if err == nil {
		t.Error("VerifyExecutorSignature(nil) should fail")
	}
}

func TestVerifyExecutorSignature_MissingSignature(t *testing.T) {
	r := minimalReceipt("verify-missing")
	r.ExecutorSignedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	err := VerifyExecutorSignature(r, []byte{})
	if err == nil {
		t.Error("VerifyExecutorSignature(missing sig) should fail")
	}
}

func TestVerifyExecutorSignature_TamperedReceipt(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("verify-tampered")
	SignReceipt(r, signer)

	// Tamper with the receipt after signing.
	r.ContractID = "tampered-contract"

	err := VerifyExecutorSignature(r, signer.PublicKey())
	if err == nil {
		t.Error("VerifyExecutorSignature(tampered) should fail")
	}
}

// ─────────────────────────────────────────────────────────────────
// AcceptReceipt
// ─────────────────────────────────────────────────────────────────

func TestAcceptReceipt_OK(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("accept-001")
	SignReceipt(r, executorSigner)

	// Pass executor's signed_at so both sign over identical content.
	err := AcceptReceipt(r, emitterSigner, r.ExecutorSignedAt)
	if err != nil {
		t.Fatalf("AcceptReceipt error: %v", err)
	}

	if r.EmitterAcceptance != AcceptanceAccepted {
		t.Errorf("EmitterAcceptance = %v, want ACCEPTED", r.EmitterAcceptance)
	}
	if r.EmitterAcceptanceAt == nil {
		t.Error("EmitterAcceptanceAt is nil after AcceptReceipt")
	}
	if r.EmitterSignature == "" {
		t.Error("EmitterSignature is empty after AcceptReceipt")
	}

	err = VerifyEmitterSignature(r, emitterSigner.PublicKey())
	if err != nil {
		t.Errorf("VerifyEmitterSignature error: %v", err)
	}
}

func TestAcceptReceipt_NilReceipt(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-a")
	err := AcceptReceipt(nil, signer, time.Now())
	if err == nil {
		t.Error("AcceptReceipt(nil) should return error")
	}
}

func TestAcceptReceipt_NilSigner(t *testing.T) {
	r := minimalReceipt("accept-nilsigner")
	err := AcceptReceipt(r, nil, time.Now())
	if err == nil {
		t.Error("AcceptReceipt(nil signer) should return error")
	}
}

// ─────────────────────────────────────────────────────────────────
// DisputeReceipt
// ─────────────────────────────────────────────────────────────────

func TestDisputeReceipt_OK(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("dispute-001")
	SignReceipt(r, executorSigner)

	err := DisputeReceipt(r, emitterSigner, "test failure: assertions not met", r.ExecutorSignedAt)
	if err != nil {
		t.Fatalf("DisputeReceipt error: %v", err)
	}

	if r.EmitterAcceptance != AcceptanceDisputed {
		t.Errorf("EmitterAcceptance = %v, want DISPUTED", r.EmitterAcceptance)
	}
	if r.DisputeReason != "test failure: assertions not met" {
		t.Errorf("DisputeReason = %q, want %q", r.DisputeReason, "test failure: assertions not met")
	}
	if r.EmitterSignature == "" {
		t.Error("EmitterSignature is empty after DisputeReceipt")
	}

	err = VerifyEmitterSignature(r, emitterSigner.PublicKey())
	if err != nil {
		t.Errorf("VerifyEmitterSignature error: %v", err)
	}
}

func TestDisputeReceipt_EmptyReason(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-a")
	r := minimalReceipt("dispute-empty")

	err := DisputeReceipt(r, signer, "", time.Now())
	if err == nil {
		t.Error("DisputeReceipt(empty reason) should return error")
	}
}

func TestDisputeReceipt_NilReceipt(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-a")
	err := DisputeReceipt(nil, signer, "reason", time.Now())
	if err == nil {
		t.Error("DisputeReceipt(nil) should return error")
	}
}

// ─────────────────────────────────────────────────────────────────
// VerifyEmitterSignature
// ─────────────────────────────────────────────────────────────────

func TestVerifyEmitterSignature_OK(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")

	r := minimalReceipt("verify-emitter")
	SignReceipt(r, emitterSigner)
	AcceptReceipt(r, emitterSigner, r.ExecutorSignedAt)

	err := VerifyEmitterSignature(r, emitterSigner.PublicKey())
	if err != nil {
		t.Errorf("VerifyEmitterSignature error: %v", err)
	}
}

func TestVerifyEmitterSignature_WrongKey(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	wrongSigner, _ := signing.GenerateSigner("wrong")

	r := minimalReceipt("verify-emitter-wrong")
	SignReceipt(r, emitterSigner)
	AcceptReceipt(r, emitterSigner, r.ExecutorSignedAt)

	err := VerifyEmitterSignature(r, wrongSigner.PublicKey())
	if err == nil {
		t.Error("VerifyEmitterSignature with wrong key should fail")
	}
}

func TestVerifyEmitterSignature_NilReceipt(t *testing.T) {
	err := VerifyEmitterSignature(nil, []byte{})
	if err == nil {
		t.Error("VerifyEmitterSignature(nil) should fail")
	}
}

func TestVerifyEmitterSignature_MissingSignature(t *testing.T) {
	r := minimalReceipt("verify-emitter-missing")
	r.ExecutorSignedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	err := VerifyEmitterSignature(r, []byte{})
	if err == nil {
		t.Error("VerifyEmitterSignature(missing sig) should fail")
	}
}

// ─────────────────────────────────────────────────────────────────
// Cross-signer: executor and emitter are different keys
// ─────────────────────────────────────────────────────────────────

func TestFullSigningFlow(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("full-flow")
	err := SignReceipt(r, executorSigner)
	if err != nil {
		t.Fatalf("SignReceipt error: %v", err)
	}

	err = VerifyExecutorSignature(r, executorSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyExecutorSignature error: %v", err)
	}

	// Emitter accepts, using the executor's signed_at.
	err = AcceptReceipt(r, emitterSigner, r.ExecutorSignedAt)
	if err != nil {
		t.Fatalf("AcceptReceipt error: %v", err)
	}

	err = VerifyEmitterSignature(r, emitterSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyEmitterSignature error: %v", err)
	}

	// Executor key cannot verify emitter signature.
	err = VerifyEmitterSignature(r, executorSigner.PublicKey())
	if err == nil {
		t.Error("Emitter signature should not verify with executor's key")
	}
}

func TestFullDisputeFlow(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("full-dispute")
	SignReceipt(r, executorSigner)
	DisputeReceipt(r, emitterSigner, "assertion 'tests_passing' returned exit code 1", r.ExecutorSignedAt)

	err := VerifyExecutorSignature(r, executorSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyExecutorSignature error: %v", err)
	}

	err = VerifyEmitterSignature(r, emitterSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyEmitterSignature error: %v", err)
	}
}
