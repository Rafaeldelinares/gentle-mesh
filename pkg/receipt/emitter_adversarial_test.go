package receipt

import (
	"testing"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

// TestS7_EmitterSignature_AcceptVsDispute_MustNotHashEqual tests that AcceptReceipt
// and DisputeReceipt do not sign the exact same hash (Issue #48).
func TestS7_EmitterSignature_AcceptVsDispute_MustNotHashEqual(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	base := minimalReceipt("r-s7-diff")
	if err := SignReceipt(base, executorSigner); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}

	rAccept := *base
	if err := AcceptReceipt(&rAccept, emitterSigner, base.ExecutorSignedAt); err != nil {
		t.Fatalf("AcceptReceipt: %v", err)
	}

	rDispute := *base
	if err := DisputeReceipt(&rDispute, emitterSigner, "assertions failed", base.ExecutorSignedAt); err != nil {
		t.Fatalf("DisputeReceipt: %v", err)
	}

	if rAccept.EmitterSignature == rDispute.EmitterSignature {
		t.Errorf("SECURITY VULNERABILITY (Issue #48): AcceptReceipt and DisputeReceipt produced IDENTICAL EmitterSignature (%s)",
			rAccept.EmitterSignature)
	}
}

// TestS7_EmitterSignature_TamperAcceptance_MustBeRejected tests that tampering
// with EmitterAcceptance (e.g. flipping ACCEPTED to DISPUTED) invalidates the signature.
func TestS7_EmitterSignature_TamperAcceptance_MustBeRejected(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("r-s7-tamper-acc")
	if err := SignReceipt(r, executorSigner); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}

	if err := AcceptReceipt(r, emitterSigner, r.ExecutorSignedAt); err != nil {
		t.Fatalf("AcceptReceipt: %v", err)
	}

	// Baseline: must verify
	if err := VerifyEmitterSignature(r, emitterSigner.PublicKey()); err != nil {
		t.Fatalf("baseline VerifyEmitterSignature failed: %v", err)
	}

	// Adversarial tampering: flip to DISPUTED
	r.EmitterAcceptance = AcceptanceDisputed
	r.DisputeReason = "forged dispute"

	if err := VerifyEmitterSignature(r, emitterSigner.PublicKey()); err == nil {
		t.Errorf("SECURITY VULNERABILITY (Issue #48): VerifyEmitterSignature accepted tampered EmitterAcceptance (flipped to DISPUTED)")
	}
}

// TestS7_EmitterSignature_TamperDisputeReason_MustBeRejected tests that tampering
// with DisputeReason invalidates the signature.
func TestS7_EmitterSignature_TamperDisputeReason_MustBeRejected(t *testing.T) {
	emitterSigner, _ := signing.GenerateSigner("agent-a")
	executorSigner, _ := signing.GenerateSigner("agent-b")

	r := minimalReceipt("r-s7-tamper-reason")
	if err := SignReceipt(r, executorSigner); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}

	if err := DisputeReceipt(r, emitterSigner, "original reason", r.ExecutorSignedAt); err != nil {
		t.Fatalf("DisputeReceipt: %v", err)
	}

	// Baseline: must verify
	if err := VerifyEmitterSignature(r, emitterSigner.PublicKey()); err != nil {
		t.Fatalf("baseline VerifyEmitterSignature failed: %v", err)
	}

	// Adversarial tampering: modify dispute reason
	r.DisputeReason = "altered reason"

	if err := VerifyEmitterSignature(r, emitterSigner.PublicKey()); err == nil {
		t.Errorf("SECURITY VULNERABILITY (Issue #48): VerifyEmitterSignature accepted tampered DisputeReason")
	}

	// Adversarial tampering: flip to ACCEPTED
	r.EmitterAcceptance = AcceptanceAccepted
	r.DisputeReason = ""

	if err := VerifyEmitterSignature(r, emitterSigner.PublicKey()); err == nil {
		t.Errorf("SECURITY VULNERABILITY (Issue #48): VerifyEmitterSignature accepted tampered EmitterAcceptance (flipped to ACCEPTED)")
	}
}
