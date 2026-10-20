package envelope_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func validTestEnvelope() *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-r7-test",
		EnvelopeID:      "env-r7-001",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/srv/workspace",
		},
		Preconditions: []envelope.Precondition{
			{
				Type:   envelope.PreconditionToolAvailable,
				Params: map[string]string{"tool": "go"},
			},
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "a1",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "go version",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds:  30,
		MaxRemediations: 2,
		CreatedAt:       time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func validTestLease() *envelope.Lease {
	return &envelope.Lease{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-r7-test",
		LeaseID:         "lease-r7-001",
		EnvelopeID:      "env-r7-001",
		ExecutorAgentID: "agent-b",
		Accepted:        true,
		ExpiresAt:       time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC),
	}
}

func validTestReceipt() *receipt.SettlementReceipt {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return &receipt.SettlementReceipt{
		ProtocolVersion: receipt.CurrentProtocolVersion,
		MeshID:          "mesh-r7-test",
		ReceiptID:       "rec-r7-001",
		ContractID:      "contract-r7-001",
		EnvelopeHash:    "f5a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: receipt.Territory{
			Repository:    "github.com/org/repo",
			Branch:        "main",
			WorkspacePath: "/srv/workspace",
		},
		Verdict: receipt.VerdictSettledClean,
		Assertions: []receipt.AssertionResult{
			{
				AssertionIndex: 0,
				AssertionID:    "a1",
				AssertionType:  "command_exit_code",
				Result:         receipt.ResultPass,
				Evidence: receipt.AssertionEvidence{
					Command:   "go version",
					ExitCode:  0,
					CheckedAt: now,
				},
			},
		},
		ExecutorSignedAt:    now,
		PreviousReceiptHash: "prev-hash-123",
	}
}

func TestSignEnvelope_DeterministicJCS(t *testing.T) {
	signer, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}

	env1 := validTestEnvelope()
	env2 := validTestEnvelope()

	if err := envelope.SignEnvelope(env1, signer); err != nil {
		t.Fatalf("SignEnvelope(env1): %v", err)
	}
	if err := envelope.SignEnvelope(env2, signer); err != nil {
		t.Fatalf("SignEnvelope(env2): %v", err)
	}

	if env1.EmitterSignature == "" {
		t.Fatal("EmitterSignature is empty after SignEnvelope")
	}
	if env1.EmitterSignature != env2.EmitterSignature {
		t.Fatalf("SignEnvelope not deterministic: %q != %q", env1.EmitterSignature, env2.EmitterSignature)
	}

	// Verify valid signature
	if err := envelope.VerifyEnvelopeSignature(env1, signer.PublicKey()); err != nil {
		t.Fatalf("VerifyEnvelopeSignature failed: %v", err)
	}

	// Verify with wrong key
	wrongSigner, _ := signing.GenerateSigner("agent-wrong")
	if err := envelope.VerifyEnvelopeSignature(env1, wrongSigner.PublicKey()); err == nil {
		t.Fatal("VerifyEnvelopeSignature with wrong key must fail")
	}

	// Tamper with envelope
	tampered := *env1
	tampered.MeshID = "malicious-mesh"
	if err := envelope.VerifyEnvelopeSignature(&tampered, signer.PublicKey()); err == nil {
		t.Fatal("VerifyEnvelopeSignature with tampered MeshID must fail")
	}
}

func TestSignLease_DeterministicJCS(t *testing.T) {
	signer, err := signing.GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}

	l1 := validTestLease()
	l2 := validTestLease()

	if err := envelope.SignLease(l1, signer); err != nil {
		t.Fatalf("SignLease(l1): %v", err)
	}
	if err := envelope.SignLease(l2, signer); err != nil {
		t.Fatalf("SignLease(l2): %v", err)
	}

	if l1.ExecutorSignature == "" {
		t.Fatal("ExecutorSignature is empty after SignLease")
	}
	if l1.ExecutorSignature != l2.ExecutorSignature {
		t.Fatalf("SignLease not deterministic: %q != %q", l1.ExecutorSignature, l2.ExecutorSignature)
	}

	// Verify valid signature
	if err := envelope.VerifyLeaseSignature(l1, signer.PublicKey()); err != nil {
		t.Fatalf("VerifyLeaseSignature failed: %v", err)
	}

	// Verify with wrong key
	wrongSigner, _ := signing.GenerateSigner("agent-wrong")
	if err := envelope.VerifyLeaseSignature(l1, wrongSigner.PublicKey()); err == nil {
		t.Fatal("VerifyLeaseSignature with wrong key must fail")
	}

	// Tamper with lease
	tampered := *l1
	tampered.Accepted = false
	if err := envelope.VerifyLeaseSignature(&tampered, signer.PublicKey()); err == nil {
		t.Fatal("VerifyLeaseSignature with tampered Accepted must fail")
	}
}

func TestComputeLeaseHash_Deterministic(t *testing.T) {
	l1 := validTestLease()
	l2 := validTestLease()

	h1, err := envelope.ComputeLeaseHash(l1)
	if err != nil {
		t.Fatalf("ComputeLeaseHash(l1): %v", err)
	}
	h2, err := envelope.ComputeLeaseHash(l2)
	if err != nil {
		t.Fatalf("ComputeLeaseHash(l2): %v", err)
	}
	if h1 != h2 {
		t.Fatalf("ComputeLeaseHash not deterministic: %q != %q", h1, h2)
	}

	// Verify that signature field does not affect hash
	l2.ExecutorSignature = "some-sig"
	h3, err := envelope.ComputeLeaseHash(l2)
	if err != nil {
		t.Fatalf("ComputeLeaseHash(l2 with sig): %v", err)
	}
	if h1 != h3 {
		t.Fatalf("ComputeLeaseHash changed with signature: %q != %q", h1, h3)
	}
}

func TestSignEnvelope_InvalidEnvelopeFails(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-a")

	badEnv := validTestEnvelope()
	badEnv.ProtocolVersion = "1"
	if err := envelope.SignEnvelope(badEnv, signer); err == nil {
		t.Fatal("SignEnvelope with protocol_version '1' must fail")
	}

	badMeshEnv := validTestEnvelope()
	badMeshEnv.MeshID = ""
	if err := envelope.SignEnvelope(badMeshEnv, signer); err == nil {
		t.Fatal("SignEnvelope with empty mesh_id must fail")
	}
}

func TestSignLease_InvalidLeaseFails(t *testing.T) {
	signer, _ := signing.GenerateSigner("agent-b")

	badLease := validTestLease()
	badLease.ProtocolVersion = "3"
	if err := envelope.SignLease(badLease, signer); err == nil {
		t.Fatal("SignLease with protocol_version '3' must fail")
	}

	badMeshLease := validTestLease()
	badMeshLease.MeshID = ""
	if err := envelope.SignLease(badMeshLease, signer); err == nil {
		t.Fatal("SignLease with empty mesh_id must fail")
	}
}

func TestR7_1000_Iterations_SQLite_Envelope_Lease_Receipt(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE envelopes (id TEXT PRIMARY KEY, data TEXT NOT NULL);
		CREATE TABLE leases (id TEXT PRIMARY KEY, data TEXT NOT NULL);
		CREATE TABLE receipts (id TEXT PRIMARY KEY, data TEXT NOT NULL);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}

	aSigner, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner A: %v", err)
	}
	bSigner, err := signing.GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("GenerateSigner B: %v", err)
	}

	// 1000 iterations for Envelope: sign -> serialize JCS -> SQLite -> read -> verify
	for i := 0; i < 1000; i++ {
		envID := fmt.Sprintf("env-%d", i)
		env := validTestEnvelope()
		env.EnvelopeID = envID
		env.CreatedAt = time.Now().UTC().Truncate(time.Second)

		if err := envelope.SignEnvelope(env, aSigner); err != nil {
			t.Fatalf("iter %d: SignEnvelope failed: %v", i, err)
		}

		data, err := jcs.Marshal(env)
		if err != nil {
			t.Fatalf("iter %d: jcs.Marshal env failed: %v", i, err)
		}

		if _, err := db.Exec(`INSERT INTO envelopes (id, data) VALUES (?, ?)`, envID, string(data)); err != nil {
			t.Fatalf("iter %d: insert env failed: %v", i, err)
		}

		var readData string
		if err := db.QueryRow(`SELECT data FROM envelopes WHERE id = ?`, envID).Scan(&readData); err != nil {
			t.Fatalf("iter %d: select env failed: %v", i, err)
		}

		var readEnv envelope.CognitiveTaskEnvelope
		if err := json.Unmarshal([]byte(readData), &readEnv); err != nil {
			t.Fatalf("iter %d: unmarshal env failed: %v", i, err)
		}

		if err := envelope.VerifyEnvelopeSignature(&readEnv, aSigner.PublicKey()); err != nil {
			t.Fatalf("iter %d: VerifyEnvelopeSignature failed: %v", i, err)
		}
	}

	// 1000 iterations for Lease: sign -> serialize JCS -> SQLite -> read -> verify
	for i := 0; i < 1000; i++ {
		leaseID := fmt.Sprintf("lease-%d", i)
		lease := validTestLease()
		lease.LeaseID = leaseID
		lease.ExpiresAt = time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)

		if err := envelope.SignLease(lease, bSigner); err != nil {
			t.Fatalf("iter %d: SignLease failed: %v", i, err)
		}

		data, err := jcs.Marshal(lease)
		if err != nil {
			t.Fatalf("iter %d: jcs.Marshal lease failed: %v", i, err)
		}

		if _, err := db.Exec(`INSERT INTO leases (id, data) VALUES (?, ?)`, leaseID, string(data)); err != nil {
			t.Fatalf("iter %d: insert lease failed: %v", i, err)
		}

		var readData string
		if err := db.QueryRow(`SELECT data FROM leases WHERE id = ?`, leaseID).Scan(&readData); err != nil {
			t.Fatalf("iter %d: select lease failed: %v", i, err)
		}

		var readLease envelope.Lease
		if err := json.Unmarshal([]byte(readData), &readLease); err != nil {
			t.Fatalf("iter %d: unmarshal lease failed: %v", i, err)
		}

		if err := envelope.VerifyLeaseSignature(&readLease, bSigner.PublicKey()); err != nil {
			t.Fatalf("iter %d: VerifyLeaseSignature failed: %v", i, err)
		}
	}

	// 1000 iterations for Receipt: sign (executor + emitter) -> serialize JCS -> SQLite -> read -> verify both
	for i := 0; i < 1000; i++ {
		recID := fmt.Sprintf("rec-%d", i)
		rec := validTestReceipt()
		rec.ReceiptID = recID

		if err := receipt.SignReceipt(rec, bSigner); err != nil {
			t.Fatalf("iter %d: SignReceipt failed: %v", i, err)
		}
		if err := receipt.AcceptReceipt(rec, aSigner, rec.ExecutorSignedAt); err != nil {
			t.Fatalf("iter %d: AcceptReceipt failed: %v", i, err)
		}

		data, err := jcs.Marshal(rec)
		if err != nil {
			t.Fatalf("iter %d: jcs.Marshal receipt failed: %v", i, err)
		}

		if _, err := db.Exec(`INSERT INTO receipts (id, data) VALUES (?, ?)`, recID, string(data)); err != nil {
			t.Fatalf("iter %d: insert receipt failed: %v", i, err)
		}

		var readData string
		if err := db.QueryRow(`SELECT data FROM receipts WHERE id = ?`, recID).Scan(&readData); err != nil {
			t.Fatalf("iter %d: select receipt failed: %v", i, err)
		}

		var readRec receipt.SettlementReceipt
		if err := json.Unmarshal([]byte(readData), &readRec); err != nil {
			t.Fatalf("iter %d: unmarshal receipt failed: %v", i, err)
		}

		if err := receipt.VerifyExecutorSignature(&readRec, bSigner.PublicKey()); err != nil {
			t.Fatalf("iter %d: VerifyExecutorSignature failed: %v", i, err)
		}
		if err := receipt.VerifyEmitterSignature(&readRec, aSigner.PublicKey()); err != nil {
			t.Fatalf("iter %d: VerifyEmitterSignature failed: %v", i, err)
		}
	}
}
