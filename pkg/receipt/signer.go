package receipt

import (
	"fmt"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// SignReceipt signs a settlement receipt as the executor agent.
// It computes the JCS hash of the receipt (with signatures cleared) and
// signs it with the provided signer. The ExecutorSignedAt is set to now.
func SignReceipt(r *SettlementReceipt, signer signing.Signer) error {
	if r == nil {
		return fmt.Errorf("receipt is nil")
	}
	if signer == nil {
		return fmt.Errorf("signer is nil")
	}

	// Set signed_at before hashing (hash must include the timestamp).
	r.ExecutorSignedAt = time.Now().UTC()

	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return fmt.Errorf("compute receipt hash: %w", err)
	}

	sig, err := signer.Sign([]byte(hash))
	if err != nil {
		return fmt.Errorf("sign receipt: %w", err)
	}

	r.ExecutorSignature = sig
	return nil
}

// VerifyExecutorSignature verifies the executor's signature on a receipt
// using the executor's public key. Returns nil if valid.
func VerifyExecutorSignature(r *SettlementReceipt, executorPublicKey []byte) error {
	if r == nil {
		return fmt.Errorf("receipt is nil")
	}
	if r.ExecutorSignature == "" {
		return fmt.Errorf("missing executor signature")
	}

	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return fmt.Errorf("compute receipt hash: %w", err)
	}

	return signing.Verify(executorPublicKey, []byte(hash), r.ExecutorSignature)
}

// AcceptReceipt records the emitter agent's acceptance of a receipt.
// The executorSignedAt must match the value used by the executor when signing
// the receipt. This ensures both parties sign over the identical content.
func AcceptReceipt(r *SettlementReceipt, emitterSigner signing.Signer, executorSignedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("receipt is nil")
	}
	if emitterSigner == nil {
		return fmt.Errorf("emitter signer is nil")
	}

	// Override ExecutorSignedAt so ComputeReceiptHash signs over the same
	// content the executor signed (same timestamp, same cleared sigs).
	r.ExecutorSignedAt = executorSignedAt

	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return fmt.Errorf("compute receipt hash: %w", err)
	}

	sig, err := emitterSigner.Sign([]byte(hash))
	if err != nil {
		return fmt.Errorf("sign acceptance: %w", err)
	}

	now := time.Now().UTC()
	r.EmitterAcceptance = AcceptanceAccepted
	r.EmitterAcceptanceAt = &now
	r.EmitterSignature = sig
	return nil
}

// DisputeReceipt records the emitter agent's dispute of a receipt.
// The executorSignedAt must match the value used by the executor when signing
// the receipt. reason must be non-empty.
func DisputeReceipt(r *SettlementReceipt, emitterSigner signing.Signer, reason string, executorSignedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("receipt is nil")
	}
	if emitterSigner == nil {
		return fmt.Errorf("emitter signer is nil")
	}
	if reason == "" {
		return fmt.Errorf("dispute reason is required")
	}

	// Override ExecutorSignedAt for deterministic hash.
	r.ExecutorSignedAt = executorSignedAt

	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return fmt.Errorf("compute receipt hash: %w", err)
	}

	sig, err := emitterSigner.Sign([]byte(hash))
	if err != nil {
		return fmt.Errorf("sign dispute: %w", err)
	}

	now := time.Now().UTC()
	r.EmitterAcceptance = AcceptanceDisputed
	r.EmitterAcceptanceAt = &now
	r.EmitterSignature = sig
	r.DisputeReason = reason
	return nil
}

// VerifyEmitterSignature verifies the emitter's acceptance/dispute signature
// on a receipt using the emitter's public key. Returns nil if valid.
func VerifyEmitterSignature(r *SettlementReceipt, emitterPublicKey []byte) error {
	if r == nil {
		return fmt.Errorf("receipt is nil")
	}
	if r.EmitterSignature == "" {
		return fmt.Errorf("missing emitter signature")
	}

	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return fmt.Errorf("compute receipt hash: %w", err)
	}

	return signing.Verify(emitterPublicKey, []byte(hash), r.EmitterSignature)
}
