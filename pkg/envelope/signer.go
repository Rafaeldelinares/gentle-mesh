package envelope

import (
	"errors"
	"fmt"

	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// SignEnvelope signs a cognitive task envelope with the emitter agent's signer.
// The envelope must pass Validate before signing.
// EnvelopeHash is computed and set. EmitterSignature is computed over the JCS
// canonical representation of the envelope with EmitterSignature cleared.
func SignEnvelope(env *CognitiveTaskEnvelope, signer signing.Signer) error {
	if env == nil {
		return errors.New("envelope: nil")
	}
	if signer == nil {
		return errors.New("signer: nil")
	}
	if err := Validate(env); err != nil {
		return fmt.Errorf("cannot sign invalid envelope: %w", err)
	}

	hash, err := ComputeEnvelopeHash(env)
	if err != nil {
		return fmt.Errorf("compute envelope hash: %w", err)
	}
	env.EnvelopeHash = hash

	signable := *env
	signable.EmitterSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return fmt.Errorf("jcs marshal: %w", err)
	}

	sig, err := signer.Sign(canonical)
	if err != nil {
		return fmt.Errorf("sign envelope: %w", err)
	}
	env.EmitterSignature = sig
	return nil
}

// VerifyEnvelopeSignature verifies the emitter's signature on the envelope
// using the emitter's public key.
func VerifyEnvelopeSignature(env *CognitiveTaskEnvelope, emitterPublicKey []byte) error {
	if env == nil {
		return errors.New("envelope: nil")
	}
	if env.EmitterSignature == "" {
		return errors.New("missing emitter signature")
	}
	if err := Validate(env); err != nil {
		return fmt.Errorf("invalid envelope: %w", err)
	}

	expectedHash, err := ComputeEnvelopeHash(env)
	if err != nil {
		return fmt.Errorf("compute envelope hash: %w", err)
	}
	if env.EnvelopeHash != expectedHash {
		return fmt.Errorf("envelope hash mismatch: got %s, want %s", env.EnvelopeHash, expectedHash)
	}

	signable := *env
	signable.EmitterSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return fmt.Errorf("jcs marshal: %w", err)
	}

	return signing.Verify(emitterPublicKey, canonical, env.EmitterSignature)
}

// ComputeLeaseHash computes the JCS canonical SHA-256 hash of the lease
// with ExecutorSignature cleared. The lease must pass ValidateLease before hashing.
func ComputeLeaseHash(lease *Lease) (string, error) {
	if lease == nil {
		return "", errors.New("lease: nil")
	}
	if err := ValidateLease(lease); err != nil {
		return "", fmt.Errorf("cannot hash invalid lease: %w", err)
	}

	signable := *lease
	signable.ExecutorSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return "", fmt.Errorf("jcs marshal: %w", err)
	}
	return jcs.HashHex(canonical)
}

// SignLease signs a lease with the executor agent's signer.
// The lease must pass ValidateLease before signing.
// ExecutorSignature is computed over the JCS canonical representation of the lease
// with ExecutorSignature cleared.
func SignLease(lease *Lease, signer signing.Signer) error {
	if lease == nil {
		return errors.New("lease: nil")
	}
	if signer == nil {
		return errors.New("signer: nil")
	}
	if err := ValidateLease(lease); err != nil {
		return fmt.Errorf("cannot sign invalid lease: %w", err)
	}

	signable := *lease
	signable.ExecutorSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return fmt.Errorf("jcs marshal: %w", err)
	}

	sig, err := signer.Sign(canonical)
	if err != nil {
		return fmt.Errorf("sign lease: %w", err)
	}
	lease.ExecutorSignature = sig
	return nil
}

// VerifyLeaseSignature verifies the executor's signature on the lease
// using the executor's public key.
func VerifyLeaseSignature(lease *Lease, executorPublicKey []byte) error {
	if lease == nil {
		return errors.New("lease: nil")
	}
	if lease.ExecutorSignature == "" {
		return errors.New("missing executor signature")
	}
	if err := ValidateLease(lease); err != nil {
		return fmt.Errorf("invalid lease: %w", err)
	}

	signable := *lease
	signable.ExecutorSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return fmt.Errorf("jcs marshal: %w", err)
	}

	return signing.Verify(executorPublicKey, canonical, lease.ExecutorSignature)
}
