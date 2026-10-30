package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
)

// TestPreconditions_ToolAvailable_CommandInjectionRejected verifies that
// shell metacharacters in the tool parameter cannot cause command injection (Issue #35 / S6).
func TestPreconditions_ToolAvailable_CommandInjectionRejected(t *testing.T) {
	injectedMarker := filepath.Join(t.TempDir(), "server_pwned.marker")
	maliciousTool := fmt.Sprintf("nonexistent-tool-xyz; touch %s", injectedMarker)

	srv := &Server{
		config: Config{
			WorkspaceDir: t.TempDir(),
		},
	}

	results := srv.runPreconditions([]envelope.Precondition{
		{
			Type:   envelope.PreconditionToolAvailable,
			Params: map[string]string{"tool": maliciousTool},
		},
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Passed {
		t.Errorf("vulnerability #35: malicious tool passed precondition")
	}
	if _, err := os.Stat(injectedMarker); err == nil {
		t.Errorf("CRITICAL SECURITY VULNERABILITY #35: command injection in server.runPreconditions succeeded, marker file %s was created", injectedMarker)
		_ = os.Remove(injectedMarker)
	}
}

// TestPreconditions_CommandExitCode_CommandInjectionRejected verifies that
// command chaining in command_exit_code cannot execute secondary commands without shell.
func TestPreconditions_CommandExitCode_CommandInjectionRejected(t *testing.T) {
	injectedMarker := filepath.Join(t.TempDir(), "cmd_pwned.marker")
	maliciousCmd := fmt.Sprintf("echo safe; touch %s", injectedMarker)

	srv := &Server{
		config: Config{
			WorkspaceDir: t.TempDir(),
		},
	}

	_ = srv.runPreconditions([]envelope.Precondition{
		{
			Type:   envelope.PreconditionCommandExitCode,
			Params: map[string]string{"command": maliciousCmd},
		},
	})

	if _, err := os.Stat(injectedMarker); err == nil {
		t.Errorf("CRITICAL SECURITY VULNERABILITY #35: command chaining succeeded, marker file %s was created", injectedMarker)
		_ = os.Remove(injectedMarker)
	}
}

// TestPreconditions_CommandExitCode_ArbitraryBinaryExecution_RED tests Issue #130 on Server:
// Executing arbitrary binaries like touch/rm without an allowlist executes directly on host.
func TestPreconditions_CommandExitCode_ArbitraryBinaryExecution_RED(t *testing.T) {
	injectedMarker := filepath.Join(t.TempDir(), "server_unauthorized_exec.marker")
	destructiveCmd := fmt.Sprintf("touch %s", injectedMarker)

	srv := &Server{
		config: Config{
			WorkspaceDir: t.TempDir(),
		},
	}

	results := srv.runPreconditions([]envelope.Precondition{
		{
			Type:   envelope.PreconditionCommandExitCode,
			Params: map[string]string{"command": destructiveCmd},
		},
	})

	if _, err := os.Stat(injectedMarker); err == nil {
		_ = os.Remove(injectedMarker)
		t.Errorf("SECURITY VULNERABILITY #130: arbitrary binary executed on server host, created %s", injectedMarker)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Passed {
		t.Errorf("SECURITY VULNERABILITY #130: arbitrary binary execution reported Passed=true")
	}
	if !strings.Contains(results[0].Message, "rejected") && !strings.Contains(results[0].Message, "not permitted") {
		t.Errorf("expected explicit rejection message, got %q", results[0].Message)
	}
}

