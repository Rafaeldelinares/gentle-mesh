package envelope_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
)

func TestEnvelope_S9_ProtocolVersion_StrictV2(t *testing.T) {
	validEnv := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "018e3a2b-0001-7000-8000-000000000001",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/work",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "check-exit",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "echo ok",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 60,
		CreatedAt:      time.Now().UTC(),
	}

	// Baseline: version "2" passes
	if err := envelope.Validate(validEnv); err != nil {
		t.Fatalf("expected valid envelope to pass, got: %v", err)
	}

	// S9: Invalid versions must be rejected with ErrUnknownProtocolVersion
	invalidVersions := []string{
		"1",
		"1.0",
		"2.0",
		"3",
		"v2",
		"",
		" ",
	}

	for _, v := range invalidVersions {
		t.Run("version_"+v, func(t *testing.T) {
			env := *validEnv
			env.ProtocolVersion = v
			err := envelope.Validate(&env)
			if err == nil {
				t.Fatalf("expected error for version %q, got nil", v)
			}
			if !errors.Is(err, envelope.ErrUnknownProtocolVersion) {
				t.Fatalf("expected ErrUnknownProtocolVersion for version %q, got: %v", v, err)
			}
		})
	}
}

func TestEnvelope_S9_MeshID_Required(t *testing.T) {
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-prod",
		EnvelopeID:      "018e3a2b-0001-7000-8000-000000000001",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/work",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "check-exit",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "echo ok",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 60,
		CreatedAt:      time.Now().UTC(),
	}

	// Valid mesh_id
	if err := envelope.Validate(env); err != nil {
		t.Fatalf("expected valid envelope with mesh_id to pass, got: %v", err)
	}

	// Invalid mesh_ids
	for _, invalid := range []string{"", "   ", "\t"} {
		envCopy := *env
		envCopy.MeshID = invalid
		err := envelope.Validate(&envCopy)
		if err == nil {
			t.Fatalf("expected error for empty mesh_id %q, got nil", invalid)
		}
		if !errors.Is(err, envelope.ErrInvalidMeshID) {
			t.Fatalf("expected ErrInvalidMeshID for %q, got: %v", invalid, err)
		}
	}
}

func TestEnvelope_Hash_IncludesProtocolVersionAndMeshID(t *testing.T) {
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-1",
		EnvelopeID:      "018e3a2b-0001-7000-8000-000000000001",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/work",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "check-exit",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "echo ok",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 60,
		CreatedAt:      time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	}

	h1, err := envelope.ComputeEnvelopeHash(env)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash failed: %v", err)
	}

	// Different mesh_id must yield different hash
	env2 := *env
	env2.MeshID = "mesh-2"
	h2, err := envelope.ComputeEnvelopeHash(&env2)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash for env2 failed: %v", err)
	}
	if h1 == h2 {
		t.Errorf("hash collision: different mesh_id produced identical hash %s", h1)
	}
}

func TestLease_S9_ValidateLease(t *testing.T) {
	validLease := &envelope.Lease{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		LeaseID:         "lease-1",
		EnvelopeID:      "018e3a2b-0001-7000-8000-000000000001",
		ExecutorAgentID: "agent-b",
		Accepted:        true,
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
	}

	if err := envelope.ValidateLease(validLease); err != nil {
		t.Fatalf("expected valid lease to pass, got: %v", err)
	}

	// Wrong version
	badVer := *validLease
	badVer.ProtocolVersion = "1"
	if err := envelope.ValidateLease(&badVer); !errors.Is(err, envelope.ErrUnknownProtocolVersion) {
		t.Fatalf("expected ErrUnknownProtocolVersion, got: %v", err)
	}

	// Empty mesh_id
	badMesh := *validLease
	badMesh.MeshID = ""
	if err := envelope.ValidateLease(&badMesh); !errors.Is(err, envelope.ErrInvalidMeshID) {
		t.Fatalf("expected ErrInvalidMeshID, got: %v", err)
	}
}
