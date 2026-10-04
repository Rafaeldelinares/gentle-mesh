package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open error: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func setupChain(t *testing.T) (*ChainStore, *sql.DB) {
	db := openDB(t)
	cs := NewChainStore(db)
	if err := cs.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema error: %v", err)
	}
	return cs, db
}

// ─────────────────────────────────────────────────────────────────
// Schema
// ─────────────────────────────────────────────────────────────────

func TestInitSchema(t *testing.T) {
	db := openDB(t)
	cs := NewChainStore(db)

	err := cs.InitSchema(context.Background())
	if err != nil {
		t.Fatalf("InitSchema error: %v", err)
	}

	// Schema should be idempotent.
	err = cs.InitSchema(context.Background())
	if err != nil {
		t.Fatalf("InitSchema (idempotent) error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────
// Save and retrieve
// ─────────────────────────────────────────────────────────────────

func TestSaveReceipt_First(t *testing.T) {
	cs, _ := setupChain(t)
	r := validReceipt()
	r.ReceiptID = "receipt-001"
	r.PreviousReceiptHash = ""

	err := cs.SaveReceipt(context.Background(), r)
	if err != nil {
		t.Fatalf("SaveReceipt error: %v", err)
	}

	// Retrieve.
	retrieved, err := cs.GetReceipt(context.Background(), "receipt-001")
	if err != nil {
		t.Fatalf("GetReceipt error: %v", err)
	}
	if retrieved.ReceiptID != "receipt-001" {
		t.Errorf("ReceiptID = %q, want %q", retrieved.ReceiptID, "receipt-001")
	}
	if retrieved.Verdict != VerdictSettledClean {
		t.Errorf("Verdict = %v, want SETTLED_CLEAN", retrieved.Verdict)
	}
}

func TestSaveReceipt_ChainLink(t *testing.T) {
	cs, _ := setupChain(t)

	// First receipt (no previous hash).
	r1 := validReceipt()
	r1.ReceiptID = "receipt-001"
	r1.PreviousReceiptHash = ""

	if err := cs.SaveReceipt(context.Background(), r1); err != nil {
		t.Fatalf("SaveReceipt(r1) error: %v", err)
	}

	// Compute chain link for second receipt.
	hash2Raw := sha256.Sum256([]byte(r1.ExecutorSignature))
	hash2 := hex.EncodeToString(hash2Raw[:])

	// Second receipt (with previous hash).
	r2 := validReceipt()
	r2.ReceiptID = "receipt-002"
	r2.ContractID = "contract-002"
	r2.PreviousReceiptHash = hash2

	if err := cs.SaveReceipt(context.Background(), r2); err != nil {
		t.Fatalf("SaveReceipt(r2) error: %v", err)
	}

	// Retrieve both.
	got1, _ := cs.GetReceipt(context.Background(), "receipt-001")
	got2, _ := cs.GetReceipt(context.Background(), "receipt-002")

	if got1.PreviousReceiptHash != "" {
		t.Errorf("r1.PreviousReceiptHash = %q, want empty", got1.PreviousReceiptHash)
	}
	if got2.PreviousReceiptHash != hash2 {
		t.Errorf("r2.PreviousReceiptHash = %q, want %q", got2.PreviousReceiptHash, hash2)
	}
}

func TestSaveReceipt_ChainBroken(t *testing.T) {
	cs, _ := setupChain(t)

	// First receipt.
	r1 := validReceipt()
	r1.ReceiptID = "receipt-001"
	r1.PreviousReceiptHash = ""
	if err := cs.SaveReceipt(context.Background(), r1); err != nil {
		t.Fatalf("SaveReceipt(r1) failed: %v", err)
	}

	// Second receipt with WRONG previous hash: SaveReceipt validates prev_hash (S7) and rejects.
	r2 := validReceipt()
	r2.ReceiptID = "receipt-002"
	r2.ContractID = "contract-002"
	r2.PreviousReceiptHash = "wrong-hash-value-0000000000000000000000000000000000000000000"

	err := cs.SaveReceipt(context.Background(), r2)
	if err == nil {
		t.Fatal("expected error when saving receipt with wrong previous hash, got nil")
	}
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected ErrChainBroken, got: %v", err)
	}
}

func TestSaveReceipt_Nil(t *testing.T) {
	cs, _ := setupChain(t)
	err := cs.SaveReceipt(context.Background(), nil)
	if err != ErrInvalidReceipt {
		t.Errorf("SaveReceipt(nil) = %v, want %v", err, ErrInvalidReceipt)
	}
}

func TestSaveReceipt_ValidationFailure(t *testing.T) {
	cs, _ := setupChain(t)

	// Receipt with invalid protocol_version
	r1 := validReceipt()
	r1.ProtocolVersion = "1.0"
	r1.MeshID = "gentle-mesh-dev"
	if err := cs.SaveReceipt(context.Background(), r1); !errors.Is(err, ErrInvalidReceipt) {
		t.Errorf("SaveReceipt(invalid protocol_version) err = %v, want ErrInvalidReceipt", err)
	}

	// Receipt with empty mesh_id
	r2 := validReceipt()
	r2.ProtocolVersion = CurrentProtocolVersion
	r2.MeshID = ""
	if err := cs.SaveReceipt(context.Background(), r2); !errors.Is(err, ErrInvalidReceipt) {
		t.Errorf("SaveReceipt(empty mesh_id) err = %v, want ErrInvalidReceipt", err)
	}
}

func TestUpdateReceipt_ValidationFailure(t *testing.T) {
	cs, _ := setupChain(t)

	r := validReceipt()
	r.ProtocolVersion = "1.0"
	r.MeshID = "gentle-mesh-dev"
	if err := cs.UpdateReceipt(context.Background(), r); !errors.Is(err, ErrInvalidReceipt) {
		t.Errorf("UpdateReceipt(invalid protocol_version) err = %v, want ErrInvalidReceipt", err)
	}
}

func TestUpdateReceipt_LegacyReceiptRejected(t *testing.T) {
	cs, db := setupChain(t)

	// Insert legacy receipt (no protocol_version, no mesh_id) directly
	r := validReceipt()
	r.ProtocolVersion = ""
	r.MeshID = ""
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal legacy receipt: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO receipts (
			receipt_id, contract_id, envelope_hash,
			emitter_agent_id, executor_agent_id, verdict,
			previous_receipt_hash, executor_signature, executor_signed_at,
			emitter_acceptance, emitter_acceptance_at, emitter_signature,
			dispute_reason, data, seq
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.ReceiptID, r.ContractID, r.EnvelopeHash,
		r.EmitterAgentID, r.ExecutorAgentID, string(r.Verdict),
		nullable(r.PreviousReceiptHash), r.ExecutorSignature,
		r.ExecutorSignedAt.Format(time.RFC3339),
		nullable(string(r.EmitterAcceptance)),
		nil,
		nullable(r.EmitterSignature),
		nullable(r.DisputeReason),
		string(data),
		1,
	)
	if err != nil {
		t.Fatalf("insert legacy receipt: %v", err)
	}

	// Updating a legacy receipt (e.g. for accept/dispute) MUST fail with ErrInvalidReceipt
	r.EmitterAcceptance = AcceptanceAccepted
	if err := cs.UpdateReceipt(context.Background(), r); !errors.Is(err, ErrInvalidReceipt) {
		t.Errorf("UpdateReceipt on legacy receipt err = %v, want ErrInvalidReceipt", err)
	}

	// Injecting a legacy receipt MUST fail with ErrInvalidReceipt
	if err := cs.InjectReceipt(context.Background(), r); !errors.Is(err, ErrInvalidReceipt) {
		t.Errorf("InjectReceipt on legacy receipt err = %v, want ErrInvalidReceipt", err)
	}
}

func TestGetReceipt_NotFound(t *testing.T) {
	cs, _ := setupChain(t)
	_, err := cs.GetReceipt(context.Background(), "nonexistent")
	if err != ErrReceiptNotFound {
		t.Errorf("GetReceipt(nonexistent) = %v, want %v", err, ErrReceiptNotFound)
	}
}

// ─────────────────────────────────────────────────────────────────
// Chain retrieval
// ─────────────────────────────────────────────────────────────────

func TestGetChain_Order(t *testing.T) {
	cs, _ := setupChain(t)

	// Save 3 receipts in order.
	for i := 1; i <= 3; i++ {
		r := validReceipt()
		r.ReceiptID = "receipt-00" + string(rune('0'+i))
		if i > 1 {
			prev, _ := cs.GetLastReceipt(context.Background(), r.EmitterAgentID, r.ExecutorAgentID)
			h := sha256.Sum256([]byte(prev.ExecutorSignature))
			r.PreviousReceiptHash = hex.EncodeToString(h[:])
		} else {
			r.PreviousReceiptHash = ""
		}
		r.ContractID = "contract-" + string(rune('0'+i))
		cs.SaveReceipt(context.Background(), r)
		time.Sleep(1 * time.Millisecond) // ensure timestamps differ
	}

	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain error: %v", err)
	}
	if len(chain) != 3 {
		t.Errorf("GetChain length = %d, want 3", len(chain))
	}
	// Verify chronological order.
	for i := 0; i < len(chain)-1; i++ {
		if chain[i].ExecutorSignedAt.After(chain[i+1].ExecutorSignedAt) {
			t.Errorf("GetChain not in order: receipt[%d].signed_at > receipt[%d].signed_at",
				i, i+1)
		}
	}
}

func TestGetChain_Empty(t *testing.T) {
	cs, _ := setupChain(t)
	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain error: %v", err)
	}
	if len(chain) != 0 {
		t.Errorf("GetChain length = %d, want 0", len(chain))
	}
}

func TestGetLastReceipt(t *testing.T) {
	cs, _ := setupChain(t)

	// Save first.
	r1 := validReceipt()
	r1.ReceiptID = "first"
	r1.PreviousReceiptHash = ""
	cs.SaveReceipt(context.Background(), r1)
	time.Sleep(1 * time.Millisecond)

	// Save second.
	r2 := validReceipt()
	r2.ReceiptID = "second"
	r2.ContractID = "contract-second"
	h := sha256.Sum256([]byte(r1.ExecutorSignature))
	r2.PreviousReceiptHash = hex.EncodeToString(h[:])
	cs.SaveReceipt(context.Background(), r2)

	last, err := cs.GetLastReceipt(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetLastReceipt error: %v", err)
	}
	if last.ReceiptID != "second" {
		t.Errorf("GetLastReceipt.ReceiptID = %q, want %q", last.ReceiptID, "second")
	}
}

func TestGetLastReceipt_Empty(t *testing.T) {
	cs, _ := setupChain(t)
	_, err := cs.GetLastReceipt(context.Background(), "agent-a", "agent-b")
	if err != ErrReceiptNotFound {
		t.Errorf("GetLastReceipt(empty) = %v, want %v", err, ErrReceiptNotFound)
	}
}

// ─────────────────────────────────────────────────────────────────
// ComputeReceiptHash
// ─────────────────────────────────────────────────────────────────

func TestComputeReceiptHash(t *testing.T) {
	r := validReceipt()
	r.ExecutorSignature = "b64sig1"
	r.EmitterSignature = "b64sig2"

	hash1, err := ComputeReceiptHash(r)
	if err != nil {
		t.Fatalf("ComputeReceiptHash error: %v", err)
	}

	// Hash should be 64 hex chars.
	if len(hash1) != 64 {
		t.Errorf("Hash length = %d, want 64", len(hash1))
	}

	// Should be deterministic.
	hash2, _ := ComputeReceiptHash(r)
	if hash1 != hash2 {
		t.Errorf("ComputeReceiptHash not deterministic: %q != %q", hash1, hash2)
	}

	// Changing the receipt should change the hash.
	r2 := validReceipt()
	r2.ExecutorSignature = "b64sig1"
	r2.EmitterSignature = "b64sig2"
	r2.ContractID = "different-contract-id"

	hash3, _ := ComputeReceiptHash(r2)
	if hash1 == hash3 {
		t.Error("Hash changed after modifying receipt content")
	}
}

func TestComputeReceiptHash_ClearsSignatures(t *testing.T) {
	// Use a fixed timestamp so both calls produce the same hash.
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := &SettlementReceipt{
		ReceiptID:         "test-001",
		ContractID:        "contract-001",
		EnvelopeHash:      "abc123",
		EmitterAgentID:    "agent-a",
		ExecutorAgentID:   "agent-b",
		Verdict:           VerdictSettledClean,
		ExecutorSignature: "sig-a",
		ExecutorSignedAt:  fixedTime,
		EmitterSignature:  "sig-b",
	}

	hash1, _ := ComputeReceiptHash(r)

	// Same content, signatures cleared: hash must be identical.
	r2 := &SettlementReceipt{
		ReceiptID:         "test-001",
		ContractID:        "contract-001",
		EnvelopeHash:      "abc123",
		EmitterAgentID:    "agent-a",
		ExecutorAgentID:   "agent-b",
		Verdict:           VerdictSettledClean,
		ExecutorSignature: "",
		ExecutorSignedAt:  fixedTime,
		EmitterSignature:  "",
	}

	hash2, _ := ComputeReceiptHash(r2)
	if hash1 != hash2 {
		t.Errorf("ComputeReceiptHash differs when signatures cleared: %q != %q", hash1, hash2)
	}
}

// ─────────────────────────────────────────────────────────────────
// Chain verification
// ─────────────────────────────────────────────────────────────────

func makeTestSigners(t *testing.T) (*signing.BasicSigner, *signing.BasicSigner) {
	emitter, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner(emitter) error: %v", err)
	}
	executor, err := signing.GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("GenerateSigner(executor) error: %v", err)
	}
	return emitter, executor
}

func signReceipt(r *SettlementReceipt, executor *signing.BasicSigner) error {
	// Set timestamp BEFORE computing hash (hash must include signed_at).
	r.ExecutorSignedAt = time.Now().UTC()
	hash, err := ComputeReceiptHash(r)
	if err != nil {
		return err
	}
	sig, err := executor.Sign([]byte(hash))
	if err != nil {
		return err
	}
	r.ExecutorSignature = sig
	return nil
}

func TestVerifyChain_Valid(t *testing.T) {
	cs, _ := setupChain(t)
	emitter, executor := makeTestSigners(t)

	// Build a 2-receipt chain with proper signatures.
	r1 := validReceipt()
	r1.ReceiptID = "vr-001"
	r1.PreviousReceiptHash = ""
	signReceipt(r1, executor)
	cs.SaveReceipt(context.Background(), r1)

	h2 := sha256.Sum256([]byte(r1.ExecutorSignature))
	r2 := validReceipt()
	r2.ReceiptID = "vr-002"
	r2.ContractID = "contract-vr-002"
	r2.PreviousReceiptHash = hex.EncodeToString(h2[:])
	signReceipt(r2, executor)
	cs.SaveReceipt(context.Background(), r2)

	results, err := cs.VerifyChain(context.Background(),
		"agent-a", "agent-b",
		executor.PublicKey(), emitter.PublicKey())
	if err != nil {
		t.Fatalf("VerifyChain error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("VerifyChain results length = %d, want 2", len(results))
	}

	for _, r := range results {
		if !r.Valid {
			t.Errorf("Receipt %s not valid: %s", r.ReceiptID, r.Error)
		}
		if !r.PreviousHashValid {
			t.Errorf("Receipt %s: PreviousHashValid = false", r.ReceiptID)
		}
		if !r.ExecutorSignatureValid {
			t.Errorf("Receipt %s: ExecutorSignatureValid = false: %s", r.ReceiptID, r.Error)
		}
	}
}

func TestVerifyChain_BrokenChain(t *testing.T) {
	cs, _ := setupChain(t)
	_, executor := makeTestSigners(t)

	// First receipt.
	r1 := validReceipt()
	r1.ReceiptID = "bc-001"
	r1.PreviousReceiptHash = ""
	signReceipt(r1, executor)
	cs.SaveReceipt(context.Background(), r1)

	// Second receipt with CORRUPTED previous hash: injected directly to test VerifyChain.
	r2 := validReceipt()
	r2.ReceiptID = "bc-002"
	r2.ContractID = "contract-bc-002"
	r2.PreviousReceiptHash = "deadbeef00000000000000000000000000000000000000000000000000000000"
	r2.SequenceNumber = 2
	signReceipt(r2, executor)

	err := cs.InjectReceipt(context.Background(), r2)
	if err != nil {
		t.Errorf("InjectReceipt with corrupted prev_hash: expected success, got %v", err)
	}

	// VerifyChain should detect the broken chain.
	results, err := cs.VerifyChain(context.Background(),
		"agent-a", "agent-b",
		executor.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChain returned unexpected error: %v", err)
	}
	// Find receipt bc-002.
	var bc2 *VerificationResult
	for i := range results {
		if results[i].ReceiptID == "bc-002" {
			bc2 = &results[i]
			break
		}
	}
	if bc2 == nil {
		t.Fatal("bc-002 not found in verification results")
	}
	if bc2.PreviousHashValid {
		t.Error("VerifyChain: bc-002 should have PreviousHashValid=false (corrupted prev_hash)")
	}
	if bc2.Valid {
		t.Error("VerifyChain: bc-002 should be invalid due to broken chain link")
	}
}

func TestVerifyChain_Empty(t *testing.T) {
	cs, _ := setupChain(t)
	emitter, executor := makeTestSigners(t)

	_, err := cs.VerifyChain(context.Background(),
		"agent-a", "agent-b",
		executor.PublicKey(), emitter.PublicKey())
	if err != ErrReceiptNotFound {
		t.Errorf("VerifyChain(empty) = %v, want %v", err, ErrReceiptNotFound)
	}
}

func TestVerifyChain_WrongExecutorKey(t *testing.T) {
	cs, _ := setupChain(t)
	_, executor := makeTestSigners(t)
	wrongSigner, _ := signing.GenerateSigner("wrong")

	r := validReceipt()
	r.ReceiptID = "wk-001"
	r.PreviousReceiptHash = ""
	signReceipt(r, executor)
	cs.SaveReceipt(context.Background(), r)

	results, err := cs.VerifyChain(context.Background(),
		"agent-a", "agent-b",
		wrongSigner.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChain error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("VerifyChain results length = %d, want 1", len(results))
	}
	if results[0].ExecutorSignatureValid {
		t.Error("ExecutorSignatureValid should be false with wrong key")
	}
}

// ─────────────────────────────────────────────────────────────────
// Count and DeleteAll
// ─────────────────────────────────────────────────────────────────

func TestCount(t *testing.T) {
	cs, _ := setupChain(t)

	n, err := cs.Count(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if n != 0 {
		t.Errorf("Initial count = %d, want 0", n)
	}

	// Add receipts.
	var lastSig string
	signer := executorForTest()
	for i := 0; i < 3; i++ {
		r := validReceipt()
		r.ReceiptID = "count-" + string(rune('0'+i))
		if i == 0 {
			r.PreviousReceiptHash = ""
		} else {
			h := sha256.Sum256([]byte(lastSig))
			r.PreviousReceiptHash = hex.EncodeToString(h[:])
		}
		signReceipt(r, signer)
		lastSig = r.ExecutorSignature
		if err := cs.SaveReceipt(context.Background(), r); err != nil {
			t.Fatalf("SaveReceipt #%d: %v", i, err)
		}
	}

	n, _ = cs.Count(context.Background(), "agent-a", "agent-b")
	if n != 3 {
		t.Errorf("Count after 3 receipts = %d, want 3", n)
	}
}

func TestDeleteAll(t *testing.T) {
	cs, _ := setupChain(t)

	// Add receipts.
	r := validReceipt()
	r.PreviousReceiptHash = ""
	signReceipt(r, executorForTest())
	cs.SaveReceipt(context.Background(), r)

	n, _ := cs.Count(context.Background(), "agent-a", "agent-b")
	if n != 1 {
		t.Fatalf("Precondition: count = %d, want 1", n)
	}

	err := cs.DeleteAll(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("DeleteAll error: %v", err)
	}

	n, _ = cs.Count(context.Background(), "agent-a", "agent-b")
	if n != 0 {
		t.Errorf("Count after DeleteAll = %d, want 0", n)
	}
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

// executorForTest creates a fixed executor signer for tests that need one.
func executorForTest() *signing.BasicSigner {
	s, _ := signing.GenerateSigner("agent-b")
	return s
}

// MarshalJSON / UnmarshalJSON round-trip for receipt.
func TestReceiptJSONRoundTrip(t *testing.T) {
	r := validReceipt()
	r.ReceiptID = "json-test"
	r.PreviousReceiptHash = ""

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var r2 SettlementReceipt
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if r2.ReceiptID != r.ReceiptID {
		t.Errorf("ReceiptID mismatch: %q != %q", r2.ReceiptID, r.ReceiptID)
	}
	if r2.Verdict != r.Verdict {
		t.Errorf("Verdict mismatch: %v != %v", r2.Verdict, r.Verdict)
	}
	if r2.ExecutorAgentID != r.ExecutorAgentID {
		t.Errorf("ExecutorAgentID mismatch: %q != %q", r2.ExecutorAgentID, r.ExecutorAgentID)
	}
}
