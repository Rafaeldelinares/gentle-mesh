package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Adversarial tests for at-rest chain rewriting.
//
// BACKGROUND: the executor signature does NOT cover `previous_receipt_hash`
// (nor `sequence_number`), so an attacker with write access to the SQLite file
// can delete a middle receipt or reorder receipts, recompute `previous_receipt_hash`
// and `seq` contiguously, and VerifyChainIntegrity still reports Valid=true on
// every receipt. See issue #43.
//
// These tests are skipped on purpose: they encode the EXPECTED (secure) behavior
// and MUST be enabled by the PR that implements the versioned signing scheme
// described in issue #43. When enabled against the current implementation they
// FAIL, which is exactly the security gap being tracked.

// chainLinkHash is the chain-link function: SHA-256 over the raw executor signature.
func chainLinkHash(executorSignature string) string {
	h := sha256.Sum256([]byte(executorSignature))
	return hex.EncodeToString(h[:])
}

// rewriteStoredReceipt persists a mutated receipt (new seq and/or prev hash),
// keeping the `seq` column and the JSON `data` payload consistent.
func rewriteStoredReceipt(t *testing.T, db *sql.DB, r *SettlementReceipt, seq int64) {
	t.Helper()
	r.SequenceNumber = seq
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal receipt %s: %v", r.ReceiptID, err)
	}
	if _, err := db.Exec(
		"UPDATE receipts SET seq = ?, previous_receipt_hash = ?, data = ? WHERE receipt_id = ?",
		seq, nullable(r.PreviousReceiptHash), string(blob), r.ReceiptID,
	); err != nil {
		t.Fatalf("rewrite receipt %s: %v", r.ReceiptID, err)
	}
}

// seedValidChain5 stores 5 receipts with real Ed25519 signatures and returns the
// store plus a fresh copy of the chain as persisted.
func seedValidChain5(t *testing.T, signer signing.Signer) (*ChainStore, *sql.DB, []*SettlementReceipt) {
	t.Helper()
	cs, db := setupChain(t)

	var prev string
	for i := 1; i <= 5; i++ {
		r := makeSignedReceipt(t, "agent-a", "agent-b", prev, fmt.Sprintf("adv-contract-%d", i), signer)
		if err := cs.SaveReceipt(context.Background(), r); err != nil {
			t.Fatalf("seed receipt %d: %v", i, err)
		}
		prev = chainLinkHash(r.ExecutorSignature)
	}

	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 5 {
		t.Fatalf("expected 5 seeded receipts, got %d", len(chain))
	}
	return cs, db, chain
}

// assertForgeryRejected fails the test unless verification detects every forged
// receipt (Valid=false and ExecutorSignatureValid=false).
func assertForgeryRejected(t *testing.T, label string, chain []*SettlementReceipt, executorPub []byte) {
	t.Helper()
	results, err := VerifyChainIntegrity(chain, executorPub, nil)
	if err != nil {
		t.Fatalf("[%s] VerifyChainIntegrity returned error: %v", label, err)
	}
	for i, res := range results {
		t.Logf("[%s] idx=%d id=%s seq=%d Valid=%v SeqValid=%v PrevHashValid=%v ExecSigValid=%v err=%q",
			label, res.Index, res.ReceiptID, res.SequenceNumber, res.Valid,
			res.SequenceValid, res.PreviousHashValid, res.ExecutorSignatureValid, res.Error)
		if res.Valid {
			t.Errorf("[%s] SECURITY: forged chain accepted at index %d (id=%s)", label, i, res.ReceiptID)
		}
		if res.ExecutorSignatureValid {
			t.Errorf("[%s] SECURITY: executor signature still valid after rewriting the chain link at index %d", label, i)
		}
	}
}

// TestAdversarialChainRewrite_DeleteMiddleReceipt deletes receipt 3 from a valid
// 5-receipt chain and rewrites `previous_receipt_hash` and `seq` of the surviving
// successors so they stay contiguous. Once previous_receipt_hash is part of the
// signed content (issue #43), every rewritten receipt must fail verification.
func TestAdversarialChainRewrite_DeleteMiddleReceipt(t *testing.T) {
	t.Skip("limitación conocida: issue #43 (la firma del ejecutor no cubre previous_receipt_hash)")

	_, signer := makeTestSigners(t)
	cs, db, chain := seedValidChain5(t, signer)

	if _, err := db.Exec("DELETE FROM receipts WHERE receipt_id = ?", chain[2].ReceiptID); err != nil {
		t.Fatalf("delete receipt 3: %v", err)
	}

	kept := []*SettlementReceipt{chain[0], chain[1], chain[3], chain[4]}
	for i := 1; i < len(kept); i++ {
		kept[i].PreviousReceiptHash = chainLinkHash(kept[i-1].ExecutorSignature)
		rewriteStoredReceipt(t, db, kept[i], int64(i+1))
	}

	forged, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain after deletion: %v", err)
	}
	if len(forged) != 4 {
		t.Fatalf("expected 4 receipts after deletion, got %d", len(forged))
	}
	assertForgeryRejected(t, "delete-middle", forged, signer.PublicKey())
}

// TestAdversarialChainRewrite_SwapAdjacentReceipts swaps receipts 2 and 3 from a
// valid 5-receipt chain and rewrites `previous_receipt_hash` and `seq` so the
// forged order is internally consistent. Once previous_receipt_hash is part of
// the signed content (issue #43), the swap must fail verification.
func TestAdversarialChainRewrite_SwapAdjacentReceipts(t *testing.T) {
	t.Skip("limitación conocida: issue #43 (la firma del ejecutor no cubre previous_receipt_hash)")

	_, signer := makeTestSigners(t)
	cs, db, chain := seedValidChain5(t, signer)

	swapped := []*SettlementReceipt{chain[0], chain[2], chain[1], chain[3], chain[4]}

	// Shift every seq first so the UNIQUE (emitter, executor, seq) index does not
	// collide while the final contiguous numbering is applied.
	if _, err := db.Exec("UPDATE receipts SET seq = seq + 1000"); err != nil {
		t.Fatalf("shift seq: %v", err)
	}
	for i := 1; i < len(swapped); i++ {
		swapped[i].PreviousReceiptHash = chainLinkHash(swapped[i-1].ExecutorSignature)
	}
	for i, r := range swapped {
		rewriteStoredReceipt(t, db, r, int64(i+1))
	}

	forged, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain after swap: %v", err)
	}
	if len(forged) != 5 {
		t.Fatalf("expected 5 receipts after swap, got %d", len(forged))
	}
	assertForgeryRejected(t, "swap-2-3", forged, signer.PublicKey())
}
