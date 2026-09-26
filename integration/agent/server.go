// Package agent provides HTTP server and client for RFC-002 agent communication.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// Server is an HTTP server that implements the RFC-002 agent protocol.
type Server struct {
	config     Config
	httpServer *http.Server
	chainStore *receipt.ChainStore
	evaluator  *settlement.Evaluator
	engine     *settlement.Engine
	signer     *signing.BasicSigner

	// In-memory state for the integration test.
	leases map[string]*envelope.Lease // leaseID → Lease
}

// NewServer creates a new agent server with the given configuration.
func NewServer(cfg Config) (*Server, error) {
	if cfg.AgentID == "" {
		return nil, errors.New("agent_id is required")
	}
	if cfg.Role == "" {
		cfg.Role = RoleExecutor
	}
	if cfg.EvalTimeout == 0 {
		cfg.EvalTimeout = 30 * time.Second
	}
	if cfg.MaxRemed == 0 {
		cfg.MaxRemed = 1
	}
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "/srv/workspace"
	}
	if cfg.ChainDBPath == "" {
		cfg.ChainDBPath = "/data/chain.db"
	}

	// Ensure chain DB directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.ChainDBPath), 0755); err != nil {
		return nil, fmt.Errorf("create chain db dir: %w", err)
	}

	// Open SQLite chain store.
	db, err := sql.Open("sqlite", cfg.ChainDBPath)
	if err != nil {
		return nil, fmt.Errorf("open chain db: %w", err)
	}
	db.SetMaxOpenConns(1)
	chainStore := receipt.NewChainStore(db)
	if err := chainStore.InitSchema(context.Background()); err != nil {
		return nil, fmt.Errorf("init chain schema: %w", err)
	}

	// Create Ed25519 signer (in-memory, key generated on startup).
	signer, err := signing.GenerateSigner(cfg.AgentID)
	if err != nil {
		return nil, fmt.Errorf("generate signer: %w", err)
	}

	// Create settlement engine.
	evaluator := settlement.NewEvaluator(cfg.WorkspaceDir)
	engine, err := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:       evaluator,
		ChainStore:     chainStore,
		ExecutorSigner: signer,
		RemediationMax: cfg.MaxRemed,
	})
	if err != nil {
		return nil, fmt.Errorf("create settlement engine: %w", err)
	}

	return &Server{
		config:     cfg,
		chainStore: chainStore,
		evaluator:  evaluator,
		engine:     engine,
		signer:     signer,
		leases:     make(map[string]*envelope.Lease),
	}, nil
}

// Run starts the HTTP server on the given port.
// If TLSCertFile and TLSKeyFile are configured, it runs HTTPS.
func (s *Server) Run(port int) error {
	if s.config.TLSCertFile != "" && s.config.TLSKeyFile != "" {
		return s.runTLS(port)
	}
	return s.runHTTP(port)
}

// runHTTP starts a plain HTTP server.
func (s *Server) runHTTP(port int) error {
	mux := http.NewServeMux()
	s.registerHandlers(mux)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("[%s] HTTP server starting on :%d (role=%s)", s.config.AgentID, port, s.config.Role)
	return s.httpServer.ListenAndServe()
}

// runTLS starts a TLS/HTTPS server.
// If ClientCAFile is set, it enables mutual TLS (mTLS).
func (s *Server) runTLS(port int) error {
	mux := http.NewServeMux()
	s.registerHandlers(mux)

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{
			tls.CurveP256,
			tls.X25519,
		},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	// Load server certificate.
	tlsConfig.Certificates = make([]tls.Certificate, 1)
	var err error
	tlsConfig.Certificates[0], err = tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}

	// Load CA cert for mTLS client certificate verification.
	if s.config.ClientCAFile != "" {
		caPEM, err := os.ReadFile(s.config.ClientCAFile)
		if err != nil {
			return fmt.Errorf("read client CA: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caPEM) {
			return fmt.Errorf("failed to parse client CA certificate")
		}
		tlsConfig.ClientCAs = caCertPool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		log.Printf("[%s] mTLS enabled: client certificates required (CA=%s)",
			s.config.AgentID, s.config.ClientCAFile)
	}

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		TLSConfig:   tlsConfig,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("[%s] HTTPS server starting on :%d (role=%s, cert=%s)",
		s.config.AgentID, port, s.config.Role, s.config.TLSCertFile)
	return s.httpServer.ListenAndServeTLS("", "") // certs come from TLSConfig
}

// registerHandlers registers all HTTP handlers on the given mux.
func (s *Server) registerHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)

	// Agent A endpoints (emitter).
	mux.HandleFunc("POST /envelopes", s.handleSubmitEnvelope)
	mux.HandleFunc("POST /verify", s.handleVerifyReceipt)

	// Agent B endpoints (executor).
	mux.HandleFunc("POST /leases", s.handleCreateLease)
	mux.HandleFunc("POST /execute", s.handleExecute)
	mux.HandleFunc("POST /settle", s.handleSettle)

	// Shared.
	mux.HandleFunc("GET /receipts/", s.handleGetReceipt)
	mux.HandleFunc("GET /chain", s.handleGetChain)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// PublicKey returns the agent's Ed25519 public key as hex string.
func (s *Server) PublicKey() string {
	return hexEncode(s.signer.PublicKey())
}

// Signer returns the agent's signer.
func (s *Server) Signer() *signing.BasicSigner {
	return s.signer
}

// ChainStore returns the receipt chain store.
func (s *Server) ChainStore() *receipt.ChainStore {
	return s.chainStore
}

// ─────────────────────────────────────────────────────────────────
// HTTP handlers
// ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &HealthResponse{
		Status:    "ok",
		AgentID:   s.config.AgentID,
		Role:      string(s.config.Role),
		Timestamp: time.Now().UTC(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":      s.config.AgentID,
		"role":          s.config.Role,
		"public_key":    hexEncode(s.signer.PublicKey()),
		"workspace_dir": s.config.WorkspaceDir,
	})
}

// handleSubmitEnvelope handles POST /envelopes.
// Called by A to submit a contract to B.
func (s *Server) handleSubmitEnvelope(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req EnvelopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if len(req.EnvelopeJSON) == 0 {
		writeError(w, http.StatusBadRequest, "envelope_json required")
		return
	}

	env := new(envelope.CognitiveTaskEnvelope)
	if err := json.Unmarshal(req.EnvelopeJSON, env); err != nil {
		writeError(w, http.StatusBadRequest, "parse envelope: "+err.Error())
		return
	}
	if err := envelope.Validate(env); err != nil {
		writeError(w, http.StatusBadRequest, "validate: "+err.Error())
		return
	}

	// Pre-flight: check preconditions synchronously.
	precondResults := s.runPreconditions(env.Preconditions)
	allPassed := true
	for _, r := range precondResults {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	lease := &envelope.Lease{
		LeaseID:            fmt.Sprintf("lease-%d", time.Now().UnixNano()),
		EnvelopeID:          env.EnvelopeID,
		ExecutorAgentID:     s.config.AgentID,
		Accepted:           allPassed,
		PreconditionResults: precondResults,
		ExpiresAt:          time.Now().Add(time.Duration(env.TimeoutSeconds) * time.Second),
	}

	// Sign the lease.
	leaseBytes, _ := json.Marshal(lease)
	leaseSig, _ := s.signer.Sign(leaseBytes)
	lease.ExecutorSignature = leaseSig

	s.leases[lease.LeaseID] = lease

	log.Printf("[%s] Lease %s: accepted=%v (%d preconditions)",
		s.config.AgentID, lease.LeaseID, lease.Accepted, len(precondResults))

	resp := &EnvelopeResponse{Accepted: lease.Accepted, LeaseID: lease.LeaseID}
	if !lease.Accepted {
		resp.Error = "preconditions not met"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCreateLease handles POST /leases.
func (s *Server) handleCreateLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req struct{ EnvelopeJSON []byte }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	env := new(envelope.CognitiveTaskEnvelope)
	if err := json.Unmarshal(req.EnvelopeJSON, env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	precondResults := s.runPreconditions(env.Preconditions)
	allPassed := true
	for _, r := range precondResults {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	lease := &envelope.Lease{
		LeaseID:            fmt.Sprintf("lease-%d", time.Now().UnixNano()),
		EnvelopeID:          env.EnvelopeID,
		ExecutorAgentID:     s.config.AgentID,
		Accepted:           allPassed,
		PreconditionResults: precondResults,
		ExpiresAt:          time.Now().Add(time.Duration(env.TimeoutSeconds) * time.Second),
	}

	leaseBytes, _ := json.Marshal(lease)
	leaseSig, _ := s.signer.Sign(leaseBytes)
	lease.ExecutorSignature = leaseSig

	s.leases[lease.LeaseID] = lease
	writeJSON(w, http.StatusOK, lease)
}

// handleExecute handles POST /execute (simulated task execution).
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.config.EvalTimeout)
	defer cancel()

	workDir := req.WorkingDir
	if workDir == "" {
		workDir = s.config.WorkspaceDir
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Command)
	cmd.Dir = workDir
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	log.Printf("[%s] Execute: %q → exit=%d", s.config.AgentID, req.Command, exitCode)

	writeJSON(w, http.StatusOK, &ExecuteResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}

// handleSettle handles POST /settle — triggers the settlement engine.
func (s *Server) handleSettle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req SettleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.EnvelopeJSON) == 0 {
		writeError(w, http.StatusBadRequest, "envelope_json required")
		return
	}

	env := new(envelope.CognitiveTaskEnvelope)
	if err := json.Unmarshal(req.EnvelopeJSON, env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.config.EvalTimeout)
	defer cancel()

	input := settlement.SettlementInput{
		Envelope:        env,
		EmitterAgentID:  env.EmitterAgentID,
		ExecutorAgentID: s.config.AgentID,
	}

	out, err := s.engine.Settle(ctx, input)
	if err != nil {
		log.Printf("[%s] Settle error: %v", s.config.AgentID, err)
		writeError(w, http.StatusInternalServerError, "settlement: "+err.Error())
		return
	}

	receiptJSON, err := json.Marshal(out.Receipt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Printf("[%s] Settled: receipt=%s verdict=%s assertions=%d",
		s.config.AgentID, out.Receipt.ReceiptID, out.Receipt.Verdict, len(out.Receipt.Assertions))

	writeJSON(w, http.StatusOK, &SettleResponse{
		ReceiptJSON: receiptJSON,
		ReceiptID:   out.Receipt.ReceiptID,
	})
}

// handleGetReceipt handles GET /receipts/{id}.
func (s *Server) handleGetReceipt(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/receipts/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "receipt id required")
		return
	}

	ctx := context.Background()
	rec, err := s.chainStore.GetReceipt(ctx, id)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, "not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recJSON, _ := json.Marshal(rec)
	writeJSON(w, http.StatusOK, &ReceiptResponse{
		ReceiptJSON: recJSON,
		ReceiptID:   rec.ReceiptID,
	})
}

// handleGetChain handles GET /chain?emitter=&executor=.
func (s *Server) handleGetChain(w http.ResponseWriter, r *http.Request) {
	emitter := r.URL.Query().Get("emitter")
	executor := r.URL.Query().Get("executor")
	if emitter == "" || executor == "" {
		writeError(w, http.StatusBadRequest, "emitter and executor query params required")
		return
	}

	ctx := context.Background()
	chain, err := s.chainStore.GetChain(ctx, emitter, executor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries := make([]ChainReceiptEntry, len(chain))
	for i, rec := range chain {
		entries[i] = ChainReceiptEntry{
			ReceiptID:         rec.ReceiptID,
			ContractID:        rec.ContractID,
			EnvelopeHash:     rec.EnvelopeHash,
			Verdict:          string(rec.Verdict),
			ExecutorSignedAt: rec.ExecutorSignedAt.Format(time.RFC3339),
		}
		if rec.EmitterAcceptanceAt != nil {
			entries[i].EmitterSignedAt = rec.EmitterAcceptanceAt.Format(time.RFC3339)
		}
		if rec.EmitterAcceptance != "" {
			entries[i].EmitterAcceptance = string(rec.EmitterAcceptance)
		}
	}

	writeJSON(w, http.StatusOK, &ChainResponse{Receipts: entries})
}

// handleVerifyReceipt handles POST /verify.
func (s *Server) handleVerifyReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rec := new(receipt.SettlementReceipt)
	if err := json.Unmarshal(req.ReceiptJSON, rec); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := &VerifyResponse{Errors: []string{}, ChainErrors: []string{}}

	// 1. Verify executor signature.
	hash, err := receipt.ComputeReceiptHash(rec)
	if err != nil {
		resp.Errors = append(resp.Errors, "compute hash: "+err.Error())
	} else {
		err := signing.Verify(s.signer.PublicKey(), []byte(hash), rec.ExecutorSignature)
		resp.ExecutorSigOK = (err == nil)
		if err != nil {
			resp.Errors = append(resp.Errors, "executor signature: "+err.Error())
		}
	}

	// 2. Verify chain integrity.
	chainCtx := context.Background()
	chain, err := s.chainStore.GetChain(chainCtx, rec.EmitterAgentID, rec.ExecutorAgentID)
	if err != nil {
		resp.ChainErrors = append(resp.ChainErrors, "get chain: "+err.Error())
		resp.ChainValid = false
	} else {
		for i, c := range chain {
			if c.ReceiptID == rec.ReceiptID {
				if i == 0 {
					resp.ChainValid = rec.PreviousReceiptHash == ""
					if !resp.ChainValid {
						resp.ChainErrors = append(resp.ChainErrors,
							fmt.Sprintf("first receipt must have empty prev_hash, got %s",
								rec.PreviousReceiptHash))
					}
				} else {
					resp.ChainValid = rec.PreviousReceiptHash == chain[i-1].PreviousReceiptHash
					if !resp.ChainValid {
						resp.ChainErrors = append(resp.ChainErrors,
							fmt.Sprintf("chain[%d]: expected %s, got %s",
								i, chain[i-1].PreviousReceiptHash, rec.PreviousReceiptHash))
					}
				}
				break
			}
		}
		if len(resp.ChainErrors) == 0 && !resp.ChainValid {
			resp.ChainErrors = append(resp.ChainErrors, "receipt not found in chain")
			resp.ChainValid = false
		}
	}

	resp.Valid = len(resp.Errors) == 0 && resp.ChainValid
	writeJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────
// Preconditions
// ─────────────────────────────────────────────────────────────────

func (s *Server) runPreconditions(preconds []envelope.Precondition) []envelope.PreconditionResult {
	results := make([]envelope.PreconditionResult, len(preconds))
	for i, p := range preconds {
		result := envelope.PreconditionResult{
			PreconditionIndex: i,
			Type:             string(p.Type),
			Passed:           false,
		}

		switch p.Type {
		case envelope.PreconditionToolAvailable:
			tool, _, ok := p.ToolParams()
			if ok && tool != "" {
				result.Passed = checkToolAvailable(tool)
				result.Message = fmt.Sprintf("tool %q available=%v", tool, result.Passed)
			} else {
				result.Message = "tool param missing"
			}

		case envelope.PreconditionCommandExitCode:
			cmd, ok := p.CommandParams()
			if ok && cmd != "" {
				exitCode := runCheckCommand(cmd, s.config.WorkspaceDir)
				result.Passed = exitCode == 0
				result.Message = fmt.Sprintf("command %q exit=%d", cmd, exitCode)
			} else {
				result.Message = "command param missing"
			}

		case envelope.PreconditionGitCleanWorktree:
			exitCode := runCheckCommand("git status --porcelain", s.config.WorkspaceDir)
			result.Passed = exitCode == 0
			result.Message = "git worktree clean=" + fmt.Sprint(result.Passed)

		case envelope.PreconditionPortAvailable:
			if pPort, ok := p.PortParams(); ok && pPort > 0 {
				result.Passed = checkPortAvailable(pPort)
				result.Message = fmt.Sprintf("port %d available=%v", pPort, result.Passed)
			}

		default:
			result.Message = fmt.Sprintf("unknown precondition: %s", p.Type)
		}

		results[i] = result
	}
	return results
}

// checkToolAvailable checks if a binary is in PATH.
func checkToolAvailable(tool string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("command -v %s", tool))
	return cmd.Run() == nil
}

// runCheckCommand runs a command and returns the exit code.
func runCheckCommand(cmdStr, dir string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	if dir != "" {
		cmd.Dir = dir
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return -1
	}
	return 0
}

// checkPortAvailable checks if a TCP port is available.
func checkPortAvailable(port int) bool {
	addr := fmt.Sprintf("localhost:%d", port)
	conn, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// hexEncode encodes bytes to hex string.
func hexEncode(data []byte) string {
	return hex.EncodeToString(data)
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, &ErrorResponse{Error: msg, Code: status})
}
