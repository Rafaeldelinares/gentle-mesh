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

// TestA1_Envelope_ZeroConfigMinimalProfile verifies RFC-002 A1:
// An envelope for a low-risk task without capability declarations (nil or empty)
// validates successfully under the minimal profile, and adding undeclared capabilities
// invalidates the signature.
func TestA1_Envelope_ZeroConfigMinimalProfile(t *testing.T) {
	signer, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}

	// 1. Nil capabilities (minimal profile) validates cleanly
	envNil := makeTestEnvelopeWithCaps(nil)
	if err := envelope.Validate(envNil); err != nil {
		t.Errorf("Validate failed on minimal profile envelope with nil capabilities: %v", err)
	}

	// 2. Empty capabilities slice also validates cleanly
	envEmpty := makeTestEnvelopeWithCaps([]envelope.Capability{})
	if err := envelope.Validate(envEmpty); err != nil {
		t.Errorf("Validate failed on minimal profile envelope with empty capabilities: %v", err)
	}

	// 3. Minimal profile hash differs from capability-bearing envelopes
	envNet := makeTestEnvelopeWithCaps([]envelope.Capability{envelope.CapabilityNet})
	hMin, err := envelope.ComputeEnvelopeHash(envNil)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash(envNil): %v", err)
	}
	hNet, err := envelope.ComputeEnvelopeHash(envNet)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash(envNet): %v", err)
	}
	if hMin == hNet {
		t.Errorf("minimal profile hash collides with capability-bearing hash")
	}

	// 4. Signing minimal profile and then tampering by adding capabilities fails verification
	if err := envelope.SignEnvelope(envNil, signer); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	if err := envelope.VerifyEnvelopeSignature(envNil, signer.PublicKey()); err != nil {
		t.Fatalf("VerifyEnvelopeSignature on legitimate minimal profile envelope failed: %v", err)
	}

	tampered := *envNil
	tampered.Capabilities = []envelope.Capability{envelope.CapabilityExec}
	if err := envelope.VerifyEnvelopeSignature(&tampered, signer.PublicKey()); err == nil {
		t.Errorf("SECURITY VULNERABILITY (A1): VerifyEnvelopeSignature accepted tampered capabilities added to minimal profile envelope")
	}
}
