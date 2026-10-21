package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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

// TestShell_EmptyMeshID_ReturnsError verifies that New returns an error if MeshID is empty.
func TestShell_EmptyMeshID_ReturnsError(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	_, err := New(Config{
		AgentID:      "test-agent",
		MeshID:       "", // Empty MeshID MUST return error
		WorkspaceDir: workspace,
		ChainDBPath:  chainDB,
	})
	if err == nil {
		t.Fatal("New(Config{MeshID: \"\"}) should have returned error, got nil")
	}
}

// TestShell_Signer verifies that the shell exposes its signer.
func TestShell_Signer(t *testing.T) {
	workspace := t.TempDir()
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
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

// TestShell_PreconditionToolAvailable_CommandInjectionRejected verifies that
// shell metacharacters in the tool parameter cannot cause command injection (Issue #35 / S6).
func TestShell_PreconditionToolAvailable_CommandInjectionRejected(t *testing.T) {
	workspace := t.TempDir()
	chainDB := filepath.Join(t.TempDir(), "chain.db")
	injectedMarker := filepath.Join(t.TempDir(), "pwned.marker")

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// In the vulnerable implementation ("sh -c command -v %s"), the shell executes:
	// command -v nonexistent-tool-xyz; touch /path/to/pwned.marker
	// which creates the file and exits 0 (falsely passing the precondition).
	maliciousTool := fmt.Sprintf("nonexistent-tool-xyz; touch %s", injectedMarker)
	report, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": maliciousTool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Errorf("vulnerability #35: malicious tool string passed pre-flight check")
	}
	if _, err := os.Stat(injectedMarker); err == nil {
		t.Errorf("CRITICAL SECURITY VULNERABILITY #35: command injection succeeded, marker file %s was created", injectedMarker)
		_ = os.Remove(injectedMarker)
	}
}

// TestShell_PreconditionCommandExitCode_CommandInjectionRejected verifies that
// command chaining in command_exit_code cannot execute secondary commands without shell.
func TestShell_PreconditionCommandExitCode_CommandInjectionRejected(t *testing.T) {
	workspace := t.TempDir()
	chainDB := filepath.Join(t.TempDir(), "chain.db")
	injectedMarker := filepath.Join(t.TempDir(), "shell_cmd_pwned.marker")

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	maliciousCmd := fmt.Sprintf("echo safe; touch %s", injectedMarker)
	_, err = s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": maliciousCmd}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(injectedMarker); err == nil {
		t.Errorf("CRITICAL SECURITY VULNERABILITY #35: command chaining succeeded, marker file %s was created", injectedMarker)
		_ = os.Remove(injectedMarker)
	}
}

// TestShell_PreconditionCommandExitCode_ArbitraryBinaryExecution_RED tests Issue #130:
// PreconditionCommandExitCode must NOT execute arbitrary binaries (such as touch, rm, curl)
// with host worker privileges. Only an explicit allowlist of read-only diagnostic tools is permitted.
func TestShell_PreconditionCommandExitCode_ArbitraryBinaryExecution_RED(t *testing.T) {
	workspace := t.TempDir()
	chainDB := filepath.Join(t.TempDir(), "chain.db")
	injectedMarker := filepath.Join(t.TempDir(), "unauthorized_exec.marker")

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A binary outside the read-only diagnostic allowlist
	destructiveCmd := fmt.Sprintf("touch %s", injectedMarker)
	report, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": destructiveCmd}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Vulnerable behavior: touch executes on host, creates the marker, and reports Passed: true.
	// We assert that the marker is NOT created and that the report is rejected with an explicit reason.
	if _, err := os.Stat(injectedMarker); err == nil {
		_ = os.Remove(injectedMarker)
		t.Errorf("SECURITY VULNERABILITY #130: arbitrary binary executed on host, created %s", injectedMarker)
	}
	if report.Passed {
		t.Errorf("SECURITY VULNERABILITY #130: arbitrary binary execution reported Passed=true")
	}
	if !strings.Contains(report.RejectionReason, "not permitted") && !strings.Contains(report.RejectionReason, "rejected") {
		t.Errorf("expected explicit rejection reason, got %q", report.RejectionReason)
	}
}

// TestShell_PreconditionCommandExitCode_AllowedAndDisallowedCommands tests both allowed
// and disallowed commands to verify deterministic permit/reject behavior.
func TestShell_PreconditionCommandExitCode_AllowedAndDisallowedCommands(t *testing.T) {
	workspace := t.TempDir()
	chainDB := filepath.Join(t.TempDir(), "chain.db")

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: workspace,
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 1. Permitted: true
	repTrue, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": "true"}},
	})
	if err != nil || !repTrue.Passed {
		t.Fatalf("expected 'true' to pass, got err=%v report=%+v", err, repTrue)
	}

	// 2. Permitted: test directory
	repTest, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": fmt.Sprintf("test -d %s", workspace)}},
	})
	if err != nil || !repTest.Passed {
		t.Fatalf("expected 'test -d' to pass, got err=%v report=%+v", err, repTest)
	}

	// 3. Disallowed: python3 -c code injection
	repPy, err := s.Check(context.Background(), []envelope.Precondition{
		{Type: envelope.PreconditionCommandExitCode, Params: map[string]string{"command": "python3 -c 'print(1)'"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repPy.Passed {
		t.Errorf("expected python3 -c to be rejected")
	}
	if !strings.Contains(repPy.RejectionReason, "not permitted") {
		t.Errorf("expected rejection reason for python3 -c, got %q", repPy.RejectionReason)
	}
}

// runCmd is a test helper that runs a command and ignores its output.
func runCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	_ = cmd.Run() // ignore errors in test setup
}
