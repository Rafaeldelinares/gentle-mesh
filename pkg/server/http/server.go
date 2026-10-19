package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/webhook"
)

// ServerConfig defines configuration parameters for the mesh HTTPS/HTTP coordinator server.
type ServerConfig struct {
	Addr                 string
	TasksDir             string
	DBPath               string
	Store                store.TaskStore
	HeartbeatTimeout     time.Duration
	TaskTTL              time.Duration
	BearerToken          string
	DispatchToken        string
	NodeToken            string
	Runner               runner.Runner
	Registry             *registry.Registry
	TaskManager          *task.TaskManager
	SSEHeartbeatInterval time.Duration
	PeerID               string
	ClusterName          string
	TerritoryManager     *federation.TerritoryManager
	TerritoryMode        protocol.TerritoryMode
	WorkspaceRoot        string
	// Host & CORS configuration
	CORSOrigins          []string
	AllowedHosts         []string
	InsecureNoAuth       bool
	LocalFallback        *bool
	// TLS configuration
	TLSEnabled     bool
	TLSCertFile    string
	TLSKeyFile     string
	MeshCA         *pki.MeshCA
	MeshCAPemFile  string
	// mTLS configuration
	RequireMTLS    bool // Require client certificates for all connections
	// Enrollment configuration
	TokenStore  store.TokenStore // For enrollment token validation
	CertStore   store.CertStore  // For node certificate tracking and revocation
	// Webhook configuration
	WebhookStore store.WebhookStore // For webhook notifications
	// Rate limiting configuration
	RateLimitRequests  int           // Requests per window (0 = disabled)
	RateLimitWindow   time.Duration // Time window
	RateLimitBurst    int           // Max burst size
}

// Server provides the HTTPS/HTTP REST and SSE coordinator daemon for gentle-mesh.
type Server struct {
	config           ServerConfig
	httpServer       *stdhttp.Server
	registry         *registry.Registry
	taskManager      *task.TaskManager
	runner           runner.Runner
	territoryManager *federation.TerritoryManager
	territoryMode    protocol.TerritoryMode
	scheduler        *TerritoryScheduler
	workspaceRoot    string
	startTime        time.Time
	meshCA           *pki.MeshCA
	tokenStore       store.TokenStore
	certStore        store.CertStore
	revocationCache  *revocationCache
	webhookStore     store.WebhookStore
}

// NewServer initializes a new Server with defaults for omitted configuration fields.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8080"
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 30 * time.Second
	}
	if cfg.TaskTTL <= 0 {
		cfg.TaskTTL = 24 * time.Hour
	}
	if cfg.SSEHeartbeatInterval <= 0 {
		cfg.SSEHeartbeatInterval = 15 * time.Second
	}
	if cfg.Registry == nil {
		cfg.Registry = registry.NewRegistry(cfg.HeartbeatTimeout)
	}
	if cfg.TaskManager == nil {
		if cfg.TasksDir == "" {
			cfg.TasksDir = filepath.Join(os.TempDir(), fmt.Sprintf("gentle-mesh-tasks-%d", time.Now().UnixNano()))
		}
		var opts []task.TaskManagerOption
		var s store.TaskStore
		if cfg.Store != nil {
			opts = append(opts, task.WithStore(cfg.Store))
		} else if cfg.DBPath != "none" {
			dbPath := cfg.DBPath
			if dbPath == "" {
				dbPath = filepath.Join(cfg.TasksDir, "gentle-mesh.db")
			}
			var err error
			s, err = store.NewSQLiteStore(dbPath)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize sqlite store: %w", err)
			}
			opts = append(opts, task.WithStore(s))
		}
		tm, err := task.NewTaskManager(cfg.TasksDir, cfg.TaskTTL, opts...)
		if err != nil {
			if s != nil {
				_ = s.Close()
			}
			return nil, fmt.Errorf("failed to initialize task manager: %w", err)
		}
		cfg.TaskManager = tm
	}
	if cfg.ClusterName == "" {
		cfg.ClusterName = "gentle-mesh"
	}
	if cfg.PeerID == "" {
		cfg.PeerID = cfg.ClusterName
	}
	if cfg.WorkspaceRoot == "" {
		cfg.WorkspaceRoot = "."
	}
	workspaceRoot, err := filepath.Abs(cfg.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	if cfg.TerritoryManager == nil {
		cfg.TerritoryManager = federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:        cfg.PeerID,
			ClusterName:   cfg.ClusterName,
			LocalSource:   cfg.TaskManager.ActiveTerritories,
			RunningSource: cfg.TaskManager.RunningTerritories,
		})
	}
	if cfg.Runner == nil {
		useLocal := true
		if cfg.LocalFallback != nil {
			useLocal = *cfg.LocalFallback
		}
		dispatchTok := cfg.DispatchToken
		if dispatchTok == "" {
			dispatchTok = cfg.BearerToken
		}
		if useLocal {
			localRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{})
			cfg.Runner = runner.NewMeshRunner(runner.MeshRunnerOptions{
				Selector:       cfg.Registry,
				FallbackRunner: localRunner,
				Token:          dispatchTok,
			})
			log.Printf("tasks without an eligible node run in a local SIMULATED runner and are reported completed with executed_locally")
		} else {
			cfg.Runner = runner.NewMeshRunner(runner.MeshRunnerOptions{
				Selector:       cfg.Registry,
				FallbackRunner: nil,
				Token:          dispatchTok,
			})
			log.Printf("the local fallback is OFF: tasks without an eligible node will fail with DISPATCH_FAILED")
		}
	}

	mode := cfg.TerritoryMode
	if !mode.Valid() {
		mode = protocol.TerritoryModeQueue
	}
	// Create webhook dispatcher if webhook store is configured
	var webhookDispatcher *webhook.Dispatcher
	if cfg.WebhookStore != nil {
		webhookDispatcher = webhook.NewDispatcher(cfg.WebhookStore)
	}

	scheduler := NewTerritoryScheduler(SchedulerConfig{
		Mode:              mode,
		TaskManager:       cfg.TaskManager,
		TerritoryManager:  cfg.TerritoryManager,
		Registry:          cfg.Registry,
		Runner:            cfg.Runner,
		WebhookDispatcher: webhookDispatcher,
	})

	revCache := newRevocationCache()
	if cfg.CertStore != nil {
		certs, err := cfg.CertStore.ListCerts(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to warm up certificate revocation cache: %w", err)
		}
		for _, c := range certs {
			if c.Revoked && c.Serial != "" {
				revCache.Add(c.Serial)
			}
		}
	}

	s := &Server{
		config:           cfg,
		registry:         cfg.Registry,
		taskManager:      cfg.TaskManager,
		runner:           cfg.Runner,
		territoryManager: cfg.TerritoryManager,
		territoryMode:    mode,
		scheduler:        scheduler,
		workspaceRoot:    workspaceRoot,
		startTime:        time.Now(),
		meshCA:           cfg.MeshCA,
		tokenStore:       cfg.TokenStore,
		certStore:        cfg.CertStore,
		revocationCache:  revCache,
		webhookStore:     cfg.WebhookStore,
	}

	s.httpServer = &stdhttp.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s, nil
}

// Registry returns the attached node registry.
func (s *Server) Registry() *registry.Registry {
	return s.registry
}

// TaskManager returns the attached task lifecycle and log manager.
func (s *Server) TaskManager() *task.TaskManager {
	return s.taskManager
}

// TerritoryManager returns the attached territory federation manager.
func (s *Server) TerritoryManager() *federation.TerritoryManager {
	return s.territoryManager
}

// Runner returns the configured task execution runner.
func (s *Server) Runner() runner.Runner {
	return s.runner
}

// Scheduler returns the territory scheduler coordinating queueing and dispatch.
func (s *Server) Scheduler() *TerritoryScheduler {
	return s.scheduler
}

// CertStore returns the attached certificate store.
func (s *Server) CertStore() store.CertStore {
	return s.certStore
}

// RevokeCert revokes a node certificate by node ID, updates the in-memory revocation cache,
// evicts the node from the active registry, and cancels active running tasks with NODE_REVOKED.
// Returns the number of cancelled running tasks and any error encountered.
func (s *Server) RevokeCert(ctx context.Context, nodeID string) error {
	_, err := s.RevokeCertWithCount(ctx, nodeID)
	return err
}

// RevokeCertWithCount revokes a node certificate and returns the count of cancelled running tasks.
func (s *Server) RevokeCertWithCount(ctx context.Context, nodeID string) (int, error) {
	if s.certStore == nil {
		return 0, errors.New("cert store is not configured")
	}
	rec, err := s.certStore.GetCert(ctx, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to get certificate for revocation: %w", err)
	}
	if rec == nil {
		return 0, errors.New("certificate not found")
	}
	if err := s.certStore.RevokeCert(ctx, nodeID); err != nil {
		return 0, err
	}
	if rec.Serial != "" && s.revocationCache != nil {
		s.revocationCache.Add(rec.Serial)
	}
	if s.registry != nil {
		_ = s.registry.DeregisterNode(nodeID)
	}
	cancelledTasks := 0
	if s.taskManager != nil {
		cancelledTasks = s.taskManager.FailTasksOfNode(nodeID, "NODE_REVOKED", fmt.Sprintf("node %s certificate has been revoked", nodeID))
	}
	return cancelledTasks, nil
}

// IsCertRevoked checks if a certificate is revoked by serial number against the in-memory blocklist.
// Absence in the in-memory blocklist indicates the certificate is not revoked, without querying SQLite.
func (s *Server) IsCertRevoked(ctx context.Context, serial string) (bool, error) {
	if s.revocationCache != nil {
		return s.revocationCache.IsRevoked(serial), nil
	}
	return false, nil
}

// revocationCache provides atomic in-memory lookups and updates of revoked certificate serials.
type revocationCache struct {
	mu      sync.RWMutex
	revoked map[string]struct{}
}

func newRevocationCache() *revocationCache {
	return &revocationCache{
		revoked: make(map[string]struct{}),
	}
}

func (c *revocationCache) IsRevoked(serial string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.revoked[serial]
	return ok
}

func (c *revocationCache) Add(serial string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked[serial] = struct{}{}
}

func (c *revocationCache) Remove(serial string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.revoked, serial)
}

// TerritoryMode returns the normalized territory conflict policy in effect.
func (s *Server) TerritoryMode() protocol.TerritoryMode {
	return s.territoryMode
}

// WorkspaceRoot returns the absolute base directory exposed for remote workspace exploration.
func (s *Server) WorkspaceRoot() string {
	return s.workspaceRoot
}

// Config returns a copy of the server configuration.
func (s *Server) Config() ServerConfig {
	return s.config
}

// Handler constructs and returns the HTTP handler with all endpoints and middleware applied.
func (s *Server) Handler() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	s.registerRoutes(mux)

	var h stdhttp.Handler = mux

	// Apply rate limiting if configured
	if s.config.RateLimitRequests > 0 {
		rateLimiter := NewRateLimiter(
			s.config.RateLimitRequests,
			s.config.RateLimitWindow,
			s.config.RateLimitBurst,
		)
		h = RateLimitMiddleware(rateLimiter)(h)
	}

	if s.config.BearerToken != "" || s.config.NodeToken != "" {
		h = AuthMiddlewareWithConfig(AuthConfig{
			AdminToken: s.config.BearerToken,
			NodeToken:  s.config.NodeToken,
		}, h)
	}
	if s.config.RequireMTLS {
		var checker CertRevocationChecker
		if s.certStore != nil {
			checker = s
		}
		h = MTLSMiddlewareWithRevocation(checker, h)
	}
	h = CORSMiddlewareWithOrigins(s.config.CORSOrigins, h)
	allowedHosts := defaultAllowedHosts(s.config.Addr, s.config.AllowedHosts...)
	h = HostMiddleware(allowedHosts, h)
	h = PanicRecoveryMiddleware(h)
	return h
}

// defaultAllowedHosts returns the default allowed hosts for Host validation:
// always localhost, 127.0.0.1, ::1; adds the concrete host when non-wildcard;
// adds all interface IPs when addr is wildcard ("", "0.0.0.0", "::");
// appends any additional allowed hosts.
func defaultAllowedHosts(addr string, extraAllowed ...string) []string {
	seen := make(map[string]bool)
	var result []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if stripped, _, err := net.SplitHostPort(h); err == nil {
			h = stripped
		}
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1]
		}
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			result = append(result, h)
		}
	}

	add("localhost")
	add("127.0.0.1")
	add("::1")

	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil {
					add(ip.String())
				}
			}
		}
	} else {
		add(host)
	}

	for _, e := range extraAllowed {
		add(e)
	}

	return result
}

// isLoopbackAddr reports whether the given address binds strictly to loopback interfaces.
// An address with no host (e.g. ":8443", ":8080") is NOT considered loopback.
func isLoopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateExposure ensures that running the coordinator on a non-loopback address
// without authentication is rejected unless explicitly allowed via InsecureNoAuth.
func (s *Server) validateExposure() error {
	if s.config.InsecureNoAuth {
		if !isLoopbackAddr(s.config.Addr) && s.config.BearerToken == "" && !s.config.RequireMTLS {
			log.Printf("[gentle-mesh] WARNING: server running on non-loopback address %s without authentication (-insecure-no-auth enabled)", s.config.Addr)
		}
		return nil
	}
	if isLoopbackAddr(s.config.Addr) {
		return nil
	}
	if s.config.BearerToken != "" {
		return nil
	}
	if s.config.RequireMTLS {
		return nil
	}

	return fmt.Errorf("cannot bind coordinator to non-loopback address %s without authentication: configure -token, -require-mtls, or pass -insecure-no-auth for local development", s.config.Addr)
}

// buildTLSConfig constructs the tls.Config for HTTPS and mTLS serving.
func (s *Server) buildTLSConfig() (*tls.Config, error) {
	if s.config.RequireMTLS && s.config.MeshCA == nil {
		return nil, errors.New("mTLS required but mesh CA is not configured")
	}

	baseConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.NoClientCert,
	}

	if !s.config.RequireMTLS {
		return baseConfig, nil
	}

	caPEM, err := pki.CertificateToPEM(s.config.MeshCA.Cert)
	if err != nil {
		return nil, fmt.Errorf("failed to encode mesh CA certificate to PEM: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("failed to append mesh CA certificate to client CA pool")
	}

	baseConfig.ClientAuth = tls.VerifyClientCertIfGiven
	baseConfig.ClientCAs = pool
	if s.certStore != nil {
		verifyChain := func(verifiedChains [][]*x509.Certificate, peerCerts []*x509.Certificate) error {
			var cert *x509.Certificate
			if len(verifiedChains) > 0 && len(verifiedChains[0]) > 0 {
				cert = verifiedChains[0][0]
			} else if len(peerCerts) > 0 {
				cert = peerCerts[0]
			}
			if cert == nil {
				return nil
			}
			serial := cert.SerialNumber.String()
			revoked, err := s.IsCertRevoked(context.Background(), serial)
			if err != nil {
				return fmt.Errorf("tls: failed to verify certificate revocation: %w", err)
			}
			if revoked {
				return errors.New("tls: certificate is revoked")
			}
			return nil
		}

		baseConfig.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return nil
			}
			var chains [][]*x509.Certificate
			if len(verifiedChains) > 0 {
				chains = verifiedChains
			} else if len(rawCerts) > 0 {
				cert, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return err
				}
				chains = [][]*x509.Certificate{{cert}}
			}
			return verifyChain(chains, nil)
		}

		// VerifyConnection validates both initial and resumed TLS sessions to prevent
		// session resumption from bypassing revocation checks (Gosec G123).
		baseConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyChain(cs.VerifiedChains, cs.PeerCertificates)
		}
	}
	return baseConfig, nil
}

// Start begins listening and serving HTTP or HTTPS requests on the configured address.
func (s *Server) Start() error {
	if err := s.validateExposure(); err != nil {
		return err
	}

	if s.httpServer == nil {
		s.httpServer = &stdhttp.Server{
			Addr:              s.config.Addr,
			Handler:           s.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}

	// Use HTTPS with TLS if enabled
	if s.config.TLSEnabled && s.config.TLSCertFile != "" && s.config.TLSKeyFile != "" {
		tlsConfig, err := s.buildTLSConfig()
		if err != nil {
			return err
		}
		s.httpServer.TLSConfig = tlsConfig

		if s.config.RequireMTLS {
			log.Printf("[gentle-mesh] mTLS required: coordinator verifying client certificates")
		}

		if err := s.httpServer.ListenAndServeTLS(s.config.TLSCertFile, s.config.TLSKeyFile); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			return err
		}
		return nil
	}

	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
		return err
	}
	return nil
}

// TLSEnabled returns true if TLS is configured.
func (s *Server) TLSEnabled() bool {
	return s.config.TLSEnabled
}

// MeshCAPemFile returns the path to the mesh CA PEM file for nodes to download.
func (s *Server) MeshCAPemFile() string {
	return s.config.MeshCAPemFile
}

// Shutdown gracefully stops the HTTP server and releases task manager resources.
func (s *Server) Shutdown(ctx context.Context) error {
	var httpErr error
	if s.httpServer != nil {
		httpErr = s.httpServer.Shutdown(ctx)
	}
	tmErr := s.taskManager.Close()
	if httpErr != nil && !errors.Is(httpErr, stdhttp.ErrServerClosed) {
		return httpErr
	}
	return tmErr
}

// FailTasksOfOfflineNodes finalizes as failed NODE_LOST every running task
// attributed to a node whose status is offline. The coordinator calls this
// after PruneStaleNodes so tasks of a node that silently went offline do not
// stay running forever (finding 29). Safe to call repeatedly: already-terminal
// tasks are skipped by FailTask.
func (s *Server) FailTasksOfOfflineNodes() int {
	if s.config.Registry == nil || s.taskManager == nil {
		return 0
	}
	finalized := 0
	for _, n := range s.config.Registry.ListNodes() {
		if n.Status == protocol.NodeStatusOffline && n.NodeID != "" {
			finalized += s.taskManager.FailTasksOfNode(n.NodeID, "NODE_LOST",
				"node went offline: no heartbeat within the timeout")
		}
	}
	return finalized
}
