package receipt_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
)

func TestReceipt_S9_ProtocolVersion_StrictV2(t *testing.T) {
	validReceipt := &receipt.SettlementReceipt{
		ProtocolVersion:  receipt.CurrentProtocolVersion,
		MeshID:           "gentle-mesh-dev",
		ReceiptID:        "rec-001",
		ContractID:       "contract-001",
		EnvelopeHash:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EmitterAgentID:   "agent-a",
		ExecutorAgentID:  "agent-b",
		Verdict:          receipt.VerdictSettledClean,
		ExecutorSignedAt: time.Now().UTC(),
	}

	if err := receipt.ValidateReceipt(validReceipt); err != nil {
		t.Fatalf("expected valid receipt to pass, got: %v", err)
	}

	// S9: Invalid versions must be rejected
	for _, v := range []string{"1", "1.0", "3", "v2", "", " "} {
		r := *validReceipt
		r.ProtocolVersion = v
		err := receipt.ValidateReceipt(&r)
		if err == nil {
			t.Fatalf("expected error for protocol_version %q, got nil", v)
		}
		if !errors.Is(err, receipt.ErrUnknownProtocolVersion) {
			t.Fatalf("expected ErrUnknownProtocolVersion for %q, got: %v", v, err)
		}
	}
}

func TestReceipt_S9_MeshID_Required(t *testing.T) {
	validReceipt := &receipt.SettlementReceipt{
		ProtocolVersion:  receipt.CurrentProtocolVersion,
		MeshID:           "gentle-mesh-prod",
		ReceiptID:        "rec-001",
		ContractID:       "contract-001",
		EnvelopeHash:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EmitterAgentID:   "agent-a",
		ExecutorAgentID:  "agent-b",
		Verdict:          receipt.VerdictSettledClean,
		ExecutorSignedAt: time.Now().UTC(),
	}

	for _, invalid := range []string{"", "   ", "\t"} {
		r := *validReceipt
		r.MeshID = invalid
		err := receipt.ValidateReceipt(&r)
		if err == nil {
			t.Fatalf("expected error for empty mesh_id %q, got nil", invalid)
		}
		if !errors.Is(err, receipt.ErrInvalidMeshID) {
			t.Fatalf("expected ErrInvalidMeshID for %q, got: %v", invalid, err)
		}
	}
}

func TestReceipt_Hash_IncludesProtocolVersionAndMeshID(t *testing.T) {
	r1 := &receipt.SettlementReceipt{
		ProtocolVersion:  receipt.CurrentProtocolVersion,
		MeshID:           "mesh-1",
		ReceiptID:        "rec-001",
		ContractID:       "contract-001",
		EnvelopeHash:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EmitterAgentID:   "agent-a",
		ExecutorAgentID:  "agent-b",
		Verdict:          receipt.VerdictSettledClean,
		ExecutorSignedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	}

	h1, err := receipt.ComputeReceiptHash(r1)
	if err != nil {
		t.Fatalf("ComputeReceiptHash failed: %v", err)
	}

	// Different mesh_id
	r2 := *r1
	r2.MeshID = "mesh-2"
	h2, err := receipt.ComputeReceiptHash(&r2)
	if err != nil {
		t.Fatalf("ComputeReceiptHash r2 failed: %v", err)
	}
	if h1 == h2 {
		t.Errorf("expected different hash for different mesh_id, got identical %s", h1)
	}

	// Different protocol_version
	r3 := *r1
	r3.ProtocolVersion = "3"
	h3, err := receipt.ComputeReceiptHash(&r3)
	if err != nil {
		t.Fatalf("ComputeReceiptHash r3 failed: %v", err)
	}
	if h1 == h3 {
		t.Errorf("expected different hash for different protocol_version, got identical %s", h1)
	}
}
