package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// openChainDBAt opens an independent *sql.DB handle to the given SQLite file,
// mimicking a second process/instance sharing the same database. It mirrors the
// production chain-store configuration: WAL journal mode and a single pooled
// connection (pkg/shell/shell.go, integration/agent/server.go).
func openChainDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		t.Fatalf("enable WAL on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestR4_SaveReceipt_TwoHandles_Concurrent_BEGIN_IMMEDIATE exercises two independent
// *sql.DB handles (separate connection pools, hence separate ChainStore mutexes) over
// the same SQLite file, with many goroutines per handle appending to the same
// (emitter, executor) chain at once.
//
// Invariants:
//   - every SaveReceipt result is nil, ErrSequenceConflict or ErrInvalidPreviousHash;
//     a raw SQLITE_BUSY ("database is locked") must never escape the store;
//   - the final chain is valid: contiguous sequences 1..N, no duplicate receipt IDs,
//     no broken links, and VerifyChainIntegrity reports Valid on every receipt.
func TestR4_SaveReceipt_TwoHandles_Concurrent_BEGIN_IMMEDIATE(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "chain_two_handles.db")

	db1 := openChainDBAt(t, dbPath)
	cs1 := NewChainStore(db1)
	if err := cs1.InitSchema(context.Background()); err != nil {
		t.Fatalf("cs1.InitSchema: %v", err)
	}

	db2 := openChainDBAt(t, dbPath)
	cs2 := NewChainStore(db2)

	_, signer := makeTestSigners(t)
	stores := []*ChainStore{cs1, cs2}
	const goroutinesPerHandle = 8
	total := goroutinesPerHandle * len(stores)

	var (
		mu          sync.Mutex
		unexpected  []error
		lockEscaped []error
		wg          sync.WaitGroup
	)
	start := make(chan struct{})

	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(strings.ToLower(err.Error()), "locked") ||
			strings.Contains(strings.ToLower(err.Error()), "busy") {
			lockEscaped = append(lockEscaped, err)
		}
		unexpected = append(unexpected, err)
	}

	for handle, cs := range stores {
		for worker := 0; worker < goroutinesPerHandle; worker++ {
			wg.Add(1)
			go func(handle, worker int, cs *ChainStore) {
				defer wg.Done()
				<-start
				contractID := fmt.Sprintf("contract-h%d-w%d", handle, worker)
				for attempt := 0; attempt < 3000; attempt++ {
					last, err := cs.GetLastReceipt(context.Background(), "agent-a", "agent-b")
					if err != nil && !errors.Is(err, ErrReceiptNotFound) {
						record(fmt.Errorf("handle %d worker %d: GetLastReceipt: %w", handle, worker, err))
						return
					}

					var prevHash string
					if last != nil {
						h := sha256.Sum256([]byte(last.ExecutorSignature))
						prevHash = hex.EncodeToString(h[:])
					}

					r := makeSignedReceipt(t, "agent-a", "agent-b", prevHash, contractID, signer)
					saveErr := cs.SaveReceipt(context.Background(), r)
					if saveErr == nil {
						return
					}
					if errors.Is(saveErr, ErrSequenceConflict) ||
						errors.Is(saveErr, ErrInvalidPreviousHash) ||
						errors.Is(saveErr, ErrChainBroken) {
						continue
					}

					record(fmt.Errorf("handle %d worker %d: SaveReceipt: %w", handle, worker, saveErr))
					return
				}
				record(fmt.Errorf("handle %d worker %d: exhausted attempts", handle, worker))
			}(handle, worker, cs)
		}
	}

	close(start)
	wg.Wait()

	for _, err := range lockEscaped {
		t.Errorf("raw SQLite lock/busy error escaped SaveReceipt: %v", err)
	}
	for _, err := range unexpected {
		t.Errorf("unexpected error: %v", err)
	}

	chain, err := cs1.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != total {
		t.Fatalf("expected %d receipts in chain, got %d", total, len(chain))
	}

	seenIDs := make(map[string]struct{}, len(chain))
	for i, r := range chain {
		wantSeq := int64(i + 1)
		if r.SequenceNumber != wantSeq {
			t.Errorf("chain[%d] SequenceNumber = %d, want %d (gap or duplicate)", i, r.SequenceNumber, wantSeq)
		}
		if _, dup := seenIDs[r.ReceiptID]; dup {
			t.Errorf("duplicate receipt_id persisted: %s", r.ReceiptID)
		}
		seenIDs[r.ReceiptID] = struct{}{}
		if i > 0 {
			h := sha256.Sum256([]byte(chain[i-1].ExecutorSignature))
			if want := hex.EncodeToString(h[:]); r.PreviousReceiptHash != want {
				t.Errorf("chain[%d] broken link: got %s want %s", i, r.PreviousReceiptHash, want)
			}
		}
	}

	results, err := VerifyChainIntegrity(chain, signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity: %v", err)
	}
	for i, res := range results {
		if !res.Valid {
			t.Errorf("chain[%d] verification invalid: %s", i, res.Error)
		}
	}
}

// TestR4_SaveReceipt_DuplicateReceiptID_NotSequenceConflict verifies that a unique
// constraint violation that is NOT the (emitter, executor, seq) pair index is never
// reported as ErrSequenceConflict. The test installs an extra unique constraint on
// (receipt_id, seq) so that two different pairs sharing a receipt_id and seq produce
// a UNIQUE error whose message also mentions a "seq" column.
func TestR4_SaveReceipt_DuplicateReceiptID_NotSequenceConflict(t *testing.T) {
	cs, db := setupChain(t)
	_, signer := makeTestSigners(t)

	if _, err := db.Exec(
		"CREATE UNIQUE INDEX idx_test_receipt_id_seq ON receipts(receipt_id, seq);",
	); err != nil {
		t.Fatalf("create test unique index: %v", err)
	}

	// First receipt for pair (agent-a, agent-b) gets seq = 1.
	first := makeSignedReceipt(t, "agent-a", "agent-b", "", "contract-dup", signer)
	first.ReceiptID = "rec-shared-id"
	if err := SignReceipt(first, signer); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	if err := cs.SaveReceipt(context.Background(), first); err != nil {
		t.Fatalf("first SaveReceipt: %v", err)
	}

	// Second receipt for a different pair reuses the same receipt_id, so it also
	// starts at seq = 1 and collides on (receipt_id, seq).
	second := makeSignedReceipt(t, "agent-a", "agent-c", "", "contract-dup-2", signer)
	second.ReceiptID = "rec-shared-id"
	if err := SignReceipt(second, signer); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}

	err := cs.SaveReceipt(context.Background(), second)
	if err == nil {
		t.Fatal("expected a unique constraint error, got nil")
	}
	if errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("duplicate receipt_id must not be classified as ErrSequenceConflict: %v", err)
	}
}
