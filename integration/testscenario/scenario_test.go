package testscenario

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
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

// TestScenario_FullFlow is the main integration test for RFC-002.
// It tests the complete protocol flow in-process without Docker.
func TestScenario_FullFlow(t *testing.T) {
	// ─── Setup: workspace with git repo ───
	dir := t.TempDir()
	if err := initGitRepo(dir); err != nil {
		t.Fatalf("initGitRepo: %v", err)
	}

	// Verify worktree is clean after init.
	if !isGitClean(dir) {
		cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
		out, _ := cmd.Output()
		t.Fatalf("worktree should be clean after init, got: %s", string(out))
	}

	// ─── Pre-flight handshake (B checks preconditions on clean repo) ───
	preconds := []envelope.Precondition{
		{Type: envelope.PreconditionGitCleanWorktree, Params: map[string]string{}},
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "python3"}},
	}
	precondResults := runPreconditions(dir, preconds)
	for _, r := range precondResults {
		if !r.Passed {
			t.Fatalf("precondition %s failed: %s", r.Type, r.Message)
		}
	}

	// ─── Create signers ───
	aSigner, err := signing.GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("generate signer A: %v", err)
	}
	bSigner, err := signing.GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("generate signer B: %v", err)
	}

	// ─── Initialize chain store (AFTER precondition check to keep worktree clean) ───
	dbDir := t.TempDir() // Separate dir for chain DB (avoids polluting git worktree)
	dbPath := filepath.Join(dbDir, "chain.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	chainStore := receipt.NewChainStore(db)
	if err := chainStore.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	evaluator := settlement.NewEvaluator(dir)
	engine, err := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:       evaluator,
		ChainStore:     chainStore,
		ExecutorSigner: bSigner,
		RemediationMax: 1,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// ─── Create and sign CognitiveTaskEnvelope ───
	calcPath := filepath.Join(dir, "calculator.py")
	baselineHash, err := fileSHA256(calcPath)
	if err != nil {
		t.Fatalf("baseline hash: %v", err)
	}

	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: dir,
		},
		Preconditions: preconds,
		Assertions: []envelope.Assertion{
			{
				ID:   "file_modified",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       "calculator.py",
					ExpectedSHA256: baselineHash,
				},
			},
			{
				ID:   "tests_passing",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "pytest -v test_calculator.py",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds:  120,
		MaxRemediations: 1,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}

	hash, err := envelope.ComputeEnvelopeHash(env)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash: %v", err)
	}
	env.EnvelopeHash = hash

	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		t.Fatalf("jcs.Marshal: %v", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		t.Fatalf("aSigner.Sign: %v", err)
	}
	env.EmitterSignature = sig

	// ─── Simulate task execution (B modifies calculator.py) ───
	newFunc := `

def multiply(a, b):
    """Multiply two numbers."""
    return a * b


def divide(a, b):
    """Divide two numbers. Raises ValueError if b is zero."""
    if b == 0:
        raise ValueError("cannot divide by zero")
    return a / b
`
	f, err := os.OpenFile(calcPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open calc for append: %v", err)
	}
	if _, err := f.WriteString(newFunc); err != nil {
		f.Close()
		t.Fatalf("write new func: %v", err)
	}
	f.Close()

	// Update test file.
	testPath := filepath.Join(dir, "test_calculator.py")
	newTests := `

class TestNewFunctions:
    def test_multiply(self):
        assert calculator.multiply(3, 4) == 12

    def test_divide(self):
        assert calculator.divide(10, 2) == 5.0

    def test_divide_by_zero(self):
        with pytest.raises(ValueError):
            calculator.divide(1, 0)
`
	f2, err := os.OpenFile(testPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open test for append: %v", err)
	}
	if _, err := f2.WriteString(newTests); err != nil {
		f2.Close()
		t.Fatalf("write new tests: %v", err)
	}
	f2.Close()

	// ─── Settlement ───
	ctx := context.Background()
	input := settlement.SettlementInput{
		Envelope:        env,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	}

	out, err := engine.Settle(ctx, input)
	if err != nil {
		t.Fatalf("engine.Settle: %v", err)
	}

	rec := out.Receipt

	// ─── Assertions ───
	var fileResult, testResult *receipt.AssertionResult
	for i := range rec.Assertions {
		switch rec.Assertions[i].AssertionID {
		case "file_modified":
			fileResult = &rec.Assertions[i]
		case "tests_passing":
			testResult = &rec.Assertions[i]
		}
	}
	if fileResult == nil || testResult == nil {
		t.Fatal("missing assertion results")
	}

	// file_modified: should PASS (hash changed from baseline).
	if fileResult.Result != receipt.ResultPass {
		t.Errorf("file_modified = %v, want PASS", fileResult.Result)
	}
	if !fileResult.Evidence.FileWasModified {
		t.Error("file_was_modified should be true")
	}

	// tests_passing: should PASS.
	if testResult.Result != receipt.ResultPass {
		t.Errorf("tests_passing = %v (exit=%d), want PASS",
			testResult.Result, testResult.Evidence.ExitCode)
	}

	// Verdict should be SETTLED_CLEAN.
	if rec.Verdict != receipt.VerdictSettledClean {
		t.Errorf("Verdict = %v, want SETTLED_CLEAN", rec.Verdict)
	}

	// ─── Executor signature verification ───
	receiptHash, err := receipt.ComputeReceiptHash(rec)
	if err != nil {
		t.Fatalf("ComputeReceiptHash: %v", err)
	}
	err = signing.Verify(bSigner.PublicKey(), []byte(receiptHash), rec.ExecutorSignature)
	if err != nil {
		t.Errorf("executor signature invalid: %v", err)
	}

	// ─── Chain verification ───
	if rec.PreviousReceiptHash != "" {
		t.Errorf("first receipt PreviousReceiptHash = %q, want empty", rec.PreviousReceiptHash)
	}

	// Receipt persisted.
	saved, err := chainStore.GetReceipt(ctx, rec.ReceiptID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if saved.ReceiptID != rec.ReceiptID {
		t.Errorf("saved receipt ID = %q, want %q", saved.ReceiptID, rec.ReceiptID)
	}

	// ─── Second receipt: chain link ───
	env2 := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: dir,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "file_exists",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       "calculator.py",
					ExpectedSHA256: baselineHash,
				},
			},
		},
		TimeoutSeconds:  120,
		MaxRemediations: 0,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}
	hash2, _ := envelope.ComputeEnvelopeHash(env2)
	env2.EnvelopeHash = hash2

	signable2 := *env2
	signable2.EmitterSignature = ""
	data2, _ := jcs.Marshal(&signable2)
	sig2, _ := aSigner.Sign(data2)
	env2.EmitterSignature = sig2

	input2 := settlement.SettlementInput{
		Envelope:        env2,
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
	}
	out2, err := engine.Settle(ctx, input2)
	if err != nil {
		t.Fatalf("engine.Settle (2nd): %v", err)
	}
	rec2 := out2.Receipt

	// Chain link: SHA-256(executor_signature of rec1).
	expectedPrevHash := sha256.Sum256([]byte(rec.ExecutorSignature))
	expectedPrevHashHex := hex.EncodeToString(expectedPrevHash[:])
	if rec2.PreviousReceiptHash != expectedPrevHashHex {
		t.Errorf("rec2.PreviousReceiptHash = %q, want SHA-256(rec1.ExecutorSignature) = %q",
			rec2.PreviousReceiptHash, expectedPrevHashHex)
	}

	// Verify rec2 signature.
	receiptHash2, _ := receipt.ComputeReceiptHash(rec2)
	err = signing.Verify(bSigner.PublicKey(), []byte(receiptHash2), rec2.ExecutorSignature)
	if err != nil {
		t.Errorf("rec2 executor signature invalid: %v", err)
	}

	// Chain count = 2.
	cnt, err := chainStore.Count(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if cnt != 2 {
		t.Errorf("chain count = %d, want 2", cnt)
	}

	// VerifyChain.
	results, err := chainStore.VerifyChain(ctx, "agent-a", "agent-b",
		bSigner.PublicKey(), aSigner.PublicKey())
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("VerifyChain returned %d results, want 2", len(results))
	}
	for i, r := range results {
		if !r.Valid {
			t.Errorf("chain[%d] (%s) invalid: %s", i, r.ReceiptID, r.Error)
		}
		if !r.PreviousHashValid {
			t.Errorf("chain[%d] (%s) prev hash invalid: %s", i, r.ReceiptID, r.Error)
		}
	}

	t.Log("=== ALL INTEGRATION CHECKS PASSED ===")
	t.Logf("Receipt 1: %s | %s | %d assertions", rec.ReceiptID, rec.Verdict, len(rec.Assertions))
	t.Logf("Receipt 2: %s | %s | %d assertions", rec2.ReceiptID, rec2.Verdict, len(rec2.Assertions))
}

// ─────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────

func initGitRepo(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	calcPath := filepath.Join(dir, "calculator.py")
	if _, err := os.Stat(calcPath); os.IsNotExist(err) {
		if err := os.WriteFile(calcPath, []byte(calculatorSource), 0644); err != nil {
			return err
		}
	}
	testPath := filepath.Join(dir, "test_calculator.py")
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		if err := os.WriteFile(testPath, []byte(testSource), 0644); err != nil {
			return err
		}
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	cmd = exec.Command("git", "-C", dir, "config", "user.email", "test@test.com")
	_ = cmd.Run()
	cmd = exec.Command("git", "-C", dir, "config", "user.name", "Test")
	_ = cmd.Run()

	// Commit to make worktree clean.
	cmd = exec.Command("git", "-C", dir, "add", ".")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-m", "initial")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

func runPreconditions(workspace string, preconds []envelope.Precondition) []envelope.PreconditionResult {
	results := make([]envelope.PreconditionResult, len(preconds))
	for i, p := range preconds {
		result := envelope.PreconditionResult{
			PreconditionIndex: i,
			Type:             string(p.Type),
			Passed:           false,
		}
		switch p.Type {
		case envelope.PreconditionGitCleanWorktree:
			result.Passed = isGitClean(workspace)
			result.Message = fmt.Sprintf("git clean=%v", result.Passed)
		case envelope.PreconditionToolAvailable:
			tool, _, ok := p.ToolParams()
			if ok {
				cmd := exec.Command("sh", "-c", "command -v "+tool)
				cmd.Dir = workspace
				result.Passed = cmd.Run() == nil
				result.Message = fmt.Sprintf("tool %s available=%v", tool, result.Passed)
			}
		default:
			result.Message = "unsupported"
		}
		results[i] = result
	}
	return results
}

func isGitClean(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	return err == nil && len(out) == 0
}


