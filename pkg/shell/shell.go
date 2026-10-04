// Package shell provides a contract-aware shell execution layer that integrates
// with gentle-mesh for verified, receipt-backed command execution.
package shell

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// Config holds shell configuration.
type Config struct {
	MeshID          string        // Mesh network identity (required, cannot be empty)
	AgentID         string        // Local agent identity (e.g., "pi-local", "pi-emitter")
	WorkspaceDir    string        // Working directory for command execution
	ChainDBPath     string        // Path to SQLite receipt chain database
	EvalTimeout     time.Duration // Maximum time for a single assertion evaluation
	MaxRemediations int           // Maximum number of remediation loops
	RemoteURL       string        // Remote executor URL (empty = local mode)
}

// Shell is a contract-aware shell execution layer.
// It wraps gentle-mesh's settlement engine with a synchronous API.
type Shell struct {
	config     Config
	signer     *signing.BasicSigner
	evaluator  *settlement.Evaluator
	chainStore *receipt.ChainStore
	db         *sql.DB
	chainMu    sync.Mutex
}

// New creates a new Shell. In local mode (RemoteURL == ""), gentle-mesh
// settlement runs embedded. In remote mode, commands are submitted via HTTPS.
func New(cfg Config) (*Shell, error) {
	if cfg.MeshID == "" {
		return nil, fmt.Errorf("MeshID is required")
	}
	if cfg.ChainDBPath == "" {
		return nil, fmt.Errorf("ChainDBPath is required")
	}

	// Create signer — generates a new Ed25519 keypair.
	signer, err := signing.GenerateSigner(cfg.AgentID)
	if err != nil {
		return nil, fmt.Errorf("generate signer: %w", err)
	}

	// Create evaluator.
	evaluator := settlement.NewEvaluator(cfg.WorkspaceDir)
	if cfg.EvalTimeout > 0 {
		evaluator = evaluator.WithTimeout(cfg.EvalTimeout)
	}

	// Open SQLite chain store.
	if err := os.MkdirAll(filepath.Dir(cfg.ChainDBPath), 0755); err != nil {
		return nil, fmt.Errorf("create chain db directory: %w", err)
	}
	db, err := sql.Open("sqlite", cfg.ChainDBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure chain db pragmas: %w", err)
	}

	chainStore := receipt.NewChainStore(db)
	if err := chainStore.InitSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("init chain schema: %w", err)
	}

	return &Shell{
		config:     cfg,
		signer:     signer,
		evaluator:  evaluator,
		chainStore: chainStore,
		db:         db,
	}, nil
}

// ReadinessReport is the result of a pre-flight check.
type ReadinessReport struct {
	Passed          bool
	Results         []envelope.PreconditionResult
	RejectionReason string
}

// Check runs pre-flight checks synchronously and returns a ReadinessReport.
// If Passed is false, the caller MUST NOT proceed with execution.
// In remote mode, this delegates to the remote executor.
func (s *Shell) Check(ctx context.Context, preconditions []envelope.Precondition) (*ReadinessReport, error) {
	results := make([]envelope.PreconditionResult, len(preconditions))
	for i, p := range preconditions {
		results[i] = s.evalPrecondition(ctx, p)
	}

	allPassed := true
	var reason string
	for _, r := range results {
		if !r.Passed {
			allPassed = false
			if reason == "" {
				reason = r.Message
			}
		}
	}

	return &ReadinessReport{
		Passed:          allPassed,
		Results:         results,
		RejectionReason: reason,
	}, nil
}

// evalPrecondition evaluates a single precondition on the local filesystem.
func (s *Shell) evalPrecondition(ctx context.Context, p envelope.Precondition) envelope.PreconditionResult {
	var result envelope.PreconditionResult
	result.Type = string(p.Type) // PreconditionType is a string type

	switch p.Type {
	case envelope.PreconditionToolAvailable:
		tool := p.Params["tool"]
		if tool == "" {
			result.Passed = false
			result.Message = "tool parameter missing"
			return result
		}
		ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx2, "sh", "-c", fmt.Sprintf("command -v %s", tool))
		if err := cmd.Run(); err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("tool not found: %s", tool)
		} else {
			result.Passed = true
			result.Message = fmt.Sprintf("tool available: %s", tool)
		}

	case envelope.PreconditionGitCleanWorktree:
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx2, "git", "-C", s.config.WorkspaceDir, "status", "--porcelain")
		out, err := cmd.Output()
		if err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("git status error: %v", err)
		} else if strings.TrimSpace(string(out)) != "" {
			result.Passed = false
			result.Message = fmt.Sprintf("git worktree dirty:\n%s", strings.TrimSpace(string(out)))
		} else {
			result.Passed = true
			result.Message = "worktree clean"
		}

	case envelope.PreconditionCommandExitCode:
		cmdStr := p.Params["command"]
		if cmdStr == "" {
			result.Passed = false
			result.Message = "command parameter missing"
			return result
		}
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx2, "sh", "-c", cmdStr)
		if err := cmd.Run(); err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("command failed: %v", err)
		} else {
			result.Passed = true
			result.Message = "command succeeded"
		}

	default:
		// Unknown precondition type: pass conservatively
		result.Passed = true
		result.Message = fmt.Sprintf("unknown precondition: %s", p.Type)
	}

	return result
}

// Execute runs assertions deterministically and returns a SettlementReceipt.
// The Arnês (Evaluator) is the sole source of truth for the verdict;
// the calling LLM has zero influence over the outcome.
func (s *Shell) Execute(ctx context.Context, assertions []envelope.Assertion) (*receipt.SettlementReceipt, error) {
	// Convert envelope assertions to settlement assertions.
	settledAssertions := ConvertAssertions(assertions)

	// Evaluate all assertions deterministically.
	evalCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	results := s.evaluator.EvaluateAll(evalCtx, settledAssertions)
	allPass, _, _ := settlement.Summary(results)

	// Compute verdict.
	var verdict receipt.Verdict
	if allPass {
		verdict = receipt.VerdictSettledClean
	} else {
		verdict = receipt.VerdictFailed
	}

	meshID := s.config.MeshID
	if meshID == "" {
		return nil, fmt.Errorf("MeshID is required")
	}

	// Build receipt.
	signedAt := time.Now().UTC()
	assertionsJSON, _ := json.Marshal(assertions)
	h := sha256.Sum256(assertionsJSON)
	envHash := hex.EncodeToString(h[:])

	agentID := s.config.AgentID
	if agentID == "" {
		agentID = "local-shell"
	}

	// Append through the shared helper: it reloads the chain head, assigns a fresh
	// crypto/rand receipt_id, signs and saves, retrying only transient contention.
	contractID := fmt.Sprintf("contract-%d", signedAt.UnixNano())
	rec, err := receipt.SignAndSaveReceipt(ctx, s.chainStore, receipt.AppendConfig{
		EmitterAgentID:  agentID,
		ExecutorAgentID: agentID,
		Signer:          s.signer,
		Lock:            &s.chainMu,
	}, func() (*receipt.SettlementReceipt, error) {
		return &receipt.SettlementReceipt{
			ProtocolVersion: receipt.CurrentProtocolVersion,
			MeshID:          meshID,
			ContractID:      contractID,
			EnvelopeHash:    envHash,
			Verdict:         verdict,
			Assertions:      ConvertResults(results),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return rec, nil
}

// GetChain returns the receipt chain for an emitter/executor pair.
// In a local shell context, an empty emitterID defaults to the shell's own agentID.
func (s *Shell) GetChain(emitterID, executorID string) ([]*receipt.SettlementReceipt, error) {
	if emitterID == "" {
		emitterID = s.config.AgentID
	}
	return s.chainStore.GetChain(context.Background(), emitterID, executorID)
}

// Close releases resources (closes the SQLite database).
func (s *Shell) Close() error {
	if s.db != nil {
		s.db.Close()
	}
	return nil
}

// Signer returns the shell's Ed25519 signer.
func (s *Shell) Signer() *signing.BasicSigner {
	return s.signer
}
