package testscenario

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// TestChain_ThreeReceiptsChainLinking verifies that a chain of 3 receipts
// maintains correct SHA-256 chain links throughout.
func TestChain_ThreeReceiptsChainLinking(t *testing.T) {
	dir := t.TempDir()
	if err := initGitRepo(dir); err != nil {
		t.Fatalf("initGitRepo: %v", err)
	}

	aSigner, _ := signing.GenerateSigner("agent-a")
	bSigner, _ := signing.GenerateSigner("agent-b")

	dbDir := t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(dbDir, "chain.db"))
	defer db.Close()
	db.SetMaxOpenConns(1)
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())

	evaluator := settlement.NewEvaluator(dir)
	engine, _ := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:       evaluator,
		ChainStore:     cs,
		ExecutorSigner: bSigner,
		RemediationMax: 0,
	})

	ctx := context.Background()
	receipts := make([]*receipt.SettlementReceipt, 3)

	for i := 0; i < 3; i++ {
		env := makeEnvelope(dir, aSigner, "contract-chain-"+fmt.Sprint(i+1))

		out, err := engine.Settle(ctx, settlement.SettlementInput{
			Envelope:        env,
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: "agent-b",
		})
		if err != nil {
			t.Fatalf("Settle #%d: %v", i+1, err)
		}
		receipts[i] = out.Receipt

		// Signatures must be valid.
		h, _ := receipt.ComputeReceiptHash(receipts[i])
		if err := signing.Verify(bSigner.PublicKey(), []byte(h), receipts[i].ExecutorSignature); err != nil {
			t.Errorf("receipt #%d executor sig invalid: %v", i+1, err)
		}

		// First receipt must have empty prev_hash.
		if i == 0 && receipts[i].PreviousReceiptHash != "" {
			t.Errorf("receipt #1 prev_hash = %q, want empty", receipts[i].PreviousReceiptHash)
		}

		// Subsequent receipts must have correct SHA-256(prev_sig) chain link.
		if i > 0 {
			expected := sha256.Sum256([]byte(receipts[i-1].ExecutorSignature))
			expectedHex := hex.EncodeToString(expected[:])
			if receipts[i].PreviousReceiptHash != expectedHex {
				t.Errorf("receipt #%d prev_hash = %q, want SHA-256(rec#%d.sig) = %q",
					i+1, receipts[i].PreviousReceiptHash, i, expectedHex)
			}
		}

		t.Logf("Receipt #%d: %s | prev_hash=%s", i+1, receipts[i].ReceiptID,
			truncate(receipts[i].PreviousReceiptHash, 12))
	}

	// VerifyChain must validate all 3 receipts.
	results, err := cs.VerifyChain(ctx, "agent-a", "agent-b",
		bSigner.PublicKey(), aSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("VerifyChain returned %d, want 3", len(results))
	}
	for i, r := range results {
		if !r.Valid {
			t.Errorf("chain[%d] (%s) valid=false: %s", i, r.ReceiptID, r.Error)
		}
		if !r.PreviousHashValid {
			t.Errorf("chain[%d] (%s) prev_hash_invalid: %s", i, r.ReceiptID, r.Error)
		}
		if !r.ExecutorSignatureValid {
			t.Errorf("chain[%d] (%s) sig_invalid: %s", i, r.ReceiptID, r.Error)
		}
	}

	t.Log("=== 3-receipt chain PASSED ===")
}

// TestChain_TamperDetection verifies that VerifyChain detects any tampering
// with persisted receipts (altered previous_receipt_hash or signatures).
func TestChain_TamperDetection(t *testing.T) {
	dir := t.TempDir()
	if err := initGitRepo(dir); err != nil {
		t.Fatalf("initGitRepo: %v", err)
	}

	aSigner, _ := signing.GenerateSigner("agent-a")
	bSigner, _ := signing.GenerateSigner("agent-b")

	dbDir := t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(dbDir, "chain.db"))
	defer db.Close()
	db.SetMaxOpenConns(1)
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())

	evaluator := settlement.NewEvaluator(dir)
	engine, _ := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:       evaluator,
		ChainStore:     cs,
		ExecutorSigner: bSigner,
		RemediationMax: 0,
	})

	ctx := context.Background()

	// Emit 2 receipts.
	for i := 0; i < 2; i++ {
		env := makeEnvelope(dir, aSigner, "contract-tamper-"+fmt.Sprint(i+1))
		out, _ := engine.Settle(ctx, settlement.SettlementInput{
			Envelope:        env,
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: "agent-b",
		})
		t.Logf("Receipt #%d: %s", i+1, out.Receipt.ReceiptID)
	}

	// ─── Tamper Test 1: Corrupt previous_receipt_hash of receipt #2 ───
	t.Run("corrupt_prev_hash", func(t *testing.T) {
		chain, _ := cs.GetChain(ctx, "agent-a", "agent-b")
		if len(chain) < 2 {
			t.Skip("need at least 2 receipts")
		}

		// Corrupt the second receipt's prev_hash in the DB.
		corruptedHash := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		// Corrupt prev_hash by reading the raw JSON, corrupting the field,
		// and writing it back. REPLACE fails because JSON stores the hash with quotes.
		rec2 := chain[1]
		corruptedRec2 := *rec2
		corruptedRec2.PreviousReceiptHash = corruptedHash
		corruptedJSON, err := json.Marshal(&corruptedRec2)
		if err != nil {
			t.Fatalf("marshal corrupted receipt: %v", err)
		}
		_, err = db.Exec(
			"UPDATE receipts SET data = ? WHERE receipt_id = ?",
			string(corruptedJSON),
			rec2.ReceiptID,
		)
		if err != nil {
			t.Fatalf("update corrupted receipt: %v", err)
		}

		results, err := cs.VerifyChain(ctx, "agent-a", "agent-b",
			bSigner.PublicKey(), aSigner.PublicKey())
		if err != nil {
			t.Fatalf("VerifyChain: %v", err)
		}

		// Receipt #1 should still be valid (not affected by #2's corruption).
		if !results[0].Valid {
			t.Errorf("receipt #1 should be valid (prev_hash not tampered)")
		}
		// Receipt #2 should fail prev_hash verification.
		if results[1].PreviousHashValid {
			t.Errorf("receipt #2 prev_hash should be invalid")
		}
		if results[1].Valid {
			t.Errorf("receipt #2 should be invalid (prev_hash tampered)")
		}
		t.Logf("Tamper detected: receipt #1 valid=%v, #2 prev_hash_valid=%v",
			results[0].Valid, results[1].PreviousHashValid)
	})

	// ─── Tamper Test 2: Corrupt executor_signature of receipt #1 ───
	t.Run("corrupt_signature", func(t *testing.T) {
		// Re-emit clean receipts.
		cs.DeleteAll(ctx, "agent-a", "agent-b")
		for i := 0; i < 2; i++ {
			env := makeEnvelope(dir, aSigner, "contract-sig-tamper-"+fmt.Sprint(i+1))
			engine.Settle(ctx, settlement.SettlementInput{
				Envelope:        env,
				EmitterAgentID:  "agent-a",
				ExecutorAgentID: "agent-b",
			})
		}

		chain, _ := cs.GetChain(ctx, "agent-a", "agent-b")
		if len(chain) < 2 {
			t.Skip("need at least 2 receipts")
		}

		// Corrupt receipt #1 signature by reading the raw JSON, corrupting a middle byte
		// of the decoded signature bytes, and writing it back.
		origRec := chain[0]
		corruptedRec := *origRec
		rawSig, err := base64.RawURLEncoding.DecodeString(origRec.ExecutorSignature)
		if err != nil {
			t.Fatalf("decode executor signature: %v", err)
		}
		if len(rawSig) > 10 {
			rawSig[len(rawSig)/2] ^= 0xFF
		}
		corruptedRec.ExecutorSignature = base64.RawURLEncoding.EncodeToString(rawSig)
		corruptedJSON, err := json.Marshal(&corruptedRec)
		if err != nil {
			t.Fatalf("marshal corrupted receipt: %v", err)
		}
		_, err = db.Exec(
			"UPDATE receipts SET data = ? WHERE receipt_id = ?",
			string(corruptedJSON),
			origRec.ReceiptID,
		)
		if err != nil {
			t.Fatalf("update corrupted receipt: %v", err)
		}

		results, err := cs.VerifyChain(ctx, "agent-a", "agent-b",
			bSigner.PublicKey(), aSigner.PublicKey())
		if err != nil {
			t.Fatalf("VerifyChain: %v", err)
		}

		// Receipt #1 signature verification should fail.
		if results[0].ExecutorSignatureValid {
			t.Errorf("receipt #1 sig should be invalid after tampering")
		}
		// Receipt #2 chain link should also fail (its prev_hash was SHA-256 of the original sig).
		if results[1].PreviousHashValid {
			t.Errorf("receipt #2 prev_hash should be invalid (upstream sig tampered)")
		}
		// Receipt #2 overall validity should also fail.
		if results[1].Valid {
			t.Errorf("receipt #2 should be invalid (chain broken by upstream sig tampering)")
		}
		t.Logf("Tamper detected: #1 sig_valid=%v, #2 prev_hash_valid=%v",
			results[0].ExecutorSignatureValid, results[1].PreviousHashValid)
	})
}

// TestChain_EmptiesChain verifies that an empty chain and a 1-receipt chain
// are handled correctly by VerifyChain.
func TestChain_EdgeCases(t *testing.T) {
	aSigner, _ := signing.GenerateSigner("agent-a")
	bSigner, _ := signing.GenerateSigner("agent-b")

	t.Run("empty_chain", func(t *testing.T) {
		dbDir := t.TempDir()
		db, _ := sql.Open("sqlite", filepath.Join(dbDir, "empty.db"))
		defer db.Close()
		db.SetMaxOpenConns(1)
		cs := receipt.NewChainStore(db)
		cs.InitSchema(context.Background())

		_, err := cs.VerifyChain(context.Background(), "agent-a", "agent-b",
			bSigner.PublicKey(), aSigner.PublicKey())
		if err == nil {
			t.Errorf("VerifyChain on empty chain: expected error, got nil")
		} else {
			t.Logf("VerifyChain on empty chain correctly returned error: %v", err)
		}
	})

	t.Run("one_receipt_valid", func(t *testing.T) {
		dir := t.TempDir()
		if err := initGitRepo(dir); err != nil {
			t.Fatalf("initGitRepo: %v", err)
		}

		dbDir := t.TempDir()
		db, _ := sql.Open("sqlite", filepath.Join(dbDir, "one.db"))
		defer db.Close()
		db.SetMaxOpenConns(1)
		cs := receipt.NewChainStore(db)
		cs.InitSchema(context.Background())

		evaluator := settlement.NewEvaluator(dir)
		engine, _ := settlement.NewEngine(settlement.EngineConfig{
			Evaluator:       evaluator,
			ChainStore:     cs,
			ExecutorSigner: bSigner,
			RemediationMax: 0,
		})

		env := makeEnvelope(dir, aSigner, "contract-one")
		out, _ := engine.Settle(context.Background(), settlement.SettlementInput{
			Envelope:        env,
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: "agent-b",
		})

		results, err := cs.VerifyChain(context.Background(), "agent-a", "agent-b",
			bSigner.PublicKey(), aSigner.PublicKey())
		if err != nil {
			t.Fatalf("VerifyChain: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("one-receipt chain should return 1 result, got %d", len(results))
		}
		if !results[0].Valid {
			t.Errorf("receipt should be valid: %s", results[0].Error)
		}
		// First receipt: PreviousHashValid=true means empty prev_hash is correct.
		if !results[0].PreviousHashValid {
			t.Errorf("first receipt prev_hash should be valid (empty hash is correct)")
		}
		if results[0].ReceiptID != out.Receipt.ReceiptID {
			t.Errorf("receipt ID mismatch: %s vs %s", results[0].ReceiptID, out.Receipt.ReceiptID)
		}
	})
}

// makeEnvelope creates a minimal valid envelope signed by agent-a.
func makeEnvelope(workspace string, signer *signing.BasicSigner, id string) *envelope.CognitiveTaskEnvelope {
	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      id,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: workspace,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "always-pass",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "true",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds:  120,
		MaxRemediations: 0,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}
	hash, _ := envelope.ComputeEnvelopeHash(env)
	env.EnvelopeHash = hash

	signable := *env
	signable.EmitterSignature = ""
	data, _ := jcs.Marshal(&signable)
	sig, _ := signer.Sign(data)
	env.EmitterSignature = sig

	return env
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
