package envelope_test

import (
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func makeTestEnvelopeWithCaps(caps []envelope.Capability) *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "a1",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "echo test",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 30,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		Capabilities:   caps,
	}
}

// TestS3_Envelope_CapabilitiesIncludedInSignature verifies that Capabilities
// are bound to the envelope signature and cannot be tampered with.
func TestS3_Envelope_CapabilitiesIncludedInSignature(t *testing.T) {
	signer, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}

	envNet := makeTestEnvelopeWithCaps([]envelope.Capability{envelope.CapabilityNet})
	envExec := makeTestEnvelopeWithCaps([]envelope.Capability{envelope.CapabilityExec})

	hNet, err := envelope.ComputeEnvelopeHash(envNet)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash(envNet): %v", err)
	}
	hExec, err := envelope.ComputeEnvelopeHash(envExec)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash(envExec): %v", err)
	}

	if hNet == hExec {
		t.Errorf("SECURITY VULNERABILITY (S3): ComputeEnvelopeHash returned identical hash %s for different capabilities", hNet)
	}

	env := makeTestEnvelopeWithCaps([]envelope.Capability{envelope.CapabilityNet})
	if err := envelope.SignEnvelope(env, signer); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	// Baseline: must verify
	if err := envelope.VerifyEnvelopeSignature(env, signer.PublicKey()); err != nil {
		t.Fatalf("baseline VerifyEnvelopeSignature failed: %v", err)
	}

	// Adversarial tampering: modify capability from net to exec
	envTampered := *env
	envTampered.Capabilities = []envelope.Capability{envelope.CapabilityExec}

	if err := envelope.VerifyEnvelopeSignature(&envTampered, signer.PublicKey()); err == nil {
		t.Errorf("SECURITY VULNERABILITY (S3): VerifyEnvelopeSignature accepted tampered capabilities (net -> exec)")
	}
}

// TestS3_Envelope_ValidateCapabilities verifies validation rules for capabilities.
func TestS3_Envelope_ValidateCapabilities(t *testing.T) {
	// Empty capability string must be rejected
	envEmptyCap := makeTestEnvelopeWithCaps([]envelope.Capability{""})
	if err := envelope.Validate(envEmptyCap); err == nil {
		t.Errorf("Validate should reject empty capability string")
	}

	// Duplicate capability must be rejected
	envDupCap := makeTestEnvelopeWithCaps([]envelope.Capability{envelope.CapabilityNet, envelope.CapabilityNet})
	if err := envelope.Validate(envDupCap); err == nil {
		t.Errorf("Validate should reject duplicate capabilities")
	}
}
