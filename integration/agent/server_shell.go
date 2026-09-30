//go:build testharness

package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// ShellServer wraps a Shell executor with HTTP handlers.
// It is used for Mode A (Local Sidecar) where gentle-mesh settlement runs
// embedded alongside the shell execution layer.
type ShellServer struct {
	shell      *shellWrapper
	chainStore *receipt.ChainStore
	db         *sql.DB
	signer     *signing.BasicSigner
	agentID    string
	meshID     string
	tlsOpts    []TLSClientOption
}

// shellWrapper holds shell configuration (mirrors pkg/shell.Config).
type shellWrapper struct {
	workspace   string
	evalTimeout time.Duration
}

// NewShellServer creates a shell-based HTTP server.
func NewShellServer(agentID, workspace, chainDBPath string, evalTimeout time.Duration, opts ...TLSClientOption) (*ShellServer, error) {
	if err := os.MkdirAll(filepath.Dir(chainDBPath), 0755); err != nil {
		return nil, fmt.Errorf("create chain db dir: %w", err)
	}
	db, err := sql.Open("sqlite", chainDBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL and busy timeout: %w", err)
	}

	signer, err := signing.GenerateSigner(agentID)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("generate signer: %w", err)
	}

	chainStore := receipt.NewChainStore(db)
	if err := chainStore.InitSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("init chain schema: %w", err)
	}

	return &ShellServer{
		shell: &shellWrapper{
			workspace:   workspace,
			evalTimeout: evalTimeout,
		},
		chainStore: chainStore,
		db:         db,
		signer:     signer,
		agentID:    agentID,
		tlsOpts:    opts,
	}, nil
}

// Close releases resources.
func (s *ShellServer) Close() error {
	if s.db != nil {
		s.db.Close()
	}
	return nil
}

// RegisterHTTPHandlers registers shell-based HTTP handlers on the given mux.
// These supplement (not replace) the existing agent server handlers.
func (s *ShellServer) RegisterHTTPHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /check", s.handleCheck)
	mux.HandleFunc("POST /execute-local", s.handleExecuteLocal)
	mux.HandleFunc("POST /dispatch", s.handleDispatch)
	mux.HandleFunc("GET /chain/", s.handleGetChainByPair)
}

// ─────────────────────────────────────────────────────────────────
// POST /check — pre-flight readiness check
// ─────────────────────────────────────────────────────────────────

// CheckRequest is the POST /check request body.
type CheckRequest struct {
	Preconditions []envelope.Precondition `json:"preconditions"`
}

// CheckResponse is the POST /check response body.
type CheckResponse struct {
	Passed          bool                          `json:"passed"`
	RejectionReason string                        `json:"rejection_reason,omitempty"`
	Results         []envelope.PreconditionResult `json:"results"`
}

// handleCheck evaluates preconditions locally and returns a ReadinessReport.
func (s *ShellServer) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req CheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := make([]envelope.PreconditionResult, len(req.Preconditions))
	allPassed := true
	var reason string

	for i, p := range req.Preconditions {
		results[i] = s.evalPrecondition(ctx, p)
		if !results[i].Passed && allPassed {
			allPassed = false
			reason = results[i].Message
		}
	}

	writeJSON(w, http.StatusOK, CheckResponse{
		Passed:          allPassed,
		RejectionReason: reason,
		Results:         results,
	})
}

// evalPrecondition evaluates a single precondition on the local filesystem.
func (s *ShellServer) evalPrecondition(ctx context.Context, p envelope.Precondition) envelope.PreconditionResult {
	var result envelope.PreconditionResult
	result.Type = string(p.Type)

	switch p.Type {
	case envelope.PreconditionToolAvailable:
		tool := p.Params["tool"]
		if tool == "" {
			result.Passed = false
			result.Message = "tool parameter missing"
			return result
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("command -v %s", tool))
		cmd.Dir = s.shell.workspace
		if err := cmd.Run(); err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("tool not found: %s", tool)
		} else {
			result.Passed = true
			result.Message = fmt.Sprintf("tool available: %s", tool)
		}

	case envelope.PreconditionGitCleanWorktree:
		cmd := exec.CommandContext(ctx, "git", "-C", s.shell.workspace, "status", "--porcelain")
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
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = s.shell.workspace
		if err := cmd.Run(); err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("command failed: %v", err)
		} else {
			result.Passed = true
			result.Message = "command succeeded"
		}

	default:
		result.Passed = true
		result.Message = fmt.Sprintf("precondition %s: passed (unhandled type)", p.Type)
	}

	return result
}

// ─────────────────────────────────────────────────────────────────
// POST /execute-local — execute assertions via shell (no remote call)
// Used for Mode A (Local Sidecar) where settlement runs embedded.
// ─────────────────────────────────────────────────────────────────

// ExecuteLocalRequest is the POST /execute-local request body.
type ExecuteLocalRequest struct {
	Assertions []envelope.Assertion `json:"assertions"`
}

// ExecuteLocalResponse is the POST /execute-local response body.
type ExecuteLocalResponse struct {
	ReceiptJSON []byte `json:"receipt_json,omitempty"`
	ReceiptID   string `json:"receipt_id,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleExecuteLocal runs assertions via the local shell executor.
func (s *ShellServer) handleExecuteLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req ExecuteLocalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Assertions) == 0 {
		writeError(w, http.StatusBadRequest, "assertions required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rec, err := s.executeLocal(ctx, req.Assertions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	receiptJSON, _ := json.Marshal(rec)
	writeJSON(w, http.StatusOK, ExecuteLocalResponse{
		ReceiptJSON: receiptJSON,
		ReceiptID:   rec.ReceiptID,
		Verdict:     string(rec.Verdict),
	})
}

// executeLocal runs assertions via direct command execution (Mode A: Local Sidecar).
func (s *ShellServer) executeLocal(ctx context.Context, assertions []envelope.Assertion) (*receipt.SettlementReceipt, error) {
	settledAssertions := convertAssertions(assertions)

	eval := settlement.NewEvaluator(s.shell.workspace)
	if s.shell.evalTimeout > 0 {
		eval = eval.WithTimeout(s.shell.evalTimeout)
	}
	results := eval.EvaluateAll(ctx, settledAssertions)

	allPass := true
	for _, r := range results {
		if r.Result != settlement.ResultPass {
			allPass = false
			break
		}
	}

	var verdict receipt.Verdict
	if allPass {
		verdict = receipt.VerdictSettledClean
	} else {
		verdict = receipt.VerdictFailed
	}

	meshID := s.meshID
	if meshID == "" {
		meshID = "gentle-mesh"
	}

	signedAt := time.Now().UTC()
	assertionsJSON, _ := json.Marshal(assertions)
	h := sha256.Sum256(assertionsJSON)
	envHash := hex.EncodeToString(h[:])

	agentID := s.agentID
	if agentID == "" {
		agentID = "local-shell"
	}

	rec := &receipt.SettlementReceipt{
		ProtocolVersion:  receipt.CurrentProtocolVersion,
		MeshID:           meshID,
		ReceiptID:        fmt.Sprintf("rcpt-%d", signedAt.UnixNano()),
		ContractID:       fmt.Sprintf("contract-%d", signedAt.UnixNano()),
		EnvelopeHash:     envHash,
		EmitterAgentID:   agentID,
		ExecutorAgentID:  agentID,
		Verdict:          verdict,
		Assertions:       convertResults(results),
		ExecutorSignedAt: signedAt,
	}

	canonicalJSON, err := jcs.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("JCS marshal: %w", err)
	}
	hashHex, err := jcs.HashHex(canonicalJSON)
	if err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}
	sig, err := signing.SignEnvelope(s.signer, hashHex)
	if err != nil {
		return nil, fmt.Errorf("Ed25519 sign: %w", err)
	}
	rec.ExecutorSignature = sig

	if err := s.chainStore.SaveReceipt(context.Background(), rec); err != nil {
		return nil, fmt.Errorf("save receipt: %w", err)
	}

	return rec, nil
}

// ─────────────────────────────────────────────────────────────────
// POST /dispatch — fan-out: one emitter → N executors concurrently
// ─────────────────────────────────────────────────────────────────

// DispatchRequest is the POST /dispatch request body.
type DispatchRequest struct {
	// Envelopes is one envelope per executor. Each has a distinct executorAgentID.
	Envelopes []envelope.CognitiveTaskEnvelope `json:"envelopes"`
	// ExecutorURLs maps executorAgentID → base URL.
	ExecutorURLs map[string]string `json:"executor_urls"`
}

// DispatchResponse is the POST /dispatch response body.
type DispatchResponse struct {
	Results   []DispatchResult `json:"results"`
	Succeeded int              `json:"succeeded"`
	Failed    int              `json:"failed"`
}

// DispatchResult is the result for one executor leg.
type DispatchResult struct {
	ExecutorID string `json:"executor_id"`
	ReceiptID  string `json:"receipt_id,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Error      string `json:"error,omitempty"`
}

// handleDispatch orchestrates fan-out dispatch to multiple executors concurrently.
func (s *ShellServer) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req DispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Envelopes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one envelope required")
		return
	}
	if len(req.Envelopes) != len(req.ExecutorURLs) {
		writeError(w, http.StatusBadRequest, "ExecutorURLs count must match Envelopes count")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	results := make([]DispatchResult, len(req.Envelopes))
	var wg sync.WaitGroup

	for i, env := range req.Envelopes {
		wg.Add(1)
		go func(idx int, e envelope.CognitiveTaskEnvelope) {
			defer wg.Done()
			results[idx] = s.dispatchOne(ctx, idx, e, req.ExecutorURLs[e.ExecutorAgentID])
		}(i, env)
	}

	wg.Wait()

	succeeded := 0
	for _, r := range results {
		if r.Error == "" {
			succeeded++
		}
	}

	writeJSON(w, http.StatusOK, DispatchResponse{
		Results:   results,
		Succeeded: succeeded,
		Failed:    len(results) - succeeded,
	})
}

// dispatchOne executes one leg of the fan-out: sign + submit + settle.
func (s *ShellServer) dispatchOne(ctx context.Context, idx int, env envelope.CognitiveTaskEnvelope, executorURL string) DispatchResult {
	result := DispatchResult{ExecutorID: env.ExecutorAgentID}

	executorClient, err := NewHTTPClientTLS(executorURL, s.tlsOpts...)
	if err != nil {
		result.Error = fmt.Sprintf("create client: %v", err)
		return result
	}
	defer executorClient.Close()

	hash, err := envelope.ComputeEnvelopeHash(&env)
	if err != nil {
		result.Error = fmt.Sprintf("compute hash: %v", err)
		return result
	}
	env.EnvelopeHash = hash

	signable := env
	signable.EmitterSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		result.Error = fmt.Sprintf("JCS marshal: %v", err)
		return result
	}
	sig, err := s.signer.Sign(canonical)
	if err != nil {
		result.Error = fmt.Sprintf("Ed25519 sign: %v", err)
		return result
	}
	env.EmitterSignature = sig

	envJSON, err := json.Marshal(&env)
	if err != nil {
		result.Error = fmt.Sprintf("marshal: %v", err)
		return result
	}

	leaseResp, err := executorClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		result.Error = fmt.Sprintf("submit: %v", err)
		return result
	}
	if !leaseResp.Accepted {
		result.Error = fmt.Sprintf("lease rejected: %s", leaseResp.Error)
		return result
	}

	settleResp, err := executorClient.Settle(ctx, &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      leaseResp.LeaseID,
	})
	if err != nil {
		result.Error = fmt.Sprintf("settle: %v", err)
		return result
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(settleResp.ReceiptJSON, &rec); err != nil {
		result.Error = fmt.Sprintf("parse receipt: %v", err)
		return result
	}

	result.ReceiptID = rec.ReceiptID
	result.Verdict = string(rec.Verdict)
	log.Printf("[dispatch] executor=%s receipt=%s verdict=%s",
		env.ExecutorAgentID, rec.ReceiptID, rec.Verdict)

	return result
}

// ─────────────────────────────────────────────────────────────────
// GET /chain/:pair — get receipt chain by emitter:executor pair
// ─────────────────────────────────────────────────────────────────

// handleGetChainByPair handles GET /chain/:pair where pair = "emitter:executor".
func (s *ShellServer) handleGetChainByPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	pair := strings.TrimPrefix(r.URL.Path, "/chain/")
	if pair == "" {
		writeError(w, http.StatusBadRequest, "pair required (format: emitter:executor)")
		return
	}

	parts := strings.Split(pair, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeError(w, http.StatusBadRequest, "pair must be emitter:executor (e.g. agent-a:agent-b)")
		return
	}
	emitterID, executorID := parts[0], parts[1]

	chain, err := s.chainStore.GetChain(context.Background(), emitterID, executorID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries := make([]ChainReceiptEntry, len(chain))
	for i, rec := range chain {
		entries[i] = ChainReceiptEntry{
			ReceiptID:           rec.ReceiptID,
			ContractID:          rec.ContractID,
			EnvelopeHash:        rec.EnvelopeHash,
			Verdict:             string(rec.Verdict),
			ExecutorSignedAt:    rec.ExecutorSignedAt.Format(time.RFC3339),
			ExecutorSignature:   rec.ExecutorSignature,
			PreviousReceiptHash: rec.PreviousReceiptHash,
		}
		if rec.EmitterAcceptanceAt != nil {
			entries[i].EmitterSignedAt = rec.EmitterAcceptanceAt.Format(time.RFC3339)
		}
		if rec.EmitterAcceptance != "" {
			entries[i].EmitterAcceptance = string(rec.EmitterAcceptance)
		}
	}

	writeJSON(w, http.StatusOK, ChainResponse{Receipts: entries})
}

// ─────────────────────────────────────────────────────────────────
// Shell helpers (mirrors pkg/shell/shell_helpers.go)
// ─────────────────────────────────────────────────────────────────

// convertAssertions: envelope.Assertion → settlement.Assertion.
func convertAssertions(envAssertions []envelope.Assertion) []*settlement.Assertion {
	result := make([]*settlement.Assertion, len(envAssertions))
	for i, ea := range envAssertions {
		result[i] = &settlement.Assertion{
			ID:     ea.ID,
			Type:   settlement.AssertionType(ea.Type),
			Params: convertParams(ea.Params),
		}
	}
	return result
}

func convertParams(p envelope.AssertionParams) map[string]any {
	m := make(map[string]any)
	if p.Description != "" {
		m["description"] = p.Description
	}
	if p.FilePath != "" {
		m["path"] = p.FilePath
	}
	if p.ExpectedSHA256 != "" {
		m["expected_hash"] = p.ExpectedSHA256
	}
	if p.Command != "" {
		m["command"] = p.Command
	}
	m["expected_code"] = p.ExpectedExitCode // always include (zero is valid)
	if p.ContainsPattern != "" {
		m["contains"] = p.ContainsPattern
	}
	if p.ContainsRegex {
		m["regex"] = true
	}
	if p.WorkingDir != "" {
		m["cwd"] = p.WorkingDir
	}
	if len(p.Env) > 0 {
		m["env"] = p.Env
	}
	return m
}

// convertResults: settlement.AssertionResult → receipt.AssertionResult.
func convertResults(sr []*settlement.AssertionResult) []receipt.AssertionResult {
	result := make([]receipt.AssertionResult, len(sr))
	for i, s := range sr {
		var rcptResult receipt.Result
		switch s.Result {
		case settlement.ResultPass:
			rcptResult = receipt.ResultPass
		default:
			rcptResult = receipt.ResultFail
		}
		result[i] = receipt.AssertionResult{
			AssertionIndex: s.AssertionIndex,
			AssertionID:    s.AssertionID,
			AssertionType:  string(s.AssertionType),
			Result:         rcptResult,
			Evidence: receipt.AssertionEvidence{
				Command:         s.Evidence.Command,
				ExitCode:        s.Evidence.ExitCode,
				StdoutHash:      sha256Hex(s.Evidence.Stdout),
				StderrHash:      sha256Hex(s.Evidence.Stderr),
				ExpectedSHA256:  s.Evidence.ExpectedHash,
				ActualSHA256:    s.Evidence.ActualHash,
				FileWasModified: s.Evidence.FileExists,
				GitStatus:       s.Evidence.GitStatus,
				PortWasFree:     s.Evidence.PortAvailable,
			},
			Message: s.Message,
		}
	}
	return result
}

func sha256Hex(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
