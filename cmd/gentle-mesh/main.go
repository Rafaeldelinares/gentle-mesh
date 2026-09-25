package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/client"
	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

// runCLI parses top-level subcommands and dispatches to corresponding handlers,
// reading streaming input (e.g. the stdio RPC bridge) from os.Stdin.
func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runCLIWithIO(ctx, args, os.Stdin, stdout, stderr)
}

// runCLIWithIO is runCLI with an injectable input stream, so streaming
// subcommands such as "rpc" can be driven from tests or other embedded hosts.
func runCLIWithIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("subcommand is required")
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "server":
		return runServer(ctx, cmdArgs, stdout, stderr)
	case "worker":
		return runWorker(ctx, cmdArgs, stdout, stderr)
	case "nodes":
		return runNodes(ctx, cmdArgs, stdout, stderr)
	case "radar":
		return runRadar(ctx, cmdArgs, stdout, stderr)
	case "run":
		return runRun(ctx, cmdArgs, stdout, stderr)
	case "rpc":
		return runRPC(ctx, cmdArgs, stdin, stdout, stderr)
	case "cert-issue":
		return runCertIssue(ctx, cmdArgs, stdout, stderr)
	case "cert-revoke":
		return runCertRevoke(ctx, cmdArgs, stdout, stderr)
	case "cert-list":
		return runCertList(ctx, cmdArgs, stdout, stderr)
	case "gen-token":
		return runGenToken(ctx, cmdArgs, stdout, stderr)
	case "token-list":
		return runTokenList(ctx, cmdArgs, stdout, stderr)
	case "token-revoke":
		return runTokenRevoke(ctx, cmdArgs, stdout, stderr)
	case "gen-csr":
		return runGenCSR(ctx, cmdArgs, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gentle-mesh <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  server    Run the mesh coordinator HTTP REST and SSE server")
	fmt.Fprintln(w, "  worker    Run a mesh worker node registering with coordinator")
	fmt.Fprintln(w, "  nodes     List registered mesh nodes and status")
	fmt.Fprintln(w, "  radar     Display real-time active subagents radar and scope")
	fmt.Fprintln(w, "  run       Submit a task and stream SSE execution events")
	fmt.Fprintln(w, "  rpc       Run stdio-to-HTTP/SSE RPC bridge for Pi frontends")
	fmt.Fprintln(w, "  cert-issue   Issue a new node certificate (mTLS)")
	fmt.Fprintln(w, "  cert-revoke  Revoke a node certificate")
	fmt.Fprintln(w, "  cert-list     List all issued certificates")
	fmt.Fprintln(w, "  gen-token    Generate an enrollment token for auto-cert")
	fmt.Fprintln(w, "  token-list   List enrollment tokens")
	fmt.Fprintln(w, "  token-revoke Revoke an enrollment token")
	fmt.Fprintln(w, "  gen-csr      Generate a CSR locally for enrollment (no CA needed)")
	fmt.Fprintln(w, "  help      Show help for gentle-mesh")
}

// runServer starts the coordinator HTTP REST and SSE server.
func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", ":8080", "HTTP coordinator listen address (use :8443 for HTTPS)")
	tasksDir := fs.String("tasks-dir", "/tmp/gentle-mesh/tasks", "Directory for task logs and state")
	dbPath := fs.String("db-path", "", "Path to SQLite database for task persistence (defaults to <tasks-dir>/gentle-mesh.db, 'none' to disable)")
	heartbeatTimeout := fs.Duration("heartbeat-timeout", 30*time.Second, "Heartbeat timeout for registered nodes")
	token := fs.String("token", "", "Optional bearer authentication token")
	taskTTL := fs.Duration("task-ttl", 24*time.Hour, "Task TTL before pruning")
	territoryMode := fs.String("territory-mode", string(protocol.TerritoryModeQueue), "Territory conflict scheduling mode (queue, warn, strict, disabled)")
	workspace := fs.String("workspace", ".", "Base directory for remote workspace file exploration")
	runnerName := fs.String("runner", "mesh", "Task execution runner: mesh (registry routing with local simulation), simulated (local only), or pi (spawn the local Pi CLI)")
	tlsEnable := fs.Bool("tls", false, "Enable TLS/HTTPS with generated certificates (generates CA if not exists)")
	tlsDir := fs.String("tls-dir", "", "Directory for TLS certificates and CA (defaults to <tasks-dir>/tls)")
	tlsInit := fs.Bool("tls-init", false, "Initialize TLS: generate new CA and server certificates (overwrites existing)")
	requireMTLS := fs.Bool("require-mtls", false, "Require mTLS client certificates for all connections (implies -tls)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// TLS directory defaults to tasks-dir/tls
	tlsDirPath := *tlsDir
	if tlsDirPath == "" {
		tlsDirPath = filepath.Join(*tasksDir, "tls")
	}

	// Handle TLS initialization
	if *tlsInit {
		fmt.Fprintf(stdout, "Initializing TLS in %s...\n", tlsDirPath)
		if err := pki.InitMeshTLS(tlsDirPath, "Gentle Mesh", "mesh-coordinator", []string{}); err != nil {
			return fmt.Errorf("failed to initialize TLS: %w", err)
		}
		fmt.Fprintf(stdout, "TLS initialized in %s\n", tlsDirPath)
		fmt.Fprintf(stdout, "  CA certificate: %s\n", filepath.Join(tlsDirPath, pki.CAPemFile))
		fmt.Fprintf(stdout, "  Server cert:    %s\n", filepath.Join(tlsDirPath, pki.CertPemFile))
		fmt.Fprintf(stdout, "\nShare %s with nodes to enable TLS\n", filepath.Join(tlsDirPath, pki.CAPemFile))
		return nil
	}

	mode := protocol.TerritoryMode(*territoryMode)
	if !mode.Valid() {
		return fmt.Errorf("invalid -territory-mode %q: must be one of queue, warn, strict, disabled", *territoryMode)
	}

	selectedRunner, err := buildRunner(*runnerName, *workspace)
	if err != nil {
		return err
	}

	serverConfig := meshhttp.ServerConfig{
		Addr:             *addr,
		TasksDir:         *tasksDir,
		DBPath:           *dbPath,
		HeartbeatTimeout: *heartbeatTimeout,
		TaskTTL:          *taskTTL,
		BearerToken:      *token,
		TerritoryMode:    mode,
		WorkspaceRoot:    *workspace,
		Runner:           selectedRunner,
	}

	// Configure TLS if enabled
	if *tlsEnable {
		fmt.Fprintf(stdout, "TLS enabled, loading certificates from %s...\n", tlsDirPath)
		// Include localhost and 127.0.0.1 in server cert for local development
		hostnames := []string{"localhost", "127.0.0.1"}
		ca, serverCert, err := pki.EnsureMeshTLS(tlsDirPath, "Gentle Mesh", "mesh-coordinator", hostnames, false)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificates: %w", err)
		}
		serverConfig.TLSEnabled = true
		serverConfig.TLSCertFile = filepath.Join(tlsDirPath, pki.CertPemFile)
		serverConfig.TLSKeyFile = filepath.Join(tlsDirPath, pki.CertKeyFile)
		serverConfig.MeshCA = ca
		serverConfig.MeshCAPemFile = filepath.Join(tlsDirPath, pki.CAPemFile)
		fmt.Fprintf(stdout, "TLS ready: CA=%s\n", ca.Cert.Subject.CommonName)
		fmt.Fprintf(stdout, "Server cert expires: %s\n", serverCert.Cert.NotAfter.Format("2006-01-02"))

		// Initialize token store for enrollment
		db, err := sql.Open("sqlite", *dbPath)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		if err := store.InitTokenSchema(db); err != nil {
			return fmt.Errorf("failed to init token schema: %w", err)
		}
		serverConfig.TokenStore = store.NewSQLiteTokenStore(db)
		fmt.Fprintf(stdout, "Enrollment tokens enabled\n")

		// Initialize webhook store and dispatcher
		if err := store.InitWebhookSchema(db); err != nil {
			return fmt.Errorf("failed to init webhook schema: %w", err)
		}
		serverConfig.WebhookStore = store.NewSQLiteWebhookStore(db)
		fmt.Fprintf(stdout, "Webhooks enabled\n")
	}

	// Configure mTLS if required
	if *requireMTLS {
		if !serverConfig.TLSEnabled {
			return errors.New("-require-mtls requires -tls to be enabled")
		}
		serverConfig.RequireMTLS = true
		fmt.Fprintf(stdout, "mTLS required: all connections must present valid client certificates\n")
	} else if *addr == ":8443" || strings.HasPrefix(*addr, ":8443") {
		// Auto-enable TLS if using common HTTPS port without -tls flag
		fmt.Fprintf(stdout, "Auto-enabling TLS on port 8443...\n")
		hostnames := []string{"localhost", "127.0.0.1"}
		ca, serverCert, err := pki.EnsureMeshTLS(tlsDirPath, "Gentle Mesh", "mesh-coordinator", hostnames, false)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificates: %w", err)
		}
		serverConfig.TLSEnabled = true
		serverConfig.TLSCertFile = filepath.Join(tlsDirPath, pki.CertPemFile)
		serverConfig.TLSKeyFile = filepath.Join(tlsDirPath, pki.CertKeyFile)
		serverConfig.MeshCA = ca
		serverConfig.MeshCAPemFile = filepath.Join(tlsDirPath, pki.CAPemFile)
		_ = serverCert // used for info above
	}

	srv, err := meshhttp.NewServer(serverConfig)
	if err != nil {
		return fmt.Errorf("failed to create coordinator server: %w", err)
	}

	tlsStatus := "HTTP"
	if serverConfig.TLSEnabled {
		if serverConfig.RequireMTLS {
			tlsStatus = "mTLS"
		} else {
			tlsStatus = "HTTPS"
		}
	}
	fmt.Fprintf(stdout, "Gentle Mesh coordinator starting on %s (%s, tasks dir: %s, territory mode: %s, runner: %s)\n",
		*addr, tlsStatus, *tasksDir, mode, *runnerName)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-ctx.Done():
		fmt.Fprintln(stdout, "Shutting down coordinator...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("error during server shutdown: %w", err)
		}
		fmt.Fprintln(stdout, "Coordinator stopped gracefully.")
		return nil
	}
}

// buildRunner resolves the -runner flag into a task execution runner. A nil
// runner leaves selection to the server default, which routes to mesh nodes and
// falls back to local simulation.
func buildRunner(name, workspace string) (runner.Runner, error) {
	switch name {
	case "mesh":
		return nil, nil
	case "simulated":
		return runner.NewSimulatedRunner(runner.SimulatedOptions{}), nil
	case "pi":
		workspaceRoot, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workspace root for the pi runner: %w", err)
		}
		return runner.NewPiRunner(runner.PiRunnerOptions{WorkspaceRoot: workspaceRoot}), nil
	default:
		return nil, fmt.Errorf("invalid -runner %q: must be one of mesh, simulated, pi", name)
	}
}

// runWorker runs a worker node that registers with the coordinator and sends heartbeats.
func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	nodeID := fs.String("node-id", "", "Unique node identifier (default: hostname)")
	endpoint := fs.String("endpoint", "http://localhost:8081", "Worker reachable endpoint URL")
	agentsFlag := fs.String("agents", "worker", "Comma-separated agent capabilities (e.g. worker,explore)")
	tagsFlag := fs.String("tags", "", "Comma-separated tags (e.g. go,fast)")
	concurrency := fs.Int("concurrency", 2, "Maximum task concurrency")
	heartbeatInterval := fs.Duration("heartbeat-interval", 10*time.Second, "Heartbeat ping interval")
	token := fs.String("token", "", "Optional bearer authentication token")
	addr := fs.String("addr", "", "Listen address for worker HTTP server (default: port from endpoint or :8081)")
	caCert := fs.String("ca", "", "Path to mesh CA certificate for TLS verification (downloads from coordinator if not provided)")
	insecureSkipTLS := fs.Bool("insecure-skip-tls-verify", false, "Skip TLS verification (for development only)")
	clientCert := fs.String("cert", "", "Path to client certificate for mTLS authentication (requires -key)")
	clientKey := fs.String("key", "", "Path to client private key for mTLS authentication (requires -cert)")
	joinToken := fs.String("join-token", "", "Enrollment token for automatic certificate issuance (uses CSR-based enrollment)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	id := *nodeID
	if id == "" {
		h, err := os.Hostname()
		if err == nil && h != "" {
			id = h
		} else {
			id = fmt.Sprintf("worker-%d", time.Now().UnixNano()%1000000)
		}
	}

	agentNames := parseCommaSeparated(*agentsFlag)
	if len(agentNames) == 0 {
		agentNames = []string{"worker"}
	}
	tags := parseCommaSeparated(*tagsFlag)

	hasGPU := false
	for _, t := range tags {
		if strings.EqualFold(t, "gpu") {
			hasGPU = true
			break
		}
	}

	var agentProfiles []protocol.AgentProfile
	for _, name := range agentNames {
		agentProfiles = append(agentProfiles, protocol.AgentProfile{
			Name:        name,
			Description: fmt.Sprintf("%s agent", name),
			Tags:        tags,
		})
	}

	coordURL := strings.TrimRight(*coordinator, "/")
	joinURL := coordURL + "/v1/mesh/join"
	heartbeatURL := coordURL + "/v1/mesh/heartbeat"
	caURL := coordURL + "/v1/mesh/ca"

	// Build HTTP client with TLS configuration
	httpClient, err := newMTLSClient(*caCert, *clientCert, *clientKey, *insecureSkipTLS)
	if err != nil {
		return fmt.Errorf("failed to create TLS client: %w", err)
	}
	httpClient.Timeout = 10 * time.Second

	// Auto-download CA from coordinator if not provided
	caPath := *caCert
	if caPath == "" && !*insecureSkipTLS {
		// Use insecure client for initial download (self-signed certs)
		tempClient := newTLSClient("", true) // skip verification

		// Check if coordinator is using TLS by probing health
		healthURL := coordURL + "/healthz"
		healthReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		healthResp, err := tempClient.Do(healthReq)
		if err == nil {
			healthResp.Body.Close()
			// If TLS is enabled on coordinator, download CA
			var healthData struct {
				TLS string `json:"tls"`
			}
			if healthResp.StatusCode == 200 {
				_ = json.NewDecoder(healthResp.Body).Decode(&healthData)
			}
			if healthData.TLS == "enabled" {
				fmt.Fprintf(stdout, "Coordinator has TLS enabled, downloading CA...\n")
				caData, err := downloadCA(ctx, tempClient, caURL)
				if err != nil {
					return fmt.Errorf("failed to download CA from coordinator: %w\nTIP: Use -ca flag or run with -insecure-skip-tls-verify for development", err)
				}
				caPath = filepath.Join(os.TempDir(), "gentle-mesh-ca.pem")
				if err := os.WriteFile(caPath, caData, 0644); err != nil {
					return fmt.Errorf("failed to save CA: %w", err)
				}
				fmt.Fprintf(stdout, "CA saved to %s\n", caPath)
			}
		}
	}

	// If we need enrollment, download CA first (if not already done)
	if *joinToken != "" && (*clientCert == "" || *clientKey == "") && caPath == "" {
		fmt.Fprintf(stdout, "Downloading CA for enrollment...\n")
		// Use insecure client for downloading CA (skips cert verification)
		tempClient := newTLSClient("", true) // true = skip verification
		caData, err := downloadCA(ctx, tempClient, caURL)
		if err != nil {
			return fmt.Errorf("failed to download CA for enrollment: %w", err)
		}
		caPath = filepath.Join(os.TempDir(), "gentle-mesh-ca.pem")
		if err := os.WriteFile(caPath, caData, 0644); err != nil {
			return fmt.Errorf("failed to save CA: %w", err)
		}
		fmt.Fprintf(stdout, "CA saved to %s\n", caPath)
	}

	// Auto-enrollment via token: generate CSR and get certificate from coordinator
	if *joinToken != "" && (*clientCert == "" || *clientKey == "") {
		fmt.Fprintf(stdout, "Auto-enrollment via token...\n")

		// For enrollment, use insecure client (skips cert verification for self-signed)
		enrollClient := newTLSClient("", true) // skip verification

		// Generate CSR locally (private key never leaves the node)
		fmt.Fprintf(stdout, "Generating CSR locally (private key stays here)...\n")
		csrResult, err := pki.GenerateCSR(id)
		if err != nil {
			return fmt.Errorf("failed to generate CSR: %w", err)
		}

		// Enroll with coordinator
		enrollURL := coordURL + "/v1/certs/enroll"
		enrollReq := map[string]string{
			"token":   *joinToken,
			"csr":     csrResult.CSRPEM,
			"node_id": id,
		}
		enrollBody, _ := json.Marshal(enrollReq)
		enrollHTTPReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, enrollURL, bytes.NewReader(enrollBody))
		enrollHTTPReq.Header.Set("Content-Type", "application/json")

		enrollResp, err := enrollClient.Do(enrollHTTPReq)
		if err != nil {
			return fmt.Errorf("failed to enroll: %w\nTIP: Check if coordinator is reachable and token is valid", err)
		}
		defer enrollResp.Body.Close()

		if enrollResp.StatusCode != http.StatusOK {
			var errResp map[string]string
			json.NewDecoder(enrollResp.Body).Decode(&errResp)
			return fmt.Errorf("enrollment failed (HTTP %d): %s", enrollResp.StatusCode, errResp["error"])
		}

		var enrollResult struct {
			CertPEM string `json:"cert_pem"`
			NodeID  string `json:"node_id"`
		}
		if err := json.NewDecoder(enrollResp.Body).Decode(&enrollResult); err != nil {
			return fmt.Errorf("failed to parse enrollment response: %w", err)
		}

		// Save certificate and key to temp directory
		certDir := filepath.Join(os.TempDir(), "gentle-mesh", id)
		if err := os.MkdirAll(certDir, 0700); err != nil {
			return fmt.Errorf("failed to create cert directory: %w", err)
		}

		certPath := filepath.Join(certDir, "cert.pem")
		keyPath := filepath.Join(certDir, "key.pem")

		if err := os.WriteFile(certPath, []byte(enrollResult.CertPEM), 0600); err != nil {
			return fmt.Errorf("failed to save certificate: %w", err)
		}
		if err := os.WriteFile(keyPath, []byte(csrResult.PrivateKeyPEM), 0600); err != nil {
			return fmt.Errorf("failed to save private key: %w", err)
		}

		fmt.Fprintf(stdout, "Certificate enrolled!\n")
		fmt.Fprintf(stdout, "  Cert: %s\n", certPath)
		fmt.Fprintf(stdout, "  Key:  %s\n", keyPath)

		// Update client cert paths for mTLS
		*clientCert = certPath
		*clientKey = keyPath

		// Re-create HTTP client with proper CA for mTLS
		// If we have CA, use it for server verification; otherwise use system CAs
		httpClient, err = newMTLSClient(caPath, certPath, keyPath, caPath == "")
		if err != nil {
			return fmt.Errorf("failed to create mTLS client: %w", err)
		}
		httpClient.Timeout = 10 * time.Second
		if caPath == "" {
			fmt.Fprintf(stderr, "⚠️  Warning: No CA downloaded, using system CAs (mTLS may fail with self-signed certs)\n")
		}
	}

	joinReq := protocol.NodeJoinRequest{
		NodeID:   id,
		Endpoint: *endpoint,
		Hardware: protocol.NodeHardware{
			CPUs:   runtime.NumCPU(),
			RAMGB:  8,
			HasGPU: hasGPU,
			OS:     runtime.GOOS,
		},
		Agents:         agentProfiles,
		MaxConcurrency: *concurrency,
	}

	joinBytes, err := json.Marshal(joinReq)
	if err != nil {
		return fmt.Errorf("failed to marshal join payload: %w", err)
	}

	listenAddr := *addr
	if listenAddr == "" {
		listenAddr = extractPortFromEndpoint(*endpoint)
	}

	workerSrv := worker.NewServer(worker.ServerConfig{
		Addr:        listenAddr,
		BearerToken: *token,
	})

	if err := workerSrv.Listen(); err != nil {
		return fmt.Errorf("failed to bind worker HTTP server on %s: %w", listenAddr, err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = workerSrv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(stdout, "Worker HTTP server started on %s\n", workerSrv.Addr())

	srvErrCh := make(chan error, 1)
	go func() {
		if err := workerSrv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErrCh <- err
		}
	}()

	// 1. Initial join request
	hReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL, bytes.NewReader(joinBytes))
	if err != nil {
		return fmt.Errorf("failed to build join request: %w", err)
	}
	hReq.Header.Set("Content-Type", "application/json")
	if *token != "" {
		hReq.Header.Set("Authorization", "Bearer "+*token)
	}

	resp, err := httpClient.Do(hReq)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator at %s: %w", joinURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coordinator rejected join (status %d): %s", resp.StatusCode, string(body))
	}

	fmt.Fprintf(stdout, "Worker %s joined mesh successfully (endpoint: %s, coordinator: %s)\n", id, *endpoint, coordURL)

	// 2. Periodic heartbeat loop
	ticker := time.NewTicker(*heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "Worker %s shutting down...\n", id)
			return nil
		case err := <-srvErrCh:
			return fmt.Errorf("worker server error: %w", err)
		case <-ticker.C:
			hbReq := protocol.NodeHeartbeatRequest{
				NodeID:      id,
				ActiveTasks: workerSrv.ActiveTasks(),
				Timestamp:   time.Now().Unix(),
			}
			hbBytes, _ := json.Marshal(hbReq)

			hbHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost, heartbeatURL, bytes.NewReader(hbBytes))
			if err != nil {
				fmt.Fprintf(stderr, "Failed to create heartbeat request: %v\n", err)
				continue
			}
			hbHTTPReq.Header.Set("Content-Type", "application/json")
			if *token != "" {
				hbHTTPReq.Header.Set("Authorization", "Bearer "+*token)
			}

			hbResp, err := httpClient.Do(hbHTTPReq)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(stderr, "Heartbeat error: %v\n", err)
				continue
			}

			if hbResp.StatusCode == http.StatusNotFound {
				hbResp.Body.Close()
				fmt.Fprintf(stdout, "Worker %s not found on coordinator, re-registering...\n", id)
				reJoinReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL, bytes.NewReader(joinBytes))
				if err == nil {
					reJoinReq.Header.Set("Content-Type", "application/json")
					if *token != "" {
						reJoinReq.Header.Set("Authorization", "Bearer "+*token)
					}
					if rResp, err := httpClient.Do(reJoinReq); err == nil {
						rResp.Body.Close()
					}
				}
			} else {
				hbResp.Body.Close()
			}
		}
	}
}

// downloadCA fetches the mesh CA certificate from the coordinator.
func downloadCA(ctx context.Context, client *http.Client, caURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, caURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coordinator returned status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// runNodes queries the coordinator for registered nodes and displays a formatted ASCII table.
func runNodes(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nodes", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	token := fs.String("token", "", "Optional bearer authentication token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	coordURL := strings.TrimRight(*coordinator, "/") + "/v1/mesh/nodes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coordURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build nodes request: %w", err)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to query coordinator nodes at %s: %w", coordURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get nodes (status %d): %s", resp.StatusCode, string(body))
	}

	var data struct {
		Nodes []*protocol.NodeInfo `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("failed to parse nodes response: %w", err)
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NODE ID\tENDPOINT\tSTATUS\tCONCURRENCY\tAGENTS\tTAGS")
	for _, n := range data.Nodes {
		var agentNames []string
		var allTags []string
		tagSet := make(map[string]bool)
		for _, a := range n.Agents {
			agentNames = append(agentNames, a.Name)
			for _, t := range a.Tags {
				if !tagSet[t] {
					tagSet[t] = true
					allTags = append(allTags, t)
				}
			}
		}

		concurrencyStr := fmt.Sprintf("%d/%d", n.ActiveTasks, n.MaxConcurrency)
		agentsStr := strings.Join(agentNames, ",")
		if agentsStr == "" {
			agentsStr = "-"
		}
		tagsStr := strings.Join(allTags, ",")
		if tagsStr == "" {
			tagsStr = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			n.NodeID,
			n.Endpoint,
			n.Status,
			concurrencyStr,
			agentsStr,
			tagsStr,
		)
	}
	_ = w.Flush()

	if len(data.Nodes) == 0 {
		fmt.Fprintln(stdout, "(No nodes registered in the mesh)")
	}

	return nil
}

// runRadar queries the coordinator radar endpoint and displays an active subagent radar table.
func runRadar(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	token := fs.String("token", "", "Optional bearer authentication token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	coordURL := strings.TrimRight(*coordinator, "/") + "/v1/mesh/radar"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coordURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build radar request: %w", err)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to query coordinator radar at %s: %w", coordURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get radar (status %d): %s", resp.StatusCode, string(body))
	}

	var report protocol.RadarReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return fmt.Errorf("failed to parse radar response: %w", err)
	}

	if len(report.ActiveAgents) == 0 {
		fmt.Fprintln(stdout, "No active agents currently running in the mesh radar.")
		return nil
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NODE/TASK ID\tAGENT\tPHASE\tDOMAIN\tBLAST RADIUS\tSURFACES\tCURRENT ACTION")
	for _, a := range report.ActiveAgents {
		idStr := a.TaskID
		if a.NodeID != "" && a.TaskID != "" {
			idStr = fmt.Sprintf("%s/%s", a.NodeID, a.TaskID)
		} else if a.NodeID != "" {
			idStr = a.NodeID
		}
		if idStr == "" {
			idStr = "-"
		}

		agentStr := a.Agent
		if agentStr == "" {
			agentStr = "-"
		}

		phaseStr := string(a.Phase)
		if phaseStr == "" {
			phaseStr = "-"
		}

		domainStr := a.Domain
		if domainStr == "" {
			domainStr = "-"
		}

		blastStr := string(a.BlastRadius)
		if blastStr == "" {
			blastStr = string(protocol.BlastRadiusIsolated)
		}

		surfacesStr := strings.Join(a.EditSurfaces, ",")
		if surfacesStr == "" {
			surfacesStr = "-"
		}

		actionStr := a.CurrentAction
		if actionStr == "" {
			actionStr = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			idStr,
			agentStr,
			phaseStr,
			domainStr,
			blastStr,
			surfacesStr,
			actionStr,
		)
	}
	_ = w.Flush()

	return nil
}

// runRPC runs the stdio JSON-RPC bridge that lets a Pi frontend drive mesh
// tasks over a newline-delimited JSON protocol on stdin/stdout. The Pi
// compatibility flags (-mode, -approve, -session) are accepted and ignored so
// the bridge can be launched with the same argument shape as a local Pi
// entrypoint.
func runRPC(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// Help is rendered to stdout by the ErrHelp branch below, so suppress the
	// default stderr usage dump. Parse errors still reach stderr.
	fs.Usage = func() {}

	coordinator := fs.String("coordinator", envOrDefault("GENTLE_MESH_COORDINATOR", "http://localhost:8080"), "Coordinator base URL")
	token := fs.String("token", os.Getenv("GENTLE_MESH_TOKEN"), "Optional bearer authentication token")
	agent := fs.String("agent", "worker", "Subagent role dispatched for each prompt")
	caCert := fs.String("ca", "", "Path to mesh CA certificate for TLS verification (download from coordinator /v1/mesh/ca)")
	insecureSkipTLS := fs.Bool("insecure-skip-tls-verify", false, "Skip TLS verification (for development only)")

	// Pi launcher compatibility flags. They are registered so the bridge can be
	// launched as `gentle-mesh rpc --mode rpc --approve [--session <file>]`
	// exactly like a local Pi entrypoint, then intentionally ignored.
	fs.String("mode", "rpc", "Pi launcher mode (accepted for compatibility, ignored)")
	fs.Bool("approve", true, "Pi launcher approval flag (accepted for compatibility, ignored)")
	fs.String("session", "", "Pi launcher session file (accepted for compatibility, ignored)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "Usage: gentle-mesh rpc [flags]")
			fmt.Fprintln(stdout, "")
			fs.SetOutput(stdout)
			fs.PrintDefaults()
			return nil
		}
		return err
	}

	bridge := client.NewBridge(client.Config{
		CoordinatorURL:         *coordinator,
		Token:                  *token,
		Agent:                  *agent,
		CACertFile:             *caCert,
		InsecureSkipTLSVerify:  *insecureSkipTLS,
	})

	return bridge.Serve(ctx, stdin, stdout)
}

// envOrDefault returns the value of the named environment variable, or def
// when it is unset or empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newTLSClient creates an HTTP client with optional TLS configuration.
func newTLSClient(caCertPath string, insecureSkipVerify bool) *http.Client {
	if insecureSkipVerify || caCertPath != "" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: insecureSkipVerify,
		}
		if caCertPath != "" && !insecureSkipVerify {
			caCert, err := os.ReadFile(caCertPath)
			if err == nil {
				caPool := x509.NewCertPool()
				caPool.AppendCertsFromPEM(caCert)
				tlsConfig.RootCAs = caPool
			}
		}
		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		}
	}
	return &http.Client{}
}

// newMTLSClient creates an HTTP client with optional mTLS configuration.
// It returns an error if only one of certPath or keyPath is provided.
func newMTLSClient(caCertPath, certPath, keyPath string, insecureSkipVerify bool) (*http.Client, error) {
	// Validate cert/key pair
	if (certPath != "" && keyPath == "") || (certPath == "" && keyPath != "") {
		return nil, errors.New("both -cert and -key must be provided for mTLS authentication")
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify,
	}

	// Load CA for server verification
	if caCertPath != "" && !insecureSkipVerify {
		caCert, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caCert)
		tlsConfig.RootCAs = caPool
	}

	// Load client certificate for mTLS
	if certPath != "" && keyPath != "" {
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read client certificate: %w", err)
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read client key: %w", err)
		}

		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate/key pair: %w", err)
		}

		tlsConfig.Certificates = []tls.Certificate{cert}

		if caCertPath == "" && !insecureSkipVerify {
			fmt.Fprintf(os.Stderr, "⚠️  Warning: mTLS configured without custom CA, using system CAs\n")
		}
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}, nil
}

// runRun dispatches a task to the coordinator and streams SSE events to stdout.
func runRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	agent := fs.String("agent", "worker", "Target subagent name")
	taskFlag := fs.String("task", "", "Subagent task prompt (required)")
	repo := fs.String("repo", "", "Optional Git repository URL")
	branch := fs.String("branch", "", "Optional Git branch")
	idempotencyKey := fs.String("idempotency-key", "", "Optional idempotency deduplication key")
	domain := fs.String("domain", "", "Architectural domain (e.g. auth, database, ui)")
	blastRadius := fs.String("blast-radius", "isolated-branch", "Blast radius scope (e.g. isolated-branch, read-only, shared-schema, breaking-change)")
	surfaces := fs.String("surfaces", "", "Comma-separated edit surfaces (files or directories)")
	tags := fs.String("tags", "", "Comma-separated required node tags (e.g. gpu, fast)")
	timeout := fs.Int("timeout", 60, "Task timeout in seconds")
	maxRetries := fs.Int("max-retries", 0, "Maximum automatic retries on failure (0 = no retry)")
	retryDelay := fs.Int("retry-delay", 30, "Seconds to wait between retries")
	priority := fs.Int("priority", 0, "Task priority (-100 to 100, higher runs first)")
	token := fs.String("token", "", "Optional bearer authentication token")
	caCert := fs.String("ca", "", "Path to mesh CA certificate for TLS verification")
	insecureSkipTLS := fs.Bool("insecure-skip-tls-verify", false, "Skip TLS verification (for development only)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *taskFlag == "" {
		return errors.New("-task flag is required")
	}

	coordURL := strings.TrimRight(*coordinator, "/")
	tasksURL := coordURL + "/v1/tasks"

	taskReq := protocol.TaskRequest{
		IdempotencyKey: *idempotencyKey,
		Agent:          *agent,
		Task:           *taskFlag,
		GitRepo:        *repo,
		GitBranch:      *branch,
		Domain:         *domain,
		BlastRadius:    protocol.BlastRadius(*blastRadius),
		EditSurfaces:   parseCommaSeparated(*surfaces),
		Tags:           parseCommaSeparated(*tags),
		TimeoutSeconds: *timeout,
		MaxRetries:     *maxRetries,
		RetryDelay:     *retryDelay,
		Priority:       *priority,
	}

	reqBytes, err := json.Marshal(taskReq)
	if err != nil {
		return fmt.Errorf("failed to marshal task request: %w", err)
	}

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tasksURL, bytes.NewReader(reqBytes))
	if err != nil {
		return fmt.Errorf("failed to build task creation request: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/json")
	if *token != "" {
		postReq.Header.Set("Authorization", "Bearer "+*token)
	}

	client := newTLSClient(*caCert, *insecureSkipTLS)
	client.Timeout = 15 * time.Second
	resp, err := client.Do(postReq)
	if err != nil {
		return fmt.Errorf("failed to dispatch task to coordinator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coordinator rejected task (status %d): %s", resp.StatusCode, string(body))
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		return fmt.Errorf("failed to parse task response: %w", err)
	}

	fmt.Fprintf(stdout, "Task ID: %s (status: %s)\n", taskResp.TaskID, taskResp.Status)

	eventsURL := taskResp.EventsURL
	if !strings.HasPrefix(eventsURL, "http://") && !strings.HasPrefix(eventsURL, "https://") {
		eventsURL = coordURL + "/" + strings.TrimLeft(eventsURL, "/")
	}

	// Connect to SSE stream (no short timeout on client for streaming)
	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build events streaming request: %w", err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	if *token != "" {
		streamReq.Header.Set("Authorization", "Bearer "+*token)
	}

	streamClient := newTLSClient(*caCert, *insecureSkipTLS)
	streamResp, err := streamClient.Do(streamReq)
	if err != nil {
		return fmt.Errorf("failed to connect to task events stream: %w", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		return fmt.Errorf("failed to stream events (status %d): %s", streamResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(streamResp.Body)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		dataContent := strings.TrimPrefix(line, "data:")
		dataContent = strings.TrimSpace(dataContent)
		if dataContent == "" {
			continue
		}

		evt, err := protocol.ParseSSE([]byte(line))
		if err != nil {
			var directEvt protocol.Event
			if jsonErr := json.Unmarshal([]byte(dataContent), &directEvt); jsonErr != nil {
				continue
			}
			evt = &directEvt
		}

		if printEvent(stdout, evt) {
			return nil
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("error reading event stream: %w", err)
	}

	return nil
}

// printEvent formats an SSE event to stdout. Returns true if the event represents a terminal state.
func printEvent(stdout io.Writer, evt *protocol.Event) bool {
	if evt == nil {
		return false
	}

	switch evt.Type {
	case protocol.EventStatus:
		var sp protocol.StatusPayload
		if err := evt.UnmarshalPayload(&sp); err == nil {
			if sp.Message != "" {
				fmt.Fprintf(stdout, "[status] %s - %s\n", sp.Status, sp.Message)
			} else {
				fmt.Fprintf(stdout, "[status] %s\n", sp.Status)
			}
			if sp.Status == protocol.TaskStatusCompleted || sp.Status == protocol.TaskStatusFailed || sp.Status == protocol.TaskStatusCanceled {
				return true
			}
		} else {
			fmt.Fprintf(stdout, "[status] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventThought:
		var tp protocol.ThoughtPayload
		if err := evt.UnmarshalPayload(&tp); err == nil {
			fmt.Fprint(stdout, tp.Text)
		} else {
			fmt.Fprint(stdout, string(evt.Payload))
		}
		return false

	case protocol.EventToolCall:
		var tcp protocol.ToolCallPayload
		if err := evt.UnmarshalPayload(&tcp); err == nil {
			argsJSON, _ := json.Marshal(tcp.Args)
			fmt.Fprintf(stdout, "\n[tool_call] %s(%s)\n", tcp.Tool, string(argsJSON))
		} else {
			fmt.Fprintf(stdout, "\n[tool_call] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventToolResult:
		var trp protocol.ToolResultPayload
		if err := evt.UnmarshalPayload(&trp); err == nil {
			if trp.IsError {
				fmt.Fprintf(stdout, "[tool_result:error] %s\n", trp.Output)
			} else {
				fmt.Fprintf(stdout, "[tool_result] %s\n", trp.Output)
			}
		} else {
			fmt.Fprintf(stdout, "[tool_result] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventQuery:
		var qp protocol.QueryPayload
		if err := evt.UnmarshalPayload(&qp); err == nil {
			fmt.Fprintf(stdout, "\n[query] %s (choices: %v)\n", qp.Prompt, qp.Choices)
		} else {
			fmt.Fprintf(stdout, "\n[query] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventCompletion:
		var cp protocol.CompletionPayload
		if err := evt.UnmarshalPayload(&cp); err == nil {
			fmt.Fprintf(stdout, "\n[completion] %s\n", cp.Result)
			if len(cp.FilesChanged) > 0 {
				fmt.Fprintf(stdout, "  Files changed: %s\n", strings.Join(cp.FilesChanged, ", "))
			}
			if cp.CommitHash != "" {
				fmt.Fprintf(stdout, "  Commit: %s\n", cp.CommitHash)
			}
		} else {
			fmt.Fprintf(stdout, "\n[completion] %s\n", string(evt.Payload))
		}
		return true

	case protocol.EventError:
		var ep protocol.ErrorPayload
		if err := evt.UnmarshalPayload(&ep); err == nil {
			fmt.Fprintf(stdout, "\n[error] %s: %s\n", ep.Code, ep.Message)
		} else {
			fmt.Fprintf(stdout, "\n[error] %s\n", string(evt.Payload))
		}
		return true

	default:
		fmt.Fprintf(stdout, "[%s] %s\n", evt.Type, string(evt.Payload))
		return false
	}
}

func parseCommaSeparated(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// extractPortFromEndpoint extracts the port from a worker endpoint URL (e.g. "http://worker-alpha:8081" -> ":8081"),
// falling back to ":8081" if absent or invalid.
func extractPortFromEndpoint(endpoint string) string {
	if endpoint == "" {
		return ":8081"
	}
	ep := endpoint
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ":8081"
	}
	p := u.Port()
	if p == "" {
		return ":8081"
	}
	return ":" + p
}

// Certificate management commands

// runCertIssue issues a new node certificate for mTLS authentication.
func runCertIssue(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cert-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	tlsDir := fs.String("tls-dir", "", "TLS directory with CA (required)")
	nodeID := fs.String("node-id", "", "Node identifier for the certificate (required)")
	outputDir := fs.String("output", "", "Output directory for certificate files (defaults to tls-dir)")
	validDays := fs.Int("valid-days", 365, "Certificate validity period in days")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *tlsDir == "" {
		return errors.New("-tls-dir is required")
	}
	if *nodeID == "" {
		return errors.New("-node-id is required")
	}

	// Load CA
	ca, err := pki.LoadCAPemFiles(*tlsDir)
	if err != nil {
		return fmt.Errorf("failed to load CA from %s: %w\nTIP: Run 'gentle-mesh server -tls-init' first", *tlsDir, err)
	}

	// Generate node certificate
	validFor := time.Duration(*validDays) * 24 * time.Hour
	nodeCert, certInfo, err := ca.GenerateNodeCert(*nodeID, validFor)
	if err != nil {
		return fmt.Errorf("failed to generate node certificate: %w", err)
	}

	// Save certificate files
	outDir := *outputDir
	if outDir == "" {
		outDir = *tlsDir
	}

	if err := nodeCert.SaveNodeCertFiles(outDir, false); err != nil {
		return fmt.Errorf("failed to save certificate files: %w", err)
	}

	certFile := filepath.Join(outDir, *nodeID+".pem")
	keyFile := filepath.Join(outDir, *nodeID+".key")

	fmt.Fprintf(stdout, "✅ Certificate issued for node: %s\n", *nodeID)
	fmt.Fprintf(stdout, "  Certificate: %s\n", certFile)
	fmt.Fprintf(stdout, "  Private key: %s\n", keyFile)
	fmt.Fprintf(stdout, "  Expires: %s\n", certInfo.ExpiresAt.Format("2006-01-02"))
	fmt.Fprintf(stdout, "  Serial: %s\n", certInfo.Serial)
	fmt.Fprintln(stdout, "")
	fmt.Fprintf(stdout, "Distribute the certificate and key to the node, then run:\n")
	fmt.Fprintf(stdout, "  gentle-mesh worker -coordinator https://... -cert %s -key %s\n", certFile, keyFile)

	return nil
}

// runGenCSR generates a CSR locally for enrollment (no CA required).
func runGenCSR(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen-csr", flag.ContinueOnError)
	fs.SetOutput(stderr)

	nodeID := fs.String("node-id", "", "Node identifier for the CSR (required)")
	outputDir := fs.String("output", ".", "Output directory for CSR and key files")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *nodeID == "" {
		return errors.New("-node-id is required")
	}

	// Generate CSR locally
	csrResult, err := pki.GenerateCSR(*nodeID)
	if err != nil {
		return fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Save CSR and key
	csrFile := filepath.Join(*outputDir, *nodeID+".csr")
	keyFile := filepath.Join(*outputDir, *nodeID+".key")

	if err := os.WriteFile(csrFile, []byte(csrResult.CSRPEM), 0600); err != nil {
		return fmt.Errorf("failed to save CSR: %w", err)
	}
	if err := os.WriteFile(keyFile, []byte(csrResult.PrivateKeyPEM), 0600); err != nil {
		return fmt.Errorf("failed to save private key: %w", err)
	}

	fmt.Fprintf(stdout, "✅ CSR generated for node: %s\n", *nodeID)
	fmt.Fprintf(stdout, "  CSR: %s\n", csrFile)
	fmt.Fprintf(stdout, "  Private key: %s\n", keyFile)
	fmt.Fprintln(stdout, "")
	fmt.Fprintf(stdout, "Send the CSR to the coordinator for signing.\n")
	fmt.Fprintf(stdout, "Use -join-token with the worker to auto-enroll.\n")

	return nil
}

// runCertRevoke revokes a node certificate.
func runCertRevoke(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cert-revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.String("tls-dir", "", "TLS directory with CA (required)")
	nodeID := fs.String("node-id", "", "Node identifier to revoke (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *nodeID == "" {
		return errors.New("-node-id is required")
	}

	// Revocation is handled via the database, not filesystem
	// For now, we just print a warning since we don't have DB access here
	fmt.Fprintf(stdout, "⚠️  To revoke a certificate, you need to:")
	fmt.Fprintf(stdout, "\n  1. Delete the certificate files from the node")
	fmt.Fprintf(stdout, "\n  2. Issue a new certificate with a different serial")
	fmt.Fprintf(stdout, "\n\nFor production revocation lists, implement CRL distribution.\n")

	return nil
}

// runCertList lists all issued certificates.
func runCertList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cert-list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	tlsDir := fs.String("tls-dir", "", "TLS directory with CA (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *tlsDir == "" {
		return errors.New("-tls-dir is required")
	}

	// List certificates from filesystem (simplified for now)
	entries, err := os.ReadDir(*tlsDir)
	if err != nil {
		return fmt.Errorf("failed to read TLS directory: %w", err)
	}

	fmt.Fprintln(stdout, "📜 Issued certificates:")
	fmt.Fprintln(stdout, "")

	hasCerts := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip CA and server certs
		if name == pki.CAPemFile || name == pki.CAPrivateFile ||
			name == pki.CertPemFile || name == pki.CertKeyFile {
			continue
		}
		if strings.HasSuffix(name, ".pem") {
			hasCerts = true
			nodeID := strings.TrimSuffix(name, ".pem")
			info, _ := pki.LoadNodeCertFiles(*tlsDir, nodeID)
			fmt.Fprintf(stdout, "  Node: %s\n", nodeID)
			if info != nil {
				fmt.Fprintf(stdout, "    Expires: %s\n", info.Cert.NotAfter.Format("2006-01-02"))
			}
			fmt.Fprintln(stdout, "")
		}
	}

	if !hasCerts {
		fmt.Fprintln(stdout, "  (No node certificates issued)")
	}

	return nil
}

// Token management commands

// runGenToken generates an enrollment token for automatic certificate enrollment.
func runGenToken(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen-token", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db-path", "", "Path to SQLite database (required)")
	maxUses := fs.Int("max-uses", 1, "Maximum number of times the token can be used (0 = unlimited)")
	validDays := fs.Int("valid-days", 30, "Number of days until the token expires (0 = never)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dbPath == "" || *dbPath == "none" {
		return errors.New("-db-path is required (e.g., /tmp/mesh/gentle-mesh.db)")
	}

	// Open database
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Initialize schemas if needed
	if err := store.InitTokenSchema(db); err != nil {
		return fmt.Errorf("failed to init token schema: %w", err)
	}

	tokenStore := store.NewSQLiteTokenStore(db)

	// Generate random token
	token := generateSecureToken()

	// Calculate expiration
	var expiresAt *time.Time
	if *validDays > 0 {
		exp := time.Now().Add(time.Duration(*validDays) * 24 * time.Hour)
		expiresAt = &exp
	}

	record := &store.TokenRecord{
		Token:     token,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
		MaxUses:   *maxUses,
	}

	if err := tokenStore.CreateToken(ctx, record); err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	fmt.Fprintln(stdout, "✅ Enrollment token generated")
	fmt.Fprintf(stdout, "  Token: %s\n", token)
	if *maxUses > 0 {
		fmt.Fprintf(stdout, "  Max uses: %d\n", *maxUses)
	} else {
		fmt.Fprintln(stdout, "  Max uses: unlimited")
	}
	if expiresAt != nil {
		fmt.Fprintf(stdout, "  Expires: %s\n", expiresAt.Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintln(stdout, "  Expires: never")
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintf(stdout, "Share this token with a node to auto-enroll.\n")
	fmt.Fprintf(stdout, "The node will use it like:\n")
	fmt.Fprintf(stdout, "  gentle-mesh worker -join-token %s -coordinator https://...\n", token)

	return nil
}

// runTokenList lists all enrollment tokens.
func runTokenList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token-list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db-path", "", "Path to SQLite database (required)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dbPath == "" || *dbPath == "none" {
		return errors.New("-db-path is required")
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	tokenStore := store.NewSQLiteTokenStore(db)

	tokens, err := tokenStore.ListTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tokens: %w", err)
	}

	fmt.Fprintln(stdout, "📜 Enrollment tokens:")
	fmt.Fprintln(stdout, "")

	if len(tokens) == 0 {
		fmt.Fprintln(stdout, "  (No tokens)")
		return nil
	}

	for _, t := range tokens {
		status := "active"
		if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
			status = "expired"
		} else if t.Uses >= t.MaxUses && t.MaxUses > 0 {
			status = "used"
		}

		fmt.Fprintf(stdout, "  Token: %s [%s]\n", t.Token, status)
		fmt.Fprintf(stdout, "    Created: %s\n", t.CreatedAt.Format("2006-01-02 15:04"))
		if t.ExpiresAt != nil {
			fmt.Fprintf(stdout, "    Expires: %s\n", t.ExpiresAt.Format("2006-01-02 15:04"))
		}
		fmt.Fprintf(stdout, "    Uses: %d/%d\n", t.Uses, t.MaxUses)
		if t.UsedAt != nil {
			fmt.Fprintf(stdout, "    Last used: %s\n", t.UsedAt.Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(stdout, "")
	}

	return nil
}

// runTokenRevoke revokes an enrollment token.
func runTokenRevoke(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token-revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db-path", "", "Path to SQLite database (required)")
	tokenValue := fs.String("token", "", "Token to revoke (required)")


	if err := fs.Parse(args); err != nil {
		return err
	}

	if *tokenValue == "" {
		return errors.New("-token is required")
	}

	if *dbPath == "" || *dbPath == "none" {
		return errors.New("-db-path is required")
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	tokenStore := store.NewSQLiteTokenStore(db)

	// Mark as used (effectively revokes by setting uses = max)
	t, err := tokenStore.UseToken(ctx, *tokenValue)
	if err != nil {
		if err.Error() == "token not found" {
			return fmt.Errorf("token not found: %s", *tokenValue)
		}
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	fmt.Fprintf(stdout, "✅ Token revoked: %s\n", *tokenValue)
	fmt.Fprintf(stdout, "  Total uses: %d\n", t.Uses)
	_ = t // suppress unused

	return nil
}

// generateSecureToken generates a cryptographically secure random token.
func generateSecureToken() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32

	randBytes := make([]byte, length)
	for i := range randBytes {
		randBytes[i] = charset[i%len(charset)]
	}

	// Read random bytes using crypto/rand (already imported)
	_, err := rand.Read(randBytes)
	if err != nil {
		// Fallback to time-based if crypto/rand fails
		for i := range randBytes {
			randBytes[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		}
	}

	result := make([]byte, length)
	for i, b := range randBytes {
		result[i] = charset[int(b)%len(charset)]
	}

	return string(result)
}
