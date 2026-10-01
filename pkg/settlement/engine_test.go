package settlement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

func setupEngine(t *testing.T) (*Engine, *receipt.ChainStore, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	cs := receipt.NewChainStore(db)
	if err := cs.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	ev := NewEvaluator(dir)
	signer, err := signing.GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}
	eng, err := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng, cs, func() { db.Close() }
}

// ─────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────

func validEnvelope() *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-001",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "test-pass",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "true",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}
}

func failingEnvelope() *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-fail",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "fail-test",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "false",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}
}

func errorEnvelope() *envelope.CognitiveTaskEnvelope {
	return &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-error",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "bad-cmd",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "nonexistent-binary-xyz",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}
}

// ─────────────────────────────────────────────────────────────────
// NewEngine
// ─────────────────────────────────────────────────────────────────

func TestNewEngine_MissingEvaluator(t *testing.T) {
	cs := receipt.NewChainStore(nil)
	s, _ := signing.GenerateSigner("x")
	_, err := NewEngine(EngineConfig{
		Evaluator:      nil,
		ChainStore:     cs,
		ExecutorSigner: s,
	})
	if err == nil {
		t.Error("NewEngine(nil evaluator) should fail")
	}
}

func TestNewEngine_MissingChainStore(t *testing.T) {
	s, _ := signing.GenerateSigner("x")
	_, err := NewEngine(EngineConfig{
		Evaluator:      NewEvaluator("/tmp"),
		ChainStore:     nil,
		ExecutorSigner: s,
	})
	if err == nil {
		t.Error("NewEngine(nil chain store) should fail")
	}
}

func TestNewEngine_MissingSigner(t *testing.T) {
	cs := receipt.NewChainStore(nil)
	_, err := NewEngine(EngineConfig{
		Evaluator:      NewEvaluator("/tmp"),
		ChainStore:     cs,
		ExecutorSigner: nil,
	})
	if err == nil {
		t.Error("NewEngine(nil signer) should fail")
	}
}

func TestNewEngine_DefaultsRemediation(t *testing.T) {
	cs := receipt.NewChainStore(nil)
	s, _ := signing.GenerateSigner("x")
	eng, err := NewEngine(EngineConfig{
		Evaluator:      NewEvaluator("/tmp"),
		ChainStore:     cs,
		ExecutorSigner: s,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if eng.remMax != DefaultRemediationLimit {
		t.Errorf("remMax = %d, want %d", eng.remMax, DefaultRemediationLimit)
	}
}

// ─────────────────────────────────────────────────────────────────
// Settle — happy path
// ─────────────────────────────────────────────────────────────────

func TestSettle_FirstReceipt_AllPass(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	in := SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	}

	out, err := eng.Settle(context.Background(), in)
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt == nil {
		t.Fatal("Receipt is nil")
	}
	if out.Receipt.Verdict != receipt.VerdictSettledClean {
		t.Errorf("Verdict = %v, want SETTLED_CLEAN", out.Receipt.Verdict)
	}
	if out.Receipt.PreviousReceiptHash != "" {
		t.Errorf("First receipt PreviousReceiptHash = %q, want empty", out.Receipt.PreviousReceiptHash)
	}
	if out.Receipt.ExecutorSignature == "" {
		t.Error("ExecutorSignature is empty")
	}
	if out.RemediationUsed != 0 {
		t.Errorf("RemediationUsed = %d, want 0", out.RemediationUsed)
	}
}

func TestSettle_SecondReceipt_ChainLink(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	in1 := SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	}
	out1, _ := eng.Settle(context.Background(), in1)

	// Second receipt with different assertions.
	in2 := SettlementInput{
		Envelope: &envelope.CognitiveTaskEnvelope{
			ProtocolVersion: envelope.CurrentProtocolVersion,
			MeshID:          "gentle-mesh-dev",
			EnvelopeID:      "contract-002",
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: "agent-b",
			Territory: envelope.Territory{
				Repository:    "github.com/gentleman-programming/gentle-mesh",
				Branch:        "main",
				WorkspacePath: "/tmp/workspace",
			},
			Assertions: []envelope.Assertion{
				{
					ID:   "test-pass2",
					Type: envelope.AssertionCommandExitCode,
					Params: envelope.AssertionParams{
						Command:          "true",
						ExpectedExitCode: 0,
					},
				},
			},
			TimeoutSeconds: 300,
		},
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	}
	out2, err := eng.Settle(context.Background(), in2)
	if err != nil {
		t.Fatalf("Settle(out2) error: %v", err)
	}

	// Verify chain link.
	h := sha256.Sum256([]byte(out1.Receipt.ExecutorSignature))
	prevHash := hex.EncodeToString(h[:])
	if out2.Receipt.PreviousReceiptHash != prevHash {
		t.Errorf("Chain link mismatch: got %q, want %q",
			out2.Receipt.PreviousReceiptHash, prevHash)
	}
}

func TestSettle_PersistedToChain(t *testing.T) {
	eng, cs, cleanup := setupEngine(t)
	defer cleanup()

	_, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	count, _ := cs.Count(context.Background(), "agent-a", "agent-b")
	if count != 1 {
		t.Errorf("Count = %d, want 1", count)
	}

	chain, _ := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if len(chain) != 1 {
		t.Errorf("Chain length = %d, want 1", len(chain))
	}
	if chain[0].Verdict != receipt.VerdictSettledClean {
		t.Errorf("Chain[0].Verdict = %v, want SETTLED_CLEAN", chain[0].Verdict)
	}
}

// ─────────────────────────────────────────────────────────────────
// Settle — verdict
// ─────────────────────────────────────────────────────────────────

func TestSettle_AssertionFails_VerdictFailed(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        failingEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want VERDICT_FAILED", out.Receipt.Verdict)
	}
	if out.RemediationUsed != 1 {
		t.Errorf("RemediationUsed = %d, want 1", out.RemediationUsed)
	}
}

func TestSettle_AssertionError_VerdictFailed(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        errorEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want VERDICT_FAILED", out.Receipt.Verdict)
	}
}

// ─────────────────────────────────────────────────────────────────
// Negative tests — failure and remediation
// ─────────────────────────────────────────────────────────────────

// TestSettle_ExitCodeMismatch verifies that when the command exits with
// a different code than expected, the assertion fails and verdict is FAILED.
func TestSettle_ExitCodeMismatch(t *testing.T) {
	dir := t.TempDir()

	// Create envelope that expects exit code 0, but command exits with 1.
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-exit-mismatch",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "exit-1",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sh -c 'exit 1'",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(out.Receipt.Assertions))
	}
	if out.Receipt.Assertions[0].Result != receipt.ResultFail {
		t.Errorf("Assertion result = %v, want FAIL", out.Receipt.Assertions[0].Result)
	}
	if out.Receipt.Assertions[0].Evidence.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", out.Receipt.Assertions[0].Evidence.ExitCode)
	}
}

// TestSettle_FileHashMismatch verifies that when the file's SHA-256 does not
// match the expected hash, the assertion fails.
func TestSettle_FileHashMismatch(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "data.txt")
	os.WriteFile(f, []byte("actual content"), 0644)

	// Envelope expects a WRONG hash — the assertion must fail.
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-hash-mismatch",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "hash-check",
				Type: envelope.AssertionFileHashEquals,
				Params: envelope.AssertionParams{
					FilePath:       "data.txt",
					ExpectedSHA256: "a948904f2f0f479b8f8564cbf12dae62c683b2a5f1677e1af51a92d4ed30a89f", // wrong hash
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(out.Receipt.Assertions))
	}
	if out.Receipt.Assertions[0].Result != receipt.ResultFail {
		t.Errorf("Assertion result = %v, want FAIL", out.Receipt.Assertions[0].Result)
	}
	// Actual hash should be populated.
	if out.Receipt.Assertions[0].Evidence.ActualSHA256 == "" {
		t.Error("ActualSHA256 should be populated")
	}
	// Expected hash should match the wrong value we sent.
	if out.Receipt.Assertions[0].Evidence.ExpectedSHA256 != "a948904f2f0f479b8f8564cbf12dae62c683b2a5f1677e1af51a92d4ed30a89f" {
		t.Errorf("ExpectedSHA256 mismatch")
	}
}

// TestSettle_AssertionTimeout verifies that an assertion that times out
// produces a SKIP result and the verdict is FAILED.
func TestSettle_AssertionTimeout(t *testing.T) {
	dir := t.TempDir()

	// Envelope with a command that sleeps longer than its timeout.
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-timeout",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "sleepy",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sleep 5",
					ExpectedExitCode: 0,
					TimeoutSeconds:   1, // times out after 1 second
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := eng.Settle(ctx, SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED (timeout)", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(out.Receipt.Assertions))
	}
	// A timeout should produce a SKIP result (or FAIL depending on implementation).
	result := out.Receipt.Assertions[0].Result
	if result != receipt.ResultSkip && result != receipt.ResultFail {
		t.Errorf("Assertion result = %v, want SKIP or FAIL", result)
	}
}

// TestSettle_RemediationMaxReached verifies that when all remediation
// attempts are exhausted without passing assertions, verdict is FAILED.
func TestSettle_RemediationMaxReached(t *testing.T) {
	dir := t.TempDir()

	// Create envelope that always fails (exit code 1).
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-remed-fail",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "always-fail",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sh -c 'exit 1'",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
		RemediationMax: 2, // try up to 2 times
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	// With remMax=2, the engine tries 2 remediation cycles.
	if out.RemediationUsed != 2 {
		t.Errorf("RemediationUsed = %d, want 2", out.RemediationUsed)
	}
}

// TestSettle_ErrorRemediation verifies that command errors trigger
// the remediation loop (just like failures) and exhaust all attempts.
func TestSettle_ErrorRemediation(t *testing.T) {
	dir := t.TempDir()

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-error",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "bad-command",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "/nonexistent-binary-xyz-123",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
		RemediationMax: 2,
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	// Errors trigger remediation just like failures.
	if out.RemediationUsed != 2 {
		t.Errorf("RemediationUsed = %d, want 2 (errors trigger remediation)", out.RemediationUsed)
	}
}

// TestSettle_AllAssertionsFail verifies that when multiple assertions fail,
// the verdict is FAILED and all results are recorded.
func TestSettle_AllAssertionsFail(t *testing.T) {
	dir := t.TempDir()

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-multi-fail",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "fail-1",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sh -c 'exit 1'",
					ExpectedExitCode: 0,
				},
			},
			{
				ID:   "fail-2",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sh -c 'exit 2'",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 2 {
		t.Fatalf("Assertions length = %d, want 2", len(out.Receipt.Assertions))
	}
	for i, a := range out.Receipt.Assertions {
		if a.Result != receipt.ResultFail {
			t.Errorf("Assertions[%d] result = %v, want FAIL", i, a.Result)
		}
	}
}

// TestSettle_MixedResults verifies that when some assertions pass and some
// fail, the overall verdict is FAILED.
func TestSettle_MixedResults(t *testing.T) {
	dir := t.TempDir()

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-mixed",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "pass-test",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "true",
					ExpectedExitCode: 0,
				},
			},
			{
				ID:   "fail-test",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "sh -c 'exit 3'",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds: 300,
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})
	defer db.Close()

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want SETTLEMENT_FAILED", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 2 {
		t.Fatalf("Assertions length = %d, want 2", len(out.Receipt.Assertions))
	}
	// First passes, second fails.
	if out.Receipt.Assertions[0].Result != receipt.ResultPass {
		t.Errorf("Assertions[0] = %v, want PASS", out.Receipt.Assertions[0].Result)
	}
	if out.Receipt.Assertions[1].Result != receipt.ResultFail {
		t.Errorf("Assertions[1] = %v, want FAIL", out.Receipt.Assertions[1].Result)
	}
}

// ─────────────────────────────────────────────────────────────────
// Settle — validation
// ─────────────────────────────────────────────────────────────────

func TestSettle_NilEnvelope(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	_, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        nil,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err == nil {
		t.Error("Settle(nil envelope) should fail")
	}
}

func TestSettle_EmptyEmitterID(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	_, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "",
		ExecutorAgentID: "agent-b",
	})
	if err == nil {
		t.Error("Settle(empty emitter ID) should fail")
	}
}

func TestSettle_EmptyEnvelopeHash_ComputedAutomatically(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	env := validEnvelope()
	env.EnvelopeHash = "" // Empty hash: engine must compute it upfront and settle cleanly
	env.TimeoutSeconds = 60
	env.EmitterAgentID = "agent-a"
	env.ExecutorAgentID = "agent-b"

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle(empty envelope_hash) error = %v, want nil", err)
	}
	if out.Receipt.EnvelopeHash == "" {
		t.Error("Receipt.EnvelopeHash should not be empty")
	}
	expectedHash, _ := envelope.ComputeEnvelopeHash(env)
	if out.Receipt.EnvelopeHash != expectedHash {
		t.Errorf("Receipt.EnvelopeHash = %q, want %q", out.Receipt.EnvelopeHash, expectedHash)
	}
	if env.EnvelopeHash != "" {
		t.Error("Envelope.EnvelopeHash should not be mutated by Settle")
	}
}

func TestSettle_InputValidation_Adversarial(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	env := validEnvelope()

	// Emitter mismatch
	if _, err := eng.Settle(context.Background(), SettlementInput{Envelope: env, EmitterAgentID: "agent-other", ExecutorAgentID: "agent-b"}); err == nil {
		t.Error("Settle(emitter mismatch) should fail")
	}

	// Executor mismatch
	if _, err := eng.Settle(context.Background(), SettlementInput{Envelope: env, EmitterAgentID: "agent-a", ExecutorAgentID: "agent-other"}); err == nil {
		t.Error("Settle(executor mismatch) should fail")
	}

	// Hash mismatch
	envForged := *env
	envForged.EnvelopeHash = "forged-hash"
	if _, err := eng.Settle(context.Background(), SettlementInput{Envelope: &envForged, EmitterAgentID: "agent-a", ExecutorAgentID: "agent-b"}); err == nil {
		t.Error("Settle(envelope hash mismatch) should fail")
	}
}

func TestSettle_InvalidEnvelope_FailsBeforeEvaluating(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	env := validEnvelope()
	env.EnvelopeHash = "pre-computed-hash"
	env.ProtocolVersion = "invalid-version"

	_, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err == nil {
		t.Error("Settle(invalid envelope) should fail upfront")
	}
}

// ─────────────────────────────────────────────────────────────────
// VerifyReceipt
// ─────────────────────────────────────────────────────────────────

func TestVerifyReceipt_OK(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	out, _ := eng.Settle(context.Background(), SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})

	signer := eng.executorSigner
	err := VerifyReceipt(out.Receipt, signer.PublicKey())
	if err != nil {
		t.Errorf("VerifyReceipt error: %v", err)
	}
}

func TestVerifyReceipt_Tampered(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	out, _ := eng.Settle(context.Background(), SettlementInput{
		Envelope:        validEnvelope(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})

	out.Receipt.EnvelopeHash = "tampered"

	signer := eng.executorSigner
	err := VerifyReceipt(out.Receipt, signer.PublicKey())
	if err == nil {
		t.Error("VerifyReceipt(tampered) should fail")
	}
}

func TestVerifyReceipt_Nil(t *testing.T) {
	err := VerifyReceipt(nil, []byte{})
	if err == nil {
		t.Error("VerifyReceipt(nil) should fail")
	}
}

// ─────────────────────────────────────────────────────────────────
// computeVerdict
// ─────────────────────────────────────────────────────────────────

func TestComputeVerdict_AllPass(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultPass},
	}
	verdict, failed := eng.computeVerdict(results)
	if verdict != receipt.VerdictSettledClean {
		t.Errorf("verdict = %v, want SETTLED_CLEAN", verdict)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
}

func TestComputeVerdict_SomeFail(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultFail},
	}
	verdict, failed := eng.computeVerdict(results)
	if verdict != receipt.VerdictFailed {
		t.Errorf("verdict = %v, want VERDICT_FAILED", verdict)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

func TestComputeVerdict_HasError(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultError},
	}
	verdict, failed := eng.computeVerdict(results)
	if verdict != receipt.VerdictFailed {
		t.Errorf("verdict = %v, want VERDICT_FAILED", verdict)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

// ─────────────────────────────────────────────────────────────────
// Remediation config
// ─────────────────────────────────────────────────────────────────

func TestSettle_RemediationMax(t *testing.T) {
	dir := t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	ev := NewEvaluator(dir)
	s, _ := signing.GenerateSigner("agent-b")

	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: s,
		RemediationMax: 2,
	})
	if eng.remMax != 2 {
		t.Errorf("remMax = %d, want 2", eng.remMax)
	}
	db.Close()
}

// ─────────────────────────────────────────────────────────────────
// convertAssertions
// ─────────────────────────────────────────────────────────────────

func TestConvertAssertions(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	envA := []envelope.Assertion{
		{
			ID:   "test1",
			Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{
				Command:          "go test",
				ExpectedExitCode: 0,
			},
		},
		{
			ID:   "file1",
			Type: envelope.AssertionFileHashEquals,
			Params: envelope.AssertionParams{
				FilePath:       "README.md",
				ExpectedSHA256: "abc123",
			},
		},
	}

	sett := eng.convertAssertions(envA)
	if len(sett) != 2 {
		t.Fatalf("len(sett) = %d, want 2", len(sett))
	}

	if sett[0].ID != "test1" {
		t.Errorf("sett[0].ID = %q, want %q", sett[0].ID, "test1")
	}
	if sett[0].Type != AssertionCommandExitCode {
		t.Errorf("sett[0].Type = %v, want %v", sett[0].Type, AssertionCommandExitCode)
	}
	if sett[0].Params["command"] != "go test" {
		t.Errorf("sett[0].Params[command] = %v, want %q", sett[0].Params["command"], "go test")
	}
	if sett[0].Params["expected_code"] != 0 {
		t.Errorf("sett[0].Params[expected_code] = %v, want 0", sett[0].Params["expected_code"])
	}

	if sett[1].Params["path"] != "README.md" {
		t.Errorf("sett[1].Params[path] = %v, want %q", sett[1].Params["path"], "README.md")
	}
	if sett[1].Params["expected_hash"] != "abc123" {
		t.Errorf("sett[1].Params[expected_hash] = %v, want %q", sett[1].Params["expected_hash"], "abc123")
	}
}

// ─────────────────────────────────────────────────────────────────
// convertParams
// ─────────────────────────────────────────────────────────────────

func TestConvertParams(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	cases := []struct {
		params envelope.AssertionParams
		check  func(map[string]any)
	}{
		{
			params: envelope.AssertionParams{FilePath: "foo.txt", ExpectedSHA256: "deadbeef"},
			check: func(m map[string]any) {
				if m["path"] != "foo.txt" {
					t.Errorf("path = %v, want foo.txt", m["path"])
				}
				if m["expected_hash"] != "deadbeef" {
					t.Errorf("expected_hash = %v, want deadbeef", m["expected_hash"])
				}
			},
		},
		{
			params: envelope.AssertionParams{Command: "make test", ExpectedExitCode: 1},
			check: func(m map[string]any) {
				if m["command"] != "make test" {
					t.Errorf("command = %v, want make test", m["command"])
				}
				if m["expected_code"] != 1 {
					t.Errorf("expected_code = %v, want 1", m["expected_code"])
				}
			},
		},
		{
			params: envelope.AssertionParams{ContainsPattern: "ok", WorkingDir: "/tmp"},
			check: func(m map[string]any) {
				if m["contains"] != "ok" {
					t.Errorf("contains = %v, want ok", m["contains"])
				}
				if m["cwd"] != "/tmp" {
					t.Errorf("cwd = %v, want /tmp", m["cwd"])
				}
			},
		},
		{
			params: envelope.AssertionParams{Port: 8080},
			check: func(m map[string]any) {
				if m["port"] != 8080 {
					t.Errorf("port = %v, want 8080", m["port"])
				}
			},
		},
	}

	for _, c := range cases {
		m := eng.convertParams(c.params)
		c.check(m)
	}
}

// ─────────────────────────────────────────────────────────────────
// File-based settlement
// ─────────────────────────────────────────────────────────────────

func TestSettle_FileBasedAssertion(t *testing.T) {
	// Set up a fresh engine with a temp dir containing a test file.
	dir := t.TempDir()
	f := filepath.Join(dir, "output.txt")
	os.WriteFile(f, []byte("hello"), 0644)

	db, _ := sql.Open("sqlite", filepath.Join(dir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	defer db.Close()

	ev := NewEvaluator(dir)
	signer, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: signer,
	})

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-file",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "file-exists",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       "output.txt",
					ExpectedSHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
				},
			},
		},
		TimeoutSeconds: 300,
	}

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictSettledClean {
		t.Errorf("Verdict = %v, want SETTLED_CLEAN", out.Receipt.Verdict)
	}
	if len(out.Receipt.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(out.Receipt.Assertions))
	}
	if out.Receipt.Assertions[0].Result != receipt.ResultPass {
		t.Errorf("Assertions[0].Result = %v, want PASS", out.Receipt.Assertions[0].Result)
	}
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Skipf("git command failed (may not be installed): %v", err)
		}
	}
	runGit("init", dir)
	runGit("-C", dir, "config", "user.email", "test@test.com")
	runGit("-C", dir, "config", "user.name", "Test")
}

func TestSettle_GitCleanWorktree(t *testing.T) {
	gitDir := t.TempDir()
	initGitRepo(t, gitDir)

	// Use a SEPARATE directory for the chain DB to avoid polluting the git worktree.
	dbDir := t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(dbDir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	defer db.Close()

	ev := NewEvaluator(gitDir)
	s, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: s,
	})

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-git",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: gitDir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "clean-worktree",
				Type: envelope.AssertionGitCleanWorktree,
			},
		},
		TimeoutSeconds: 300,
	}

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictSettledClean {
		t.Errorf("Verdict = %v, want SETTLED_CLEAN", out.Receipt.Verdict)
	}
	db.Close()
}

func TestSettle_GitDirtyWorktree(t *testing.T) {
	gitDir := t.TempDir()
	initGitRepo(t, gitDir)
	os.WriteFile(filepath.Join(gitDir, "dirty.txt"), []byte("dirty"), 0644)

	dbDir := t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(dbDir, "chain.db"))
	cs := receipt.NewChainStore(db)
	cs.InitSchema(context.Background())
	defer db.Close()

	ev := NewEvaluator(gitDir)
	s, _ := signing.GenerateSigner("agent-b")
	eng, _ := NewEngine(EngineConfig{
		Evaluator:      ev,
		ChainStore:     cs,
		ExecutorSigner: s,
	})

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		EnvelopeID:      "contract-git-dirty",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "main",
			WorkspacePath: gitDir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "clean-worktree",
				Type: envelope.AssertionGitCleanWorktree,
			},
		},
		TimeoutSeconds: 300,
	}

	out, err := eng.Settle(context.Background(), SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	})
	if err != nil {
		t.Fatalf("Settle error: %v", err)
	}

	if out.Receipt.Verdict != receipt.VerdictFailed {
		t.Errorf("Verdict = %v, want VERDICT_FAILED", out.Receipt.Verdict)
	}
	db.Close()
}

func TestComputeVerdict_FileAssertion(t *testing.T) {
	eng, _, cleanup := setupEngine(t)
	defer cleanup()

	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultPass},
		{Result: ResultFail},
	}
	verdict, failed := eng.computeVerdict(results)
	if verdict != receipt.VerdictFailed {
		t.Errorf("verdict = %v, want VERDICT_FAILED", verdict)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

func TestSettle_ConcurrentWrites_SamePair(t *testing.T) {
	eng, cs, cleanup := setupEngine(t)
	defer cleanup()

	const numGoroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)
	receiptsCh := make(chan *receipt.SettlementReceipt, numGoroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			env := validEnvelope()
			env.EnvelopeID = fmt.Sprintf("contract-concurrent-%d", idx)

			in := SettlementInput{
				Envelope:        env,
				EmitterAgentID:  "agent-a",
				ExecutorAgentID: "agent-b",
			}

			out, err := eng.Settle(context.Background(), in)
			if err != nil {
				errCh <- fmt.Errorf("worker %d: %w", idx, err)
				return
			}
			receiptsCh <- out.Receipt
		}(i)
	}

	close(startBarrier)
	wg.Wait()
	close(errCh)
	close(receiptsCh)

	for err := range errCh {
		t.Errorf("concurrent Settle error: %v", err)
	}

	chain, err := cs.GetChain(context.Background(), "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("GetChain failed: %v", err)
	}

	if len(chain) != numGoroutines {
		t.Fatalf("chain length = %d, want %d", len(chain), numGoroutines)
	}

	results, err := receipt.VerifyChainIntegrity(chain, eng.executorSigner.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity error: %v", err)
	}
	for i, res := range results {
		if !res.Valid {
			t.Fatalf("receipt[%d] invalid: %s", i, res.Error)
		}
	}
}
