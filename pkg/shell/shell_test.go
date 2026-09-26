package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
)

// TestShell_LocalPreFlightRejection verifies that a dirty git worktree
// causes Check() to return Passed=false.
func TestShell_LocalPreFlightRejection(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	// Init git repo and create a dirty file.
	runCmd("git", "init", workspace)
	runCmd("git", "-C", workspace, "config", "user.email", "test@test.com")
	runCmd("git", "-C", workspace, "config", "user.name", "Test")
	if err := os.WriteFile(workspace+"/dirty.txt", []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	runCmd("git", "-C", workspace, "add", ".")

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	report, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionGitCleanWorktree, Params: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Error("dirty worktree: Check() should have returned Passed=false")
	}
	if report.RejectionReason == "" {
		t.Error("RejectionReason should be non-empty for dirty worktree")
	}
}

// TestShell_LocalPreFlightToolNotFound verifies that a missing tool
// causes the tool_available precondition to fail.
func TestShell_LocalPreFlightToolNotFound(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	report, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "nonexistent-binary-xyz"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Error("missing tool: Check() should have returned Passed=false")
	}
}

// TestShell_LocalPreFlightToolFound verifies that an available tool passes.
func TestShell_LocalPreFlightToolFound(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	report, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Errorf("go tool: Check() should have returned Passed=true, got: %s", report.RejectionReason)
	}
}

// TestShell_ExecuteAssertionPass verifies that Execute() with a passing
// command_exit_code assertion produces a SETTLED_CLEAN receipt.
func TestShell_ExecuteAssertionPass(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
		EvalTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec, err := s.Execute(context.Background(), []envelope.Assertion{
		{
			ID:   "true_passes",
			Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{
				Command:          "true", // exit 0
				ExpectedExitCode: 0,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Verdict != receipt.VerdictSettledClean {
		t.Errorf("verdict = %v, want SETTLED_CLEAN", rec.Verdict)
	}
	if len(rec.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(rec.Assertions))
	}
	if rec.Assertions[0].Result != receipt.ResultPass {
		t.Errorf("assertion result = %v, want PASS", rec.Assertions[0].Result)
	}
}

// TestShell_ExecuteAssertionFail verifies that Execute() with a failing
// command_exit_code assertion produces a SETTLED_FAILED receipt.
func TestShell_ExecuteAssertionFail(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
		EvalTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec, err := s.Execute(context.Background(), []envelope.Assertion{
		{
			ID:   "false_fails",
			Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{
				Command:          "exit 1",
				ExpectedExitCode: 0,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Verdict != receipt.VerdictFailed {
		t.Errorf("verdict = %v, want SETTLED_FAILED", rec.Verdict)
	}
	if len(rec.Assertions) != 1 {
		t.Fatalf("Assertions length = %d, want 1", len(rec.Assertions))
	}
	if rec.Assertions[0].Result != receipt.ResultFail {
		t.Errorf("assertion result = %v, want FAIL", rec.Assertions[0].Result)
	}
}

// TestShell_ExecuteFileHashMismatch verifies that a file_hash_equals
// assertion fails when the hash doesn't match.
func TestShell_ExecuteFileHashMismatch(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	// Write a file.
	fpath := filepath.Join(workspace, "output.txt")
	if err := os.WriteFile(fpath, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
		EvalTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Hash of "hello world" is e965ad69c14f6e4f1520f3030e7e73b8be9884b4
	// but we use a wrong hash so it fails.
	rec, err := s.Execute(context.Background(), []envelope.Assertion{
		{
			ID:   "wrong_hash",
			Type: envelope.AssertionFileHashEquals,
			Params: envelope.AssertionParams{
				FilePath:       "output.txt",
				ExpectedSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Verdict != receipt.VerdictFailed {
		t.Errorf("verdict = %v, want SETTLED_FAILED (hash mismatch)", rec.Verdict)
	}
}

// TestShell_ChainStore verifies that Execute() saves receipts to the chain.
func TestShell_ChainStore(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
		EvalTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Execute two receipts.
	_, err = s.Execute(context.Background(), []envelope.Assertion{
		{ID: "p1", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Execute(context.Background(), []envelope.Assertion{
		{ID: "p2", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}

	chain, err := s.GetChain("", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Errorf("chain length = %d, want 2", len(chain))
	}
}

// TestShell_Signer verifies that the shell exposes its signer.
func TestShell_Signer(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		AgentID:       "test-agent",
		WorkspaceDir:  workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	signer := s.Signer()
	if signer == nil {
		t.Fatal("Signer() returned nil")
	}
	if signer.AgentID() != "test-agent" {
		t.Errorf("AgentID = %q, want %q", signer.AgentID(), "test-agent")
	}
}

// runCmd is a test helper that runs a command and ignores its output.
func runCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	_ = cmd.Run() // ignore errors in test setup
}
