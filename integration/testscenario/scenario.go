// Package testscenario provides the RFC-002 integration test scenario.
// It orchestrates the full protocol flow between Agent A (emitter) and
// Agent B (executor), verifying receipts, chain integrity, and signatures.
package testscenario

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Scenario orchestrates the RFC-002 integration test.
type Scenario struct {
	aClient  *agent.HTTPClient // Agent A (emitter)
	bClient  *agent.HTTPClient // Agent B (executor)
	aSigner  *signing.BasicSigner
	bSigner  *signing.BasicSigner
	workspace string
}

// NewScenario creates a new test scenario with HTTP clients.
func NewScenario(aURL, bURL, workspace string) *Scenario {
	return &Scenario{
		aClient:  agent.NewHTTPClient(aURL),
		bClient:  agent.NewHTTPClient(bURL),
		workspace: workspace,
	}
}

// Run executes the full integration test scenario over HTTP.
func (s *Scenario) Run(ctx context.Context) error {
	log.Println("=== RFC-002 Integration Test Scenario (HTTP) ===")
	log.Println()

	// Step 0: Health check.
	log.Println("[0/7] Health check...")
	if err := s.checkHealth(ctx); err != nil {
		return fmt.Errorf("health: %w", err)
	}
	log.Println("    ✓ Both agents healthy")

	// Step 1: Initialize workspace.
	log.Println("[1/7] Initialize workspace...")
	if err := initWorkspace(s.workspace); err != nil {
		return fmt.Errorf("init workspace: %w", err)
	}
	log.Println("    ✓ Workspace initialized")

	// Step 2: Agent A creates and signs the envelope.
	log.Println("[2/7] Create and sign CognitiveTaskEnvelope...")
	env, err := s.createEnvelope()
	if err != nil {
		return fmt.Errorf("create envelope: %w", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	log.Printf("    ✓ Envelope: id=%s hash=%s", env.EnvelopeID, env.EnvelopeHash)
	log.Printf("    ✓ Assertions: %d", len(env.Assertions))

	// Step 3: Submit to Agent B (pre-flight handshake).
	log.Println("[3/7] Submit envelope (pre-flight handshake)...")
	leaseResp, err := s.aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	if !leaseResp.Accepted {
		return fmt.Errorf("lease rejected: %s", leaseResp.Error)
	}
	log.Printf("    ✓ Lease: id=%s", leaseResp.LeaseID)

	// Step 4: Simulate task execution.
	log.Println("[4/7] Simulate execution...")
	if err := s.simulateExecution(ctx); err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	log.Println("    ✓ calculator.py modified")

	// Step 5: Settlement.
	log.Println("[5/7] Settlement...")
	settleResp, err := s.bClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      leaseResp.LeaseID,
	})
	if err != nil {
		return fmt.Errorf("settle: %w", err)
	}
	rec := new(receipt.SettlementReceipt)
	if err := json.Unmarshal(settleResp.ReceiptJSON, rec); err != nil {
		return fmt.Errorf("parse receipt: %w", err)
	}
	log.Printf("    ✓ Receipt: id=%s verdict=%s", rec.ReceiptID, rec.Verdict)

	// Verify chain.
	chain, err := s.bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		return fmt.Errorf("get chain: %w", err)
	}
	if len(chain.Receipts) < 1 {
		return fmt.Errorf("chain empty")
	}
	log.Printf("    ✓ Chain length: %d", len(chain.Receipts))

	// Verify prev_hash = SHA-256(executor_signature).
	if rec.PreviousReceiptHash != "" {
		h := sha256.Sum256([]byte(rec.ExecutorSignature))
		expected := hex.EncodeToString(h[:])
		if rec.PreviousReceiptHash != expected {
			return fmt.Errorf("prev_hash mismatch: %s vs %s", rec.PreviousReceiptHash, expected)
		}
	}

	log.Println()
	log.Println("=== PASSED ===")
	return nil
}

// initWorkspace creates the git repo and initial files.
func initWorkspace(workspace string) error {
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return err
	}

	// Create initial calculator.py.
	calcPath := filepath.Join(workspace, "calculator.py")
	if _, err := os.Stat(calcPath); os.IsNotExist(err) {
		if err := os.WriteFile(calcPath, []byte(calculatorSource), 0644); err != nil {
			return err
		}
	}

	// Create test file.
	testPath := filepath.Join(workspace, "test_calculator.py")
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		if err := os.WriteFile(testPath, []byte(testSource), 0644); err != nil {
			return err
		}
	}

	// Init git repo.
	cmd := exec.Command("git", "init")
	cmd.Dir = workspace
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = workspace
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = workspace
	_ = cmd.Run()

	return nil
}

func (s *Scenario) checkHealth(ctx context.Context) error {
	if s.aClient != nil {
		if _, err := s.aClient.Health(ctx); err != nil {
			return fmt.Errorf("agent-a: %w", err)
		}
	}
	if s.bClient != nil {
		if _, err := s.bClient.Health(ctx); err != nil {
			return fmt.Errorf("agent-b: %w", err)
		}
	}
	return nil
}

func (s *Scenario) createEnvelope() (*envelope.CognitiveTaskEnvelope, error) {
	calcPath := filepath.Join(s.workspace, "calculator.py")
	baselineHash, err := fileSHA256(calcPath)
	if err != nil {
		return nil, fmt.Errorf("baseline hash: %w", err)
	}

	// Generate signers if not already done (for HTTP mode, we use the keys from the server).
	// In HTTP mode, A's signer is implicit (the server has it).
	// For this test, we generate local signers just for signing.
	aSigner, err := signing.GenerateSigner("agent-a")
	if err != nil {
		return nil, err
	}

	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: s.workspace,
		},
		Preconditions: []envelope.Precondition{
			{Type: envelope.PreconditionGitCleanWorktree, Params: map[string]string{}},
			{Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "pytest"}},
		},
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
					Command:          "pytest -v",
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
		return nil, fmt.Errorf("compute hash: %w", err)
	}
	env.EnvelopeHash = hash

	// Sign.
	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		return nil, fmt.Errorf("jcs marshal: %w", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	env.EmitterSignature = sig

	// Store signer for later verification.
	s.aSigner = aSigner

	return env, nil
}

func (s *Scenario) simulateExecution(ctx context.Context) error {
	// For HTTP mode, use the execute endpoint.
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
	cmd := fmt.Sprintf("cat >> %s << 'PYEOF'\n%sPYEOF", filepath.Join(s.workspace, "calculator.py"), newFunc)
	_, err := s.bClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    cmd,
		WorkingDir: s.workspace,
	})
	return err
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

func newUUIDv7() string {
	now := time.Now().UTC()
	unixMs := now.UnixMilli()
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uint32(unixMs>>32),
		uint16(unixMs>>16)&0xFFFF,
		uint16(unixMs)&0xFFFF,
		uint16(0x8000|((now.UnixNano()>>48)&0x3FFF)),
		uint64(0x4000000000000000|((now.UnixNano()/1000000)&0x3FFFFFFFFFFFFF)),
	)
}

// calculatorSource is the initial calculator.py content.
const calculatorSource = `#!/usr/bin/env python3
"""Simple calculator module for RFC-002 integration tests."""


def add(a, b):
    """Add two numbers."""
    return a + b


def subtract(a, b):
    """Subtract b from a."""
    return a - b
`

// testSource is the pytest test file.
const testSource = `#!/usr/bin/env python3
"""Tests for calculator module."""
import pytest
import sys
sys.path.insert(0, '.')
import calculator


class TestCalculator:
    def test_add(self):
        assert calculator.add(2, 3) == 5

    def test_subtract(self):
        assert calculator.subtract(5, 3) == 2
`
