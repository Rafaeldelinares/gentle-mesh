package receipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Helper to create a signed receipt with specified prevHash and contract ID.
func makeSignedReceipt(t *testing.T, emitterID, executorID, prevHash, contractID string, signer signing.Signer) *SettlementReceipt {
	t.Helper()
	now := time.Now().UTC()
	r := &SettlementReceipt{
		ProtocolVersion:     CurrentProtocolVersion,
		MeshID:              "test-mesh",
		ReceiptID:           fmt.Sprintf("rec-%s-%d", contractID, now.UnixNano()),
		ContractID:          contractID,
		EnvelopeHash:        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EmitterAgentID:      emitterID,
		ExecutorAgentID:     executorID,
		Verdict:             VerdictSettledClean,
		PreviousReceiptHash: prevHash,
		ExecutorSignedAt:    now,
	}
	if err := SignReceipt(r, signer); err != nil {
		t.Fatalf("SignReceipt failed: %v", err)
	}
	return r
}

// ─────────────────────────────────────────────────────────────────
// S7: Validación al escribir (SaveReceipt)
// ─────────────────────────────────────────────────────────────────

func TestS7_SaveReceipt_FirstReceipt_NonEmptyPrevHash_Rejected(t *testing.T) {
	cs, _ := setupChain(t)
	_, signer := makeTestSigners(t)

	// First receipt for pair (agent-a, agent-b) with non-empty previous_receipt_hash must be rejected.
	r := makeSignedReceipt(t, "agent-a", "agent-b", "some-unfounded-prev-hash", "c-1", signer)

	err := cs.SaveReceipt(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when saving first receipt with non-empty PreviousReceiptHash, got nil")
	}
	if !errors.Is(err, ErrChainBroken) && !errors.Is(err, ErrInvalidPreviousHash) {
		t.Fatalf("expected ErrChainBroken / ErrInvalidPreviousHash, got: %v", err)
	}
}

func TestS7_SaveReceipt_SecondReceipt_WrongPrevHash_Rejected(t *testing.T) {
	cs, _ := setupChain(t)
	_, signer := makeTestSigners(t)

	// First receipt succeeds with empty PreviousReceiptHash.
	r1 := makeSignedReceipt(t, "agent-a", "agent-b", "", "c-1", signer)
	if err := cs.SaveReceipt(context.Background(), r1); err != nil {
		t.Fatalf("SaveReceipt(r1) failed: %v", err)
	}

	// Second receipt with wrong previous hash must be rejected.
	r2Wrong := makeSignedReceipt(t, "agent-a", "agent-b", "wrong-hash-0000000000000000000000000000000000000000000000000000000", "c-2", signer)
	err := cs.SaveReceipt(context.Background(), r2Wrong)
	if err == nil {
		t.Fatal("expected error when saving receipt with mismatched PreviousReceiptHash, got nil")
	}
	if !errors.Is(err, ErrChainBroken) && !errors.Is(err, ErrInvalidPreviousHash) {
		t.Fatalf("expected ErrChainBroken / ErrInvalidPreviousHash, got: %v", err)
	}

	// Second receipt with empty PreviousReceiptHash must also be rejected because it is not first.
	r2Empty := makeSignedReceipt(t, "agent-a", "agent-b", "", "c-2-empty", signer)
	err = cs.SaveReceipt(context.Background(), r2Empty)
	if err == nil {
		t.Fatal("expected error when saving subsequent receipt with empty PreviousReceiptHash, got nil")
	}
	if !errors.Is(err, ErrChainBroken) && !errors.Is(err, ErrInvalidPreviousHash) {
		t.Fatalf("expected ErrChainBroken / ErrInvalidPreviousHash, got: %v", err)
	}

	// Second receipt with correct previous hash must succeed.
	h := sha256.Sum256([]byte(r1.ExecutorSignature))
	correctPrevHash := hex.EncodeToString(h[:])
	r2Correct := makeSignedReceipt(t, "agent-a", "agent-b", correctPrevHash, "c-2-correct", signer)
	if err := cs.SaveReceipt(context.Background(), r2Correct); err != nil {
		t.Fatalf("SaveReceipt(r2Correct) with valid link failed: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────
// R4: Secuencia monótona estricta
// ─────────────────────────────────────────────────────────────────

func TestR4_SaveReceipt_SequenceMonotonicity(t *testing.T) {
	cs, _ := setupChain(t)
	_, signer := makeTestSigners(t)

	// Save 3 receipts in sequence.
	var lastSig string
	for i := 1; i <= 3; i++ {
		var prevHash string
		if i > 1 {
			h := sha256.Sum256([]byte(lastSig))
			prevHash = hex.EncodeToString(h[:])
		}
		r := makeSignedReceipt(t, "agent-a", "agent-b", prevHash, fmt.Sprintf("contract-%d", i), signer)
		if err := cs.SaveReceipt(context.Background(), r); err != nil {
			t.Fatalf("SaveReceipt(%d) failed: %v", i, err)
		}
		if r.SequenceNumber != int64(i) {
			t.Fatalf("receipt %d has SequenceNumber %d, want %d", i, r.SequenceNumber, i)
		}
		lastSig = r.ExecutorSignature
	}

	// Retrieve chain and verify stored sequence numbers.
	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain failed: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 receipts in chain, got %d", len(chain))
	}
	for i, r := range chain {
		if r.SequenceNumber != int64(i+1) {
			t.Errorf("chain[%d] SequenceNumber = %d, want %d", i, r.SequenceNumber, i+1)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Concurrencia: 50+ escrituras concurrentes con -race
// ─────────────────────────────────────────────────────────────────

func TestR4_SaveReceipt_Concurrency_50Goroutines_Race(t *testing.T) {
	cs, _ := setupChain(t)
	_, signer := makeTestSigners(t)

	const totalWrites = 50
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	// Launch 50 concurrent goroutines competing to append receipts.
	for i := 0; i < totalWrites; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier // synchronize start for maximum contention

			contractID := fmt.Sprintf("contract-c-%d", workerID)

			// Retry loop: optimistic concurrency / CAS on hash chain.
			for attempt := 0; attempt < 500; attempt++ {
				last, err := cs.GetLastReceipt(context.Background(), "agent-a", "agent-b")
				if err != nil && !errors.Is(err, ErrReceiptNotFound) {
					t.Errorf("worker %d: unexpected GetLastReceipt error: %v", workerID, err)
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
					// Successfully persisted to chain!
					return
				}

				if errors.Is(saveErr, ErrChainBroken) ||
					errors.Is(saveErr, ErrInvalidPreviousHash) ||
					errors.Is(saveErr, ErrSequenceConflict) {
					// Another worker advanced the chain; retry with latest tail.
					time.Sleep(time.Duration(attempt%5) * time.Millisecond)
					continue
				}

				// Any other error is a failure.
				t.Errorf("worker %d: unexpected SaveReceipt error: %v", workerID, saveErr)
				return
			}
			t.Errorf("worker %d failed to commit receipt within max attempts", workerID)
		}(i)
	}

	// Trigger all goroutines simultaneously.
	close(startBarrier)
	wg.Wait()

	// Verify all 50 receipts were saved without gaps, duplicates, or broken links.
	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain failed: %v", err)
	}
	if len(chain) != totalWrites {
		t.Fatalf("expected exactly %d receipts in chain, got %d", totalWrites, len(chain))
	}

	// Verify strictly monotonic sequences 1..50 and continuous hash chain.
	for i, r := range chain {
		expectedSeq := int64(i + 1)
		if r.SequenceNumber != expectedSeq {
			t.Errorf("receipt[%d] SequenceNumber = %d, want %d", i, r.SequenceNumber, expectedSeq)
		}

		if i == 0 {
			if r.PreviousReceiptHash != "" {
				t.Errorf("receipt[0] has non-empty PreviousReceiptHash: %q", r.PreviousReceiptHash)
			}
		} else {
			prevSig := chain[i-1].ExecutorSignature
			h := sha256.Sum256([]byte(prevSig))
			expectedHash := hex.EncodeToString(h[:])
			if r.PreviousReceiptHash != expectedHash {
				t.Errorf("receipt[%d] PreviousReceiptHash mismatch: got %q, want %q",
					i, r.PreviousReceiptHash, expectedHash)
			}
		}
	}

	// Verify entire chain with independent verifier.
	results, err := cs.VerifyChain(context.Background(), "agent-a", "agent-b", signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChain error: %v", err)
	}
	if len(results) != totalWrites {
		t.Fatalf("VerifyChain returned %d results, want %d", len(results), totalWrites)
	}
	for i, res := range results {
		if !res.Valid {
			t.Errorf("result[%d] invalid: %s", i, res.Error)
		}
		if !res.SequenceValid {
			t.Errorf("result[%d] SequenceValid is false", i)
		}
		if !res.PreviousHashValid {
			t.Errorf("result[%d] PreviousHashValid is false", i)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Verificador independiente de manipulación
// ─────────────────────────────────────────────────────────────────

func TestChainIntegrityVerifier_TamperingDetection(t *testing.T) {
	_, signer := makeTestSigners(t)

	// Build a valid chain of 3 receipts.
	var chain []*SettlementReceipt
	var lastSig string
	for i := 1; i <= 3; i++ {
		var prevHash string
		if i > 1 {
			h := sha256.Sum256([]byte(lastSig))
			prevHash = hex.EncodeToString(h[:])
		}
		r := makeSignedReceipt(t, "agent-a", "agent-b", prevHash, fmt.Sprintf("contract-%d", i), signer)
		r.SequenceNumber = int64(i)
		chain = append(chain, r)
		lastSig = r.ExecutorSignature
	}

	// 1. Valid chain passes.
	results, err := VerifyChainIntegrity(chain, signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity valid chain error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, res := range results {
		if !res.Valid {
			t.Fatalf("expected receipt %d to be valid, got error: %s", i, res.Error)
		}
	}

	// 2. Tampered receipt (modified content): hash does not match signature.
	t.Run("ModifiedReceipt_HashMismatch", func(t *testing.T) {
		tamperedChain := make([]*SettlementReceipt, len(chain))
		for i, r := range chain {
			cpy := *r
			tamperedChain[i] = &cpy
		}
		// Modify verdict of receipt 2.
		tamperedChain[1].Verdict = VerdictFailed

		res, err := VerifyChainIntegrity(tamperedChain, signer.PublicKey(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res[1].Valid {
			t.Error("expected tampered receipt to be invalid, but Valid was true")
		}
		if res[1].ExecutorSignatureValid {
			t.Error("expected ExecutorSignatureValid to be false for modified receipt")
		}
	})

	// 3. Deleted receipt (gap in sequence).
	t.Run("DeletedReceipt_SequenceGap", func(t *testing.T) {
		// Delete receipt 2: chain only contains receipt 1 and receipt 3.
		gapChain := []*SettlementReceipt{chain[0], chain[2]}

		res, err := VerifyChainIntegrity(gapChain, signer.PublicKey(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Receipt at index 1 has SequenceNumber 3 (expected 2) and broken previous hash link.
		if res[1].Valid {
			t.Error("expected gap chain receipt 1 to be invalid, but Valid was true")
		}
		if res[1].SequenceValid {
			t.Error("expected SequenceValid to be false due to sequence gap")
		}
		if res[1].PreviousHashValid {
			t.Error("expected PreviousHashValid to be false due to missing link")
		}
	})

	// 4. Out-of-order receipt.
	t.Run("OutOfOrderReceipt", func(t *testing.T) {
		// Reorder: receipt 1, receipt 3, receipt 2.
		reorderedChain := []*SettlementReceipt{chain[0], chain[2], chain[1]}

		res, err := VerifyChainIntegrity(reorderedChain, signer.PublicKey(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res[1].Valid {
			t.Error("expected out-of-order receipt 1 to be invalid")
		}
		if res[1].SequenceValid {
			t.Error("expected SequenceValid to be false for out-of-order receipt")
		}
		if res[2].Valid {
			t.Error("expected out-of-order receipt 2 to be invalid")
		}
		if res[2].SequenceValid {
			t.Error("expected SequenceValid to be false for out-of-order receipt")
		}
	})
}
