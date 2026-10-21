// Package agent provides HTTP server and client for RFC-002 agent communication.
package agent

import (
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
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/keystore"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// Server is an HTTP server that implements the RFC-002 agent protocol.
type Server struct {
	config      Config
	httpServer  *http.Server
	chainStore  *receipt.ChainStore
	evaluator   *settlement.Evaluator
	engine      *settlement.Engine
	signerMu    sync.RWMutex
	signer      signing.Signer
	keyResolver PublicKeyResolver

	// In-memory state for the integration test.
	leasesMu sync.RWMutex
	leases   map[string]*envelope.Lease // leaseID → Lease

	// R2 (RFC-002 #69): envelope registry for lease expiration. The reaper
	// needs the envelope hash and agent IDs to build the timeout receipt.
	leaseEnvelopes map[string]*envelope.CognitiveTaskEnvelope // EnvelopeID → envelope

	// R2: reaper lifecycle — context cancellation, no orphan goroutines.
	reaperDone   chan struct{}
	reaperCancel context.CancelFunc
}

// inMemoryKeyResolver is a thread-safe in-memory key resolver.
type inMemoryKeyResolver struct {
	mu   sync.RWMutex
	keys map[string][]byte
}

func newInMemoryKeyResolver() *inMemoryKeyResolver {
	return &inMemoryKeyResolver{keys: make(map[string][]byte)}
}

func (r *inMemoryKeyResolver) Register(agentID string, key []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[agentID] = key
}

func (r *inMemoryKeyResolver) GetAgentPublicKey(agentID string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[agentID]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in keystore", agentID)
	}
	return k, nil
}

// NewServer creates a new agent server with the given configuration.
func NewServer(cfg Config) (*Server, error) {
	if cfg.AgentID == "" {
		return nil, errors.New("agent_id is required")
	}
	if cfg.MeshID == "" {
		return nil, errors.New("mesh_id is required")
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
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure chain db pragmas: %w", err)
	}
	chainStore := receipt.NewChainStore(db)
	if err := chainStore.InitSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init chain schema: %w", err)
	}

	// Initialize signer and key resolver.
	// If KeystoreDir is configured, use pkg/keystore for persistent keys.
	var (
		signer      signing.Signer
		keyResolver PublicKeyResolver = cfg.KeyResolver
	)

	if cfg.KeystoreDir != "" {
		ks, err := keystore.New(cfg.KeystoreDir, cfg.AgentID, true)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("init keystore: %w", err)
		}
		if keyResolver == nil {
			keyResolver = ks
		}
		signer = ks
	}

	if signer == nil {
		var err error
		signer, err = signing.GenerateSigner(cfg.AgentID)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("generate signer: %w", err)
		}
	}

	if keyResolver == nil {
		keyResolver = newInMemoryKeyResolver()
	}

	// Create settlement engine.
	evaluator := settlement.NewEvaluator(cfg.WorkspaceDir)
	engine, err := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:      evaluator,
		ChainStore:     chainStore,
		ExecutorSigner: signer,
		RemediationMax: cfg.MaxRemed,
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create settlement engine: %w", err)
	}

	srv := &Server{
		config:         cfg,
		chainStore:     chainStore,
		evaluator:      evaluator,
		engine:         engine,
		signer:         signer,
		keyResolver:    keyResolver,
		leases:         make(map[string]*envelope.Lease),
		leaseEnvelopes: make(map[string]*envelope.CognitiveTaskEnvelope),
	}
	if signer != nil {
		_ = srv.RegisterAgentKey(signer.AgentID(), signer.PublicKey())
	}
	if cfg.LeaseReaperInterval > 0 {
		srv.startLeaseReaper(cfg.LeaseReaperInterval)
	}
	return srv, nil
}

// RegisterAgentKey registers an agent's public key with the server's key resolver.
func (s *Server) RegisterAgentKey(agentID string, pubKey []byte) error {
	if mem, ok := s.keyResolver.(*inMemoryKeyResolver); ok {
		mem.Register(agentID, pubKey)
		return nil
	}
	type dynamicRegistrar interface {
		AddAgentKey(agentID string, publicKeyHex string) error
	}
	if reg, ok := s.keyResolver.(dynamicRegistrar); ok {
		return reg.AddAgentKey(agentID, hex.EncodeToString(pubKey))
	}
	return errors.New("key resolver does not support dynamic registration")
}

// KeyResolver returns the active public key resolver.
func (s *Server) KeyResolver() PublicKeyResolver {
	return s.keyResolver
}

func (s *Server) getEmitterPublicKey(agentID string) ([]byte, error) {
	if s.keyResolver == nil {
		return nil, errors.New("no key resolver configured")
	}
	return s.keyResolver.GetAgentPublicKey(agentID)
}

func (s *Server) verifyEnvelope(env *envelope.CognitiveTaskEnvelope) error {
	if env == nil {
		return errors.New("envelope is nil")
	}
	if env.EmitterSignature == "" {
		return errors.New("envelope is unsigned: emitter_signature required (S2)")
	}
	pubKey, err := s.getEmitterPublicKey(env.EmitterAgentID)
	if err != nil {
		return fmt.Errorf("emitter agent %q key not registered: %w", env.EmitterAgentID, err)
	}
	if err := envelope.VerifyEnvelopeSignature(env, pubKey); err != nil {
		return fmt.Errorf("invalid envelope signature: %w", err)
	}
	return nil
}

func (s *Server) verifyEmitterReceiptSignature(stored *receipt.SettlementReceipt, emitterSig string, acceptance receipt.Acceptance, disputeReason string) error {
	emitterPubKey, err := s.getEmitterPublicKey(stored.EmitterAgentID)
	if err != nil {
		return fmt.Errorf("emitter key not found: %w", err)
	}
	candidate := *stored
	candidate.EmitterAcceptance = acceptance
	candidate.DisputeReason = disputeReason
	candidate.EmitterSignature = emitterSig
	if err := receipt.VerifyEmitterSignature(&candidate, emitterPubKey); err != nil {
		return fmt.Errorf("emitter signature invalid: %w", err)
	}
	return nil
}

// verifyMTLSIdentity verifies that under TLS, a client certificate is presented
// and its Subject CommonName or one of its SAN DNSNames matches claimedAgentID (RFC-002 S1).
// Plain HTTP (r.TLS == nil) is permitted solely for local dev / non-TLS unit tests.
// Under TLS with mTLS enabled (s.config.ClientCAFile != ""), a client certificate is strictly required;
// missing certificates or identity discrepancies return an error that maps to HTTP 403 Forbidden.
func (s *Server) verifyMTLSIdentity(r *http.Request, claimedAgentID string) error {
	if r.TLS == nil {
		return nil
	}
	if len(r.TLS.PeerCertificates) == 0 {
		if s.config.ClientCAFile != "" {
			return errors.New("mTLS client certificate required under TLS")
		}
		return nil
	}
	cert := r.TLS.PeerCertificates[0]
	if cert.Subject.CommonName == claimedAgentID {
		return nil
	}
	for _, san := range cert.DNSNames {
		if san == claimedAgentID {
			return nil
		}
	}
	return fmt.Errorf("client certificate CN %q and SANs %v do not match claimed agent %q",
		cert.Subject.CommonName, cert.DNSNames, claimedAgentID)
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
		TLSConfig:    tlsConfig,
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
	mux.HandleFunc("POST /preflight", s.handlePreflight)
	mux.HandleFunc("POST /leases", s.handleCreateLease)
	mux.HandleFunc("POST /settle", s.handleSettle)
	mux.HandleFunc("POST /accept", s.handleAccept)
	mux.HandleFunc("POST /dispute", s.handleDispute)
	mux.HandleFunc("POST /verify-chain", s.handleVerifyChain)

	// Shared.
	mux.HandleFunc("GET /receipts/", s.handleGetReceipt)
	mux.HandleFunc("GET /chain", s.handleGetChain)

	// Test-harness-only endpoints: /execute and /inject-receipt.
	// The stub (no-op) is in server_harness_stub.go; the real registration
	// is in server_harness.go when building with -tags testharness.
	s.registerTestHarnessEndpoints(mux)
}

// Shutdown gracefully shuts down the server: it stops the lease reaper first
// (context cancellation, no orphan goroutines) and then the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.reaperCancel != nil {
		s.reaperCancel()
		if s.reaperDone != nil {
			select {
			case <-s.reaperDone:
			case <-ctx.Done():
			}
		}
	}
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// startLeaseReaper launches the lease expiration monitor (RFC-002 R2).
// Every tick it purges expired leases and emits a SETTLEMENT_TIMEOUT receipt
// for each — autonomously, without any incoming request. It stops cleanly on
// context cancellation and closes reaperDone.
func (s *Server) startLeaseReaper(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	s.reaperCancel = cancel
	done := make(chan struct{})
	s.reaperDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.purgeExpiredLeases(time.Now())
			}
		}
	}()
}

// expiredLease is a snapshot entry of one expired lease. Lease identity and
// ExpiresAt are copied BY VALUE at snapshot time: the confirmation phase
// compares them against the live state, so a renewal (a new future ExpiresAt
// under the same leaseID) is detected and the lease survives.
type expiredLease struct {
	leaseID    string
	envelopeID string
	expiresAt  time.Time
	envelope   *envelope.CognitiveTaskEnvelope // immutable once signed; nil if unregistered
}

// snapshotExpiredLeases (reaper phase 1) reads the currently expired leases
// with their envelopes. Read-only: nothing is purged here.
func (s *Server) snapshotExpiredLeases(now time.Time) []expiredLease {
	s.leasesMu.RLock()
	defer s.leasesMu.RUnlock()
	var out []expiredLease
	for _, l := range s.leases {
		if !l.ExpiresAt.After(now) {
			out = append(out, expiredLease{
				leaseID:    l.LeaseID,
				envelopeID: l.EnvelopeID,
				expiresAt:  l.ExpiresAt,
				envelope:   s.leaseEnvelopes[l.EnvelopeID],
			})
		}
	}
	return out
}

// confirmAndEmit (reaper phase 2) confirms each snapshot still matches the
// live lease, purges the confirmed ones, and emits one SETTLEMENT_TIMEOUT
// receipt per confirmed lease with a registered envelope.
//
// Locking contract: leasesMu is NEVER held during receipt I/O. The
// confirmation (a compare-and-delete: the purge only takes effect when
// ExpiresAt still equals the snapshot value) runs in one short critical
// section; receipt persistence runs afterwards with no server locks held
// (chainMu is taken inside SignAndSaveReceipt). No path holds leasesMu and
// chainMu together, so lock-order deadlock is impossible by construction.
func (s *Server) confirmAndEmit(snaps []expiredLease, now time.Time) (confirmed, emitted int) {
	var toEmit []*envelope.CognitiveTaskEnvelope

	s.leasesMu.Lock()
	for _, sn := range snaps {
		l, ok := s.leases[sn.leaseID]
		if !ok {
			continue // already purged by another path
		}
		// Compare-and-delete: a renewal changed ExpiresAt under the same
		// leaseID — the lease is legitimately alive again; skip entirely.
		if !l.ExpiresAt.Equal(sn.expiresAt) {
			continue
		}
		delete(s.leases, sn.leaseID)
		if sn.envelopeID != "" {
			delete(s.leaseEnvelopes, sn.envelopeID)
		}
		confirmed++
		toEmit = append(toEmit, sn.envelope)
	}
	s.leasesMu.Unlock()

	for _, env := range toEmit {
		rec, err := s.emitSettlementTimeout(env)
		if err != nil {
			log.Printf("[%s] reaper: emit SETTLEMENT_TIMEOUT failed: %v", s.config.AgentID, err)
			continue
		}
		if rec != nil && rec.Verdict == receipt.VerdictTimeout {
			log.Printf("[%s] reaper: lease expired, emitted SETTLEMENT_TIMEOUT receipt=%s contract=%s",
				s.config.AgentID, rec.ReceiptID, rec.ContractID)
			emitted++
		}
	}
	return confirmed, emitted
}

// purgeExpiredLeases runs both reaper phases and returns how many leases
// were confirmed purged. Used by the reaper ticker and by tests.
func (s *Server) purgeExpiredLeases(now time.Time) int {
	snaps := s.snapshotExpiredLeases(now)
	confirmed, _ := s.confirmAndEmit(snaps, now)
	return confirmed
}

// emitSettlementTimeout persists a SETTLEMENT_TIMEOUT receipt for the given
// envelope, idempotently (RFC-002 R1): a contract that already carries a
// receipt — settled before expiry, or a racing settle that won the UNIQUE
// index — keeps its winner; no timeout receipt is added.
func (s *Server) emitSettlementTimeout(env *envelope.CognitiveTaskEnvelope) (*receipt.SettlementReceipt, error) {
	if env == nil {
		// No envelope registered (lease created outside the envelope flow):
		// purge silently, there is no contract to time out.
		return nil, nil
	}
	if s.signer == nil {
		return nil, errors.New("server signer unavailable")
	}

	// R1 idempotency: an existing receipt for this contract wins.
	if existing, err := s.chainStore.GetReceiptByContractID(context.Background(), env.EnvelopeID); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !errors.Is(err, receipt.ErrReceiptNotFound) {
		return nil, fmt.Errorf("check existing receipt: %w", err)
	}

	envHash := env.EnvelopeHash
	if envHash == "" {
		h, err := envelope.ComputeEnvelopeHash(env)
		if err != nil {
			return nil, fmt.Errorf("compute envelope hash: %w", err)
		}
		envHash = h
	}

	rec, err := receipt.SignAndSaveReceipt(context.Background(), s.chainStore, receipt.AppendConfig{
		EmitterAgentID:  env.EmitterAgentID,
		ExecutorAgentID: s.config.AgentID,
		Signer:          s.signer,
	}, func() (*receipt.SettlementReceipt, error) {
		return &receipt.SettlementReceipt{
			ProtocolVersion: receipt.CurrentProtocolVersion,
			MeshID:          env.MeshID,
			ContractID:      env.EnvelopeID,
			EnvelopeHash:    envHash,
			Verdict:         receipt.VerdictTimeout,
			// No assertions: the executor aborted without executing anything.
		}, nil
	})
	if err != nil {
		if errors.Is(err, receipt.ErrDuplicateContract) {
			// Lost a UNIQUE (contract_id) race against a legitimate settle:
			// R1 arbitrates — the winner stands, no timeout receipt is added.
			return s.chainStore.GetReceiptByContractID(context.Background(), env.EnvelopeID)
		}
		return nil, err
	}
	return rec, nil
}

// PublicKey returns the agent's Ed25519 public key as hex string.
func (s *Server) PublicKey() string {
	s.signerMu.RLock()
	defer s.signerMu.RUnlock()
	return hexEncode(s.signer.PublicKey())
}

// Signer returns the agent's signer.
func (s *Server) Signer() signing.Signer {
	s.signerMu.RLock()
	defer s.signerMu.RUnlock()
	return s.signer
}

// SetSigner overrides the agent's signer (useful in tests to simulate signing failures).
func (s *Server) SetSigner(signer signing.Signer) {
	s.signerMu.Lock()
	defer s.signerMu.Unlock()
	s.signer = signer
}

// ChainStore returns the receipt chain store.
func (s *Server) ChainStore() *receipt.ChainStore {
	return s.chainStore
}

// GetLease retrieves a lease by ID with read lock.
func (s *Server) GetLease(leaseID string) (*envelope.Lease, bool) {
	s.leasesMu.RLock()
	defer s.leasesMu.RUnlock()
	l, ok := s.leases[leaseID]
	return l, ok
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
		PublicKey: s.PublicKey(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":      s.config.AgentID,
		"role":          s.config.Role,
		"public_key":    s.PublicKey(),
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
	if env.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, envelope.ErrMeshMismatch.Error())
		return
	}

	// S1: Verify mTLS client certificate identity matches emitter agent ID.
	if err := s.verifyMTLSIdentity(r, env.EmitterAgentID); err != nil {
		writeError(w, http.StatusForbidden, "mTLS identity mismatch: "+err.Error())
		return
	}

	// S2: Verify emitter's signature before issuing lease.
	if err := s.verifyEnvelope(env); err != nil {
		writeError(w, http.StatusUnauthorized, "verify envelope: "+err.Error())
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

	meshID := s.config.MeshID

	lease := &envelope.Lease{
		ProtocolVersion:     envelope.CurrentProtocolVersion,
		MeshID:              meshID,
		LeaseID:             fmt.Sprintf("lease-%d", time.Now().UnixNano()),
		EnvelopeID:          env.EnvelopeID,
		ExecutorAgentID:     s.config.AgentID,
		Accepted:            allPassed,
		PreconditionResults: precondResults,
		ExpiresAt:           time.Now().Add(time.Duration(env.TimeoutSeconds) * time.Second),
	}

	// Sign the lease.
	if err := envelope.SignLease(lease, s.Signer()); err != nil {
		log.Printf("[%s] Sign lease error: %v", s.config.AgentID, err)
		writeError(w, http.StatusInternalServerError, "sign lease: "+err.Error())
		return
	}

	s.leasesMu.Lock()
	s.leases[lease.LeaseID] = lease
	s.leaseEnvelopes[env.EnvelopeID] = env
	s.leasesMu.Unlock()

	log.Printf("[%s] Lease %s: accepted=%v (%d preconditions)",
		s.config.AgentID, lease.LeaseID, lease.Accepted, len(precondResults))

	resp := &EnvelopeResponse{Accepted: lease.Accepted, LeaseID: lease.LeaseID}
	if !lease.Accepted {
		resp.Error = "preconditions not met"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePreflight handles POST /preflight — evaluates preconditions without creating a lease (RFC-002 A2).
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req PreflightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	results := s.runPreconditions(req.Preconditions)
	allPassed := true
	var reason string
	for _, res := range results {
		if !res.Passed {
			allPassed = false
			if reason == "" {
				reason = res.Message
			}
		}
	}

	writeJSON(w, http.StatusOK, &PreflightResponse{
		Passed:          allPassed,
		RejectionReason: reason,
		Results:         results,
	})
}

// handleCreateLease handles POST /leases.
func (s *Server) handleCreateLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req struct {
		EnvelopeJSON []byte `json:"envelope_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	env := new(envelope.CognitiveTaskEnvelope)
	if err := json.Unmarshal(req.EnvelopeJSON, env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := envelope.Validate(env); err != nil {
		writeError(w, http.StatusBadRequest, "validate: "+err.Error())
		return
	}
	if env.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, envelope.ErrMeshMismatch.Error())
		return
	}

	// S1: Verify mTLS client certificate identity matches emitter agent ID.
	if err := s.verifyMTLSIdentity(r, env.EmitterAgentID); err != nil {
		writeError(w, http.StatusForbidden, "mTLS identity mismatch: "+err.Error())
		return
	}

	// S2: Verify emitter's signature before creating lease.
	if err := s.verifyEnvelope(env); err != nil {
		writeError(w, http.StatusUnauthorized, "verify envelope: "+err.Error())
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

	meshID := s.config.MeshID

	lease := &envelope.Lease{
		ProtocolVersion:     envelope.CurrentProtocolVersion,
		MeshID:              meshID,
		LeaseID:             fmt.Sprintf("lease-%d", time.Now().UnixNano()),
		EnvelopeID:          env.EnvelopeID,
		ExecutorAgentID:     s.config.AgentID,
		Accepted:            allPassed,
		PreconditionResults: precondResults,
		ExpiresAt:           time.Now().Add(time.Duration(env.TimeoutSeconds) * time.Second),
	}

	if err := envelope.SignLease(lease, s.Signer()); err != nil {
		log.Printf("[%s] Sign lease error: %v", s.config.AgentID, err)
		writeError(w, http.StatusInternalServerError, "sign lease: "+err.Error())
		return
	}

	s.leasesMu.Lock()
	s.leases[lease.LeaseID] = lease
	s.leaseEnvelopes[env.EnvelopeID] = env
	s.leasesMu.Unlock()
	writeJSON(w, http.StatusOK, lease)
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
	if err := envelope.Validate(env); err != nil {
		writeError(w, http.StatusBadRequest, "validate: "+err.Error())
		return
	}
	if env.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, envelope.ErrMeshMismatch.Error())
		return
	}

	// S1: Verify mTLS client certificate identity matches emitter agent ID.
	if err := s.verifyMTLSIdentity(r, env.EmitterAgentID); err != nil {
		writeError(w, http.StatusForbidden, "mTLS identity mismatch: "+err.Error())
		return
	}

	// S2: Verify emitter's signature before executing settlement.
	if err := s.verifyEnvelope(env); err != nil {
		writeError(w, http.StatusUnauthorized, "verify envelope: "+err.Error())
		return
	}

	// R2: reactive lease-expiration check. A settle whose lease already
	// expired aborts without executing and emits a SETTLEMENT_TIMEOUT receipt
	// (idempotent: an already-settled contract keeps its receipt, R1).
	if req.LeaseID != "" {
		if l, ok := s.GetLease(req.LeaseID); ok && l.EnvelopeID == env.EnvelopeID && !l.ExpiresAt.After(time.Now()) {
			timeoutRec, emitErr := s.emitSettlementTimeout(env)
			if emitErr != nil {
				log.Printf("[%s] emit SETTLEMENT_TIMEOUT for expired lease %s: %v", s.config.AgentID, req.LeaseID, emitErr)
				writeError(w, http.StatusInternalServerError, "emit timeout receipt: "+emitErr.Error())
				return
			}
			receiptJSON, err := json.Marshal(timeoutRec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			log.Printf("[%s] lease %s expired: SETTLEMENT_TIMEOUT receipt=%s",
				s.config.AgentID, req.LeaseID, timeoutRec.ReceiptID)
			writeJSON(w, http.StatusOK, &SettleResponse{ReceiptJSON: receiptJSON, ReceiptID: timeoutRec.ReceiptID})
			return
		}
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

// handleAccept handles POST /accept — the emitter (A) accepts a settled receipt.
// The executor (B) applies the acceptance to the receipt in its chain store.
//
// Flow: A calls AcceptReceipt locally → sends emitter_signature to B →
// B verifies executor sig → applies acceptance fields → stores.
func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req AcceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if len(req.ReceiptJSON) == 0 {
		writeError(w, http.StatusBadRequest, "receipt_json required")
		return
	}
	if req.ExecutorSignedAtRFC == "" {
		writeError(w, http.StatusBadRequest, "executor_signed_at_rfc required")
		return
	}
	if req.EmitterSignature == "" {
		writeError(w, http.StatusBadRequest, "emitter_signature required")
		return
	}

	// Parse the receipt.
	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(req.ReceiptJSON, &rec); err != nil {
		writeError(w, http.StatusBadRequest, "parse receipt: "+err.Error())
		return
	}
	if rec.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	// Validate the executor signed-at field is present.
	if req.ExecutorSignedAtRFC == "" {
		writeError(w, http.StatusBadRequest, "executor_signed_at_rfc required")
		return
	}

	// Get the receipt from the chain store.
	ctx := context.Background()
	stored, err := s.chainStore.GetReceipt(ctx, rec.ReceiptID)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, "receipt not found: "+rec.ReceiptID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stored.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	// S1: Verify mTLS client certificate identity matches emitter agent ID.
	if err := s.verifyMTLSIdentity(r, stored.EmitterAgentID); err != nil {
		writeError(w, http.StatusForbidden, "mTLS identity mismatch: "+err.Error())
		return
	}

	// S2: Verify the executor's Ed25519 signature before persisting acceptance.
	//
	// Approach: verify against 'stored' (B's DB record), not 'rec' (A's JSON).
	// Reason: 'stored' is B's authoritative record — its ExecutorSignedAt and
	// ExecutorSignature are exactly what B signed. Using A's receipt risks
	// hash mismatch if A's JSON parse/serialize lost precision or content.
	// This is safe because the receipt_id in 'rec' (from A) matches 'stored'.
	if err := receipt.VerifyExecutorSignature(stored, s.Signer().PublicKey()); err != nil {
		writeError(w, http.StatusUnauthorized, "executor signature invalid: "+err.Error())
		return
	}

	// S2: Verify emitter's signature on the receipt before persisting acceptance.
	if err := s.verifyEmitterReceiptSignature(stored, req.EmitterSignature, receipt.AcceptanceAccepted, ""); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// Apply the acceptance fields. A already computed EmitterSignature locally
	// using AcceptReceipt; we just store the acceptance on B's chain.
	now := time.Now().UTC()
	stored.EmitterAcceptance = receipt.AcceptanceAccepted
	stored.DisputeReason = ""
	stored.EmitterAcceptanceAt = &now
	stored.EmitterSignature = req.EmitterSignature

	// Update in chain store.
	if err := s.chainStore.UpdateReceipt(ctx, stored); err != nil {
		writeError(w, http.StatusInternalServerError, "update: "+err.Error())
		return
	}

	// Return the updated receipt.
	updatedJSON, err := json.Marshal(stored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	}
	log.Printf("[%s] Accept: receipt=%s emitter_acceptance=%s",
		s.config.AgentID, stored.ReceiptID, stored.EmitterAcceptance)

	writeJSON(w, http.StatusOK, &AcceptResponse{
		ReceiptJSON: updatedJSON,
		ReceiptID:   stored.ReceiptID,
		Accepted:    true,
	})
}

// handleDispute handles POST /dispute — the emitter (A) formally disputes a settled receipt.
// The executor (B) applies the dispute to the receipt in its chain store.
//
// Flow: A detects anomaly → calls DisputeReceipt locally → sends emitter_signature to B →
// B verifies executor sig → applies dispute fields → stores on chain.
func (s *Server) handleDispute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req DisputeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if len(req.ReceiptJSON) == 0 {
		writeError(w, http.StatusBadRequest, "receipt_json required")
		return
	}
	if req.ExecutorSignedAtRFC == "" {
		writeError(w, http.StatusBadRequest, "executor_signed_at_rfc required")
		return
	}
	if req.EmitterSignature == "" {
		writeError(w, http.StatusBadRequest, "emitter_signature required")
		return
	}
	if req.DisputeReason == "" {
		writeError(w, http.StatusBadRequest, "dispute_reason required")
		return
	}

	// Parse the receipt.
	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(req.ReceiptJSON, &rec); err != nil {
		writeError(w, http.StatusBadRequest, "parse receipt: "+err.Error())
		return
	}
	if rec.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	// Get the receipt from the chain store.
	ctx := context.Background()
	stored, err := s.chainStore.GetReceipt(ctx, rec.ReceiptID)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, "receipt not found: "+rec.ReceiptID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stored.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	// S1: Verify mTLS client certificate identity matches emitter agent ID.
	if err := s.verifyMTLSIdentity(r, stored.EmitterAgentID); err != nil {
		writeError(w, http.StatusForbidden, "mTLS identity mismatch: "+err.Error())
		return
	}

	// Verify the receipt is not already accepted or disputed.
	if stored.EmitterAcceptance == receipt.AcceptanceAccepted {
		writeError(w, http.StatusConflict, "receipt already accepted")
		return
	}
	if stored.EmitterAcceptance == receipt.AcceptanceDisputed {
		writeError(w, http.StatusConflict, "receipt already disputed")
		return
	}

	// S2: Verify against 'stored' (DB record). Using 'rec' (A's JSON) risks
	// hash mismatch if A's JSON parse/serialize lost precision or content.
	if err := receipt.VerifyExecutorSignature(stored, s.Signer().PublicKey()); err != nil {
		writeError(w, http.StatusUnauthorized, "executor signature invalid: "+err.Error())
		return
	}

	// S2: Verify emitter's signature on the receipt before persisting dispute.
	if err := s.verifyEmitterReceiptSignature(stored, req.EmitterSignature, receipt.AcceptanceDisputed, req.DisputeReason); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// Apply the dispute fields. A already computed EmitterSignature locally
	// using DisputeReceipt; we just store the dispute on B's chain.
	now := time.Now().UTC()
	stored.EmitterAcceptance = receipt.AcceptanceDisputed
	stored.EmitterAcceptanceAt = &now
	stored.EmitterSignature = req.EmitterSignature
	stored.DisputeReason = req.DisputeReason

	// Update in chain store.
	if err := s.chainStore.UpdateReceipt(ctx, stored); err != nil {
		writeError(w, http.StatusInternalServerError, "update: "+err.Error())
		return
	}

	// Return the updated receipt.
	updatedJSON, err := json.Marshal(stored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	}
	log.Printf("[%s] Dispute: receipt=%s reason=%s",
		s.config.AgentID, stored.ReceiptID, stored.DisputeReason)

	writeJSON(w, http.StatusOK, &DisputeResponse{
		ReceiptJSON: updatedJSON,
		ReceiptID:   stored.ReceiptID,
		Disputed:    true,
	})
}

// handleVerifyChain handles POST /verify-chain — verifies the full receipt chain
// using ChainStore.VerifyChain with the provided public keys.
func (s *Server) handleVerifyChain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req VerifyChainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if req.EmitterID == "" || req.ExecutorID == "" {
		writeError(w, http.StatusBadRequest, "emitter_id and executor_id required")
		return
	}

	executorKey, err := hex.DecodeString(req.ExecutorKey)
	if err != nil || len(executorKey) != 32 {
		writeError(w, http.StatusBadRequest, "invalid executor_key")
		return
	}
	emitterKey, err := hex.DecodeString(req.EmitterKey)
	if err != nil || len(emitterKey) != 32 {
		writeError(w, http.StatusBadRequest, "invalid emitter_key")
		return
	}

	ctx := context.Background()
	chainResults, err := s.chainStore.VerifyChain(ctx, req.EmitterID, req.ExecutorID, executorKey, emitterKey)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, "chain not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	results := make([]VerifyChainResult, len(chainResults))
	allValid := true
	for i, cr := range chainResults {
		results[i] = VerifyChainResult{
			ReceiptID:         cr.ReceiptID,
			ExecutorSigValid:  cr.ExecutorSignatureValid,
			EmitterSigValid:   cr.EmitterSignatureValid,
			PreviousHashValid: cr.PreviousHashValid,
			Error:             cr.Error,
		}
		if !cr.ExecutorSignatureValid || !cr.PreviousHashValid {
			allValid = false
		}
	}

	writeJSON(w, http.StatusOK, &VerifyChainResponse{
		Results:  results,
		AllValid: allValid,
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
	if rec.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	recJSON, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	}
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
	if rec.MeshID != s.config.MeshID {
		writeError(w, http.StatusBadRequest, receipt.ErrMeshMismatch.Error())
		return
	}

	resp := &VerifyResponse{Errors: []string{}, ChainErrors: []string{}}

	// 1. Verify executor signature.
	hash, err := receipt.ComputeReceiptHash(rec)
	if err != nil {
		resp.Errors = append(resp.Errors, "compute hash: "+err.Error())
	} else {
		err := signing.Verify(s.Signer().PublicKey(), []byte(hash), rec.ExecutorSignature)
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
			Type:              string(p.Type),
			Passed:            false,
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
				parts, err := settlement.ValidatePreconditionCommand(cmd)
				if err != nil {
					result.Passed = false
					result.Message = fmt.Sprintf("command %q rejected: %v", cmd, err)
				} else {
					exitCode := runCheckCommand(parts, s.config.WorkspaceDir)
					result.Passed = exitCode == 0
					result.Message = fmt.Sprintf("command %q exit=%d", cmd, exitCode)
				}
			} else {
				result.Message = "command param missing"
			}

		case envelope.PreconditionGitCleanWorktree:
			ctxGit, cancelGit := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelGit()
			cmd := exec.CommandContext(ctxGit, "git", "-C", s.config.WorkspaceDir, "status", "--porcelain")
			out, err := cmd.Output()
			result.Passed = (err == nil && strings.TrimSpace(string(out)) == "")
			result.Message = fmt.Sprintf("git worktree clean=%v", result.Passed)

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

// checkToolAvailable checks if a binary is in PATH using exec.LookPath (RFC-002 S6).
func checkToolAvailable(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// runCheckCommand executes a validated command directly with tokenized arguments (RFC-002 S6 / Issue #130).
// Direct execution without "sh -c" prevents shell metacharacter injection.
// Binaries and arguments are strictly validated by settlement.ValidatePreconditionCommand before execution.
func runCheckCommand(parts []string, dir string) int {
	if len(parts) == 0 {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
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
