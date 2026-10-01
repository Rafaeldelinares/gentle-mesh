package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupOldSchemaDB creates a disk-based SQLite DB with the pre-R4/S7 schema (without seq column).
func setupOldSchemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_old_schema.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	db.SetMaxOpenConns(1)

	oldSchema := `
	CREATE TABLE receipts (
		receipt_id            TEXT NOT NULL,
		contract_id           TEXT NOT NULL,
		envelope_hash         TEXT NOT NULL,
		emitter_agent_id      TEXT NOT NULL,
		executor_agent_id     TEXT NOT NULL,
		verdict               TEXT NOT NULL,
		previous_receipt_hash TEXT,
		executor_signature    TEXT NOT NULL,
		executor_signed_at    TEXT NOT NULL,
		emitter_acceptance    TEXT,
		emitter_acceptance_at TEXT,
		emitter_signature     TEXT,
		dispute_reason        TEXT,
		data                  TEXT NOT NULL
	);

	CREATE INDEX idx_receipts_pair
		ON receipts(emitter_agent_id, executor_agent_id, executor_signed_at);

	CREATE INDEX idx_receipts_contract
		ON receipts(contract_id);
	`
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatalf("failed to create old schema: %v", err)
	}
	return db, dbPath
}

// insertOldReceipt inserts a receipt row directly into the old receipts table.
func insertOldReceipt(t *testing.T, db *sql.DB, r *SettlementReceipt) {
	t.Helper()
	dataJSON, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}

	query := `
	INSERT INTO receipts (
		receipt_id, contract_id, envelope_hash, emitter_agent_id, executor_agent_id,
		verdict, previous_receipt_hash, executor_signature, executor_signed_at,
		emitter_acceptance, emitter_acceptance_at, emitter_signature,
		dispute_reason, data
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = db.Exec(query,
		r.ReceiptID,
		r.ContractID,
		r.EnvelopeHash,
		r.EmitterAgentID,
		r.ExecutorAgentID,
		string(r.Verdict),
		r.PreviousReceiptHash,
		r.ExecutorSignature,
		r.ExecutorSignedAt.Format(time.RFC3339Nano),
		r.EmitterAcceptance,
		r.EmitterAcceptanceAt,
		r.EmitterSignature,
		r.DisputeReason,
		string(dataJSON),
	)
	if err != nil {
		t.Fatalf("insert old receipt %s failed: %v", r.ReceiptID, err)
	}
}

// TestMigration_OldSchema_MultipleReceiptsPerPair tests atomic migration on a real SQLite DB
// with pre-existing receipts for multiple agent pairs, including a pair with 3+ receipts.
func TestMigration_OldSchema_MultipleReceiptsPerPair(t *testing.T) {
	db, _ := setupOldSchemaDB(t)
	defer db.Close()

	_, signer := makeTestSigners(t)

	// Pair 1: agent-a -> agent-b (3 receipts in valid hash chain)
	var prevHash string
	pair1Receipts := make([]*SettlementReceipt, 3)
	for i := 0; i < 3; i++ {
		r := makeSignedReceipt(t, "agent-a", "agent-b", prevHash, fmt.Sprintf("contract-ab-%d", i+1), signer)
		insertOldReceipt(t, db, r)
		pair1Receipts[i] = r
		h := sha256.Sum256([]byte(r.ExecutorSignature))
		prevHash = hex.EncodeToString(h[:])
	}

	// Pair 2: agent-c -> agent-d (2 receipts in valid hash chain)
	prevHash = ""
	pair2Receipts := make([]*SettlementReceipt, 2)
	for i := 0; i < 2; i++ {
		r := makeSignedReceipt(t, "agent-c", "agent-d", prevHash, fmt.Sprintf("contract-cd-%d", i+1), signer)
		insertOldReceipt(t, db, r)
		pair2Receipts[i] = r
		h := sha256.Sum256([]byte(r.ExecutorSignature))
		prevHash = hex.EncodeToString(h[:])
	}

	cs := NewChainStore(db)

	// Step 1: Run InitSchema which must atomically migrate the existing table.
	if err := cs.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema migration failed: %v", err)
	}

	// Step 2: Idempotence - running InitSchema a second time must succeed without error or modification.
	if err := cs.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema second execution (idempotence) failed: %v", err)
	}

	// Step 3: Verify Pair 1 sequence numbers (must be 1, 2, 3)
	chain1, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain(agent-a, agent-b) failed: %v", err)
	}
	if len(chain1) != 3 {
		t.Fatalf("expected 3 receipts for pair 1, got %d", len(chain1))
	}
	for i, r := range chain1 {
		expectedSeq := int64(i + 1)
		if r.SequenceNumber != expectedSeq {
			t.Errorf("pair1 chain[%d] SequenceNumber = %d, want %d", i, r.SequenceNumber, expectedSeq)
		}
	}

	// Step 4: Verify Pair 2 sequence numbers (must be 1, 2)
	chain2, err := cs.GetChain(context.Background(), "agent-c", "agent-d")
	if err != nil {
		t.Fatalf("GetChain(agent-c, agent-d) failed: %v", err)
	}
	if len(chain2) != 2 {
		t.Fatalf("expected 2 receipts for pair 2, got %d", len(chain2))
	}
	for i, r := range chain2 {
		expectedSeq := int64(i + 1)
		if r.SequenceNumber != expectedSeq {
			t.Errorf("pair2 chain[%d] SequenceNumber = %d, want %d", i, r.SequenceNumber, expectedSeq)
		}
	}

	// Step 5: VerifyChainIntegrity must pass over the migrated chain
	results1, err := VerifyChainIntegrity(chain1, signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity pair1 failed: %v", err)
	}
	for i, res := range results1 {
		if !res.SequenceValid || !res.PreviousHashValid || !res.ExecutorSignatureValid {
			t.Errorf("pair1 receipt %d verification failed: %+v", i, res)
		}
	}

	results2, err := VerifyChainIntegrity(chain2, signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity pair2 failed: %v", err)
	}
	for i, res := range results2 {
		if !res.SequenceValid || !res.PreviousHashValid || !res.ExecutorSignatureValid {
			t.Errorf("pair2 receipt %d verification failed: %+v", i, res)
		}
	}
}

// TestMigration_ForkedChain_Rejected verifies that an ambiguous/forked chain
// (e.g. multiple root receipts for the same pair) fails migration with an explicit error
// and leaves the database uncorrupted.
func TestMigration_ForkedChain_Rejected(t *testing.T) {
	db, _ := setupOldSchemaDB(t)
	defer db.Close()

	_, signer := makeTestSigners(t)

	// Two receipts with previous_receipt_hash = "" for the SAME pair (fork at root)
	r1 := makeSignedReceipt(t, "agent-a", "agent-b", "", "contract-1", signer)
	r2 := makeSignedReceipt(t, "agent-a", "agent-b", "", "contract-2", signer)
	insertOldReceipt(t, db, r1)
	insertOldReceipt(t, db, r2)

	cs := NewChainStore(db)

	err := cs.InitSchema(context.Background())
	if err == nil {
		t.Fatal("expected InitSchema to fail on forked chain, got nil")
	}
	if !errors.Is(err, ErrChainBroken) {
		t.Errorf("expected error wrapping ErrChainBroken, got %v", err)
	}

	// Verify database is left in a valid state (no unique index created with corrupt seq)
	var colCount int
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('receipts') WHERE name = 'seq'").Scan(&colCount)
	if err != nil {
		t.Fatalf("pragma check failed: %v", err)
	}
}

// TestMigration_BrokenChain_Rejected verifies that a broken hash chain
// (missing link / invalid previous_receipt_hash) fails migration with an explicit error
// and rolls back cleanly.
func TestMigration_BrokenChain_Rejected(t *testing.T) {
	db, _ := setupOldSchemaDB(t)
	defer db.Close()

	_, signer := makeTestSigners(t)

	// Root receipt
	r1 := makeSignedReceipt(t, "agent-a", "agent-b", "", "contract-1", signer)
	insertOldReceipt(t, db, r1)

	// Second receipt has an invalid prevHash that points to nowhere
	r2 := makeSignedReceipt(t, "agent-a", "agent-b", "deadbeef00000000000000000000000000000000000000000000000000000000", "contract-2", signer)
	insertOldReceipt(t, db, r2)

	cs := NewChainStore(db)

	err := cs.InitSchema(context.Background())
	if err == nil {
		t.Fatal("expected InitSchema to fail on broken chain, got nil")
	}
	if !errors.Is(err, ErrChainBroken) && !errors.Is(err, ErrInvalidPreviousHash) {
		t.Errorf("expected error wrapping ErrChainBroken or ErrInvalidPreviousHash, got %v", err)
	}
}
