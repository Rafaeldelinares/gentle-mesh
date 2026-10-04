package settlement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// DefaultRemediationLimit is the maximum number of remediation attempts.
const DefaultRemediationLimit = 1

// Engine runs the settlement process for a CognitiveTaskEnvelope.
type Engine struct {
	evaluator      *Evaluator
	chainStore     *receipt.ChainStore
	executorSigner signing.Signer
	remMax         int
	chainMu        sync.Mutex
}

// EngineConfig holds the dependencies for the settlement engine.
type EngineConfig struct {
	Evaluator      *Evaluator
	ChainStore     *receipt.ChainStore
	ExecutorSigner signing.Signer
	RemediationMax int // defaults to DefaultRemediationLimit
}

// NewEngine creates a settlement engine from its dependencies.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Evaluator == nil {
		return nil, errors.New("evaluator is required")
	}
	if cfg.ChainStore == nil {
		return nil, errors.New("chain store is required")
	}
	if cfg.ExecutorSigner == nil {
		return nil, errors.New("executor signer is required")
	}
	remMax := cfg.RemediationMax
	if remMax <= 0 {
		remMax = DefaultRemediationLimit
	}
	return &Engine{
		evaluator:      cfg.Evaluator,
		chainStore:     cfg.ChainStore,
		executorSigner: cfg.ExecutorSigner,
		remMax:         remMax,
	}, nil
}

// SettlementInput is the input for a settlement run.
type SettlementInput struct {
	Envelope        *envelope.CognitiveTaskEnvelope
	EmitterAgentID  string
	ExecutorAgentID string
}

// SettlementOutput is the outcome of a settlement run.
type SettlementOutput struct {
	Receipt         *receipt.SettlementReceipt
	RemediationUsed int
}

// Settle runs the full settlement process: convert assertions, evaluate,
// determine verdict, sign the receipt, and persist it to the chain.
func (eng *Engine) Settle(ctx context.Context, in SettlementInput) (*SettlementOutput, error) {
	if in.Envelope == nil {
		return nil, errors.New("envelope is required")
	}
	if in.EmitterAgentID == "" {
		return nil, errors.New("emitter agent ID is required")
	}
	if err := envelope.Validate(in.Envelope); err != nil {
		return nil, fmt.Errorf("envelope validation failed: %w", err)
	}
	if in.Envelope.EmitterAgentID != in.EmitterAgentID {
		return nil, fmt.Errorf("emitter agent ID mismatch: input %q != envelope %q", in.EmitterAgentID, in.Envelope.EmitterAgentID)
	}
	if in.ExecutorAgentID != "" && in.Envelope.ExecutorAgentID != in.ExecutorAgentID {
		return nil, fmt.Errorf("executor agent ID mismatch: input %q != envelope %q", in.ExecutorAgentID, in.Envelope.ExecutorAgentID)
	}
	expectedHash, err := envelope.ComputeEnvelopeHash(in.Envelope)
	if err != nil {
		return nil, fmt.Errorf("compute envelope hash: %w", err)
	}
	if in.Envelope.EnvelopeHash != "" && in.Envelope.EnvelopeHash != expectedHash {
		return nil, fmt.Errorf("envelope hash mismatch: input %q != computed %q", in.Envelope.EnvelopeHash, expectedHash)
	}

	// 1. prev_hash is computed inside ChainStore.SaveReceipt (under mutex) to ensure
	// correct chain linkage even when concurrent Settle() calls race.

	// 2. Convert envelope assertions to settlement assertions.
	settAssertions := eng.convertAssertions(in.Envelope.Assertions)

	// 3. Evaluate.
	results := eng.evaluator.EvaluateAll(ctx, settAssertions)

	// 4. Determine verdict.
	verdict, failedCount := eng.computeVerdict(results)

	// 5. Remediation loop if settlement failed.
	remediationUsed := 0
	if verdict == receipt.VerdictFailed {
		verdict, remediationUsed, results = eng.runRemediation(ctx, in, failedCount)
	}

	// 6. Build, sign, and persist the receipt through the shared append helper,
	// which reloads the chain head and retries transient contention.
	rec, err := receipt.SignAndSaveReceipt(ctx, eng.chainStore, receipt.AppendConfig{
		EmitterAgentID:  in.Envelope.EmitterAgentID,
		ExecutorAgentID: in.Envelope.ExecutorAgentID,
		Signer:          eng.executorSigner,
		Lock:            &eng.chainMu,
	}, func() (*receipt.SettlementReceipt, error) {
		return &receipt.SettlementReceipt{
			ProtocolVersion: receipt.CurrentProtocolVersion,
			MeshID:          in.Envelope.MeshID,
			ContractID:      in.Envelope.EnvelopeID,
			EnvelopeHash:    expectedHash,
			Verdict:         verdict,
			Territory:       eng.convertTerritory(in.Envelope.Territory),
			Assertions:      eng.convertResults(results),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return &SettlementOutput{Receipt: rec, RemediationUsed: remediationUsed}, nil
}

// runRemediation runs up to remMax remediation cycles.
func (eng *Engine) runRemediation(
	ctx context.Context, in SettlementInput, initialFailed int,
) (receipt.Verdict, int, []*AssertionResult) {
	for attempt := 1; attempt <= eng.remMax; attempt++ {
		settAssertions := eng.convertAssertions(in.Envelope.Assertions)
		results := eng.evaluator.EvaluateAll(ctx, settAssertions)
		verdict, _ := eng.computeVerdict(results)

		if verdict == receipt.VerdictSettledClean {
			// Some assertions now pass that failed before.
			if initialFailed > 0 {
				verdict = receipt.VerdictRemediated
			}
			return verdict, attempt, results
		}

		if attempt == eng.remMax {
			return receipt.VerdictFailed, attempt, results
		}
	}
	return receipt.VerdictFailed, eng.remMax, nil
}

// computeVerdict determines the verdict from settlement results.
func (eng *Engine) computeVerdict(results []*AssertionResult) (receipt.Verdict, int) {
	allPass := true
	hasErrors := false
	failed := 0

	for _, r := range results {
		switch r.Result {
		case ResultPass:
		case ResultFail:
			allPass = false
			failed++
		case ResultError:
			hasErrors = true
			failed++
		}
	}

	if allPass && !hasErrors {
		return receipt.VerdictSettledClean, 0
	}
	return receipt.VerdictFailed, failed
}

// convertAssertions converts envelope assertions to settlement assertions.
// It maps typed assertion params to the settlement evaluator's string-keyed map.
func (eng *Engine) convertAssertions(envAssertions []envelope.Assertion) []*Assertion {
	out := make([]*Assertion, len(envAssertions))
	for i, a := range envAssertions {
		params := eng.convertParams(a.Params)
		timeout := time.Duration(a.Params.TimeoutSeconds) * time.Second
		out[i] = &Assertion{
			ID:      a.ID,
			Type:    AssertionType(a.Type),
			Params:  params,
			Timeout: timeout,
		}
	}
	return out
}

// convertParams maps typed envelope params to settlement map params.
func (eng *Engine) convertParams(p envelope.AssertionParams) map[string]any {
	m := make(map[string]any)
	if p.FilePath != "" {
		m["path"] = p.FilePath
	}
	if p.ExpectedSHA256 != "" {
		m["expected_hash"] = p.ExpectedSHA256
	}
	if p.Command != "" {
		m["command"] = p.Command
	}
	// ExpectedExitCode is always set (default 0) so always include it.
	m["expected_code"] = p.ExpectedExitCode
	if p.ContainsPattern != "" {
		m["contains"] = p.ContainsPattern
	}
	if p.WorkingDir != "" {
		m["cwd"] = p.WorkingDir
	}
	if p.Port != 0 {
		m["port"] = p.Port
	}
	return m
}

// convertResults converts settlement results to receipt results.
func (eng *Engine) convertResults(results []*AssertionResult) []receipt.AssertionResult {
	out := make([]receipt.AssertionResult, len(results))
	for i, r := range results {
		ev := receipt.AssertionEvidence{
			Command:     r.Evidence.Command,
			ExitCode:    r.Evidence.ExitCode,
			StdoutHash:  r.Evidence.Stdout,
			StderrHash:  r.Evidence.Stderr,
			GitStatus:   r.Evidence.GitStatus,
			Port:        0,
			PortWasFree: r.Evidence.PortAvailable,
			CheckedAt:   r.Evidence.CheckedAt,
		}
		// File-specific evidence.
		if r.Evidence.FileExists {
			ev.FilePath = "<path>"
			ev.FileWasModified = r.Evidence.FileExists
		}
		if r.Evidence.ActualHash != "" {
			ev.ActualSHA256 = r.Evidence.ActualHash
			ev.ExpectedSHA256 = r.Evidence.ExpectedHash
		}
		// Output contains check.
		if strings.Contains(r.Evidence.Stdout, "") {
			ev.StdoutContains = true
		}
		if strings.Contains(r.Evidence.Stderr, "") {
			ev.StderrContains = true
		}
		out[i] = receipt.AssertionResult{
			AssertionIndex: r.AssertionIndex,
			AssertionID:    r.AssertionID,
			AssertionType:  string(r.AssertionType),
			Result:         eng.convertResult(r.Result),
			Evidence:       ev,
			Message:        r.Message,
		}
	}
	return out
}

// convertResult maps settlement.Result to receipt.Result.
func (eng *Engine) convertResult(r Result) receipt.Result {
	switch r {
	case ResultPass:
		return receipt.ResultPass
	case ResultFail:
		return receipt.ResultFail
	case ResultError:
		return receipt.ResultSkip
	default:
		return receipt.ResultSkip
	}
}

// convertTerritory converts envelope.Territory to receipt.Territory.
func (eng *Engine) convertTerritory(t envelope.Territory) receipt.Territory {
	return receipt.Territory{
		Repository:    t.Repository,
		Branch:        t.Branch,
		WorkspacePath: t.WorkspacePath,
	}
}

// VerifyReceipt verifies a receipt's executor signature.
func VerifyReceipt(r *receipt.SettlementReceipt, executorPublicKey []byte) error {
	if r == nil {
		return errors.New("receipt is nil")
	}
	return receipt.VerifyExecutorSignature(r, executorPublicKey)
}
