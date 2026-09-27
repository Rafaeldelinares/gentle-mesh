// Package main is the test harness CLI for RFC-002 integration testing.
// It can run as an agent server or as a scenario orchestrator.
//
// Usage:
//
//   serve --port 8080              Run as an agent HTTP server
//   test-scenario                  Run the full integration test scenario
//   health --url http://host:8080  Check agent health
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/integration/testscenario"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "test-scenario":
		runScenario(os.Args[2:])
	case "health":
		runHealth(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`RFC-002 Test Harness

Commands:
  serve [--port N]              Run as an agent HTTP server
  test-scenario                 Run the full integration test scenario
  health --url URL              Check agent health`)
}

// ─────────────────────────────────────────────────────────────────
// serve command
// ─────────────────────────────────────────────────────────────────

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 8080, "HTTP server port")
	agentID := fs.String("agent-id", "", "Agent ID (required)")
	role := fs.String("role", "executor", "Role: emitter or executor")
	chainDB := fs.String("chain-db", "/data/chain.db", "Path to SQLite chain database")
	workspace := fs.String("workspace", "/srv/workspace", "Workspace directory")
	evalTimeout := fs.Duration("eval-timeout", 30*time.Second, "Evaluation timeout")
	maxRemed := fs.Int("max-remediations", 1, "Max remediation attempts")

	// TLS flags.
	tlsCert := fs.String("tls-cert", "", "TLS certificate file (enables HTTPS)")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	clientCA := fs.String("client-ca", "", "Client CA file for mTLS (optional)")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *agentID == "" {
		log.Fatal("--agent-id is required")
	}

	if (*tlsCert != "") != (*tlsKey != "") {
		log.Fatal("--tls-cert and --tls-key must both be set or both be empty")
	}

	cfg := agent.Config{
		AgentID:     *agentID,
		Role:        agent.Role(*role),
		ChainDBPath: *chainDB,
		WorkspaceDir: *workspace,
		EvalTimeout: *evalTimeout,
		MaxRemed:   *maxRemed,
		TLSCertFile: *tlsCert,
		TLSKeyFile:  *tlsKey,
		ClientCAFile: *clientCA,
	}

	srv, err := agent.NewServer(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(*port)
	}()

	select {
	case <-ctx.Done():
		log.Println("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("server error: %v", err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// test-scenario command
// ─────────────────────────────────────────────────────────────────

func runScenario(args []string) {
	fs := flag.NewFlagSet("test-scenario", flag.ContinueOnError)
	aURL := fs.String("agent-a", "https://agent-a:8443", "Agent A URL (default: https://agent-a:8443)")
	bURL := fs.String("agent-b", "https://agent-b:8443", "Agent B URL (default: https://agent-b:8443)")
	cURL := fs.String("agent-c", "", "Agent C URL (optional; enables fan-out test)")
	workspace := fs.String("workspace", "/srv/workspace", "Workspace directory")
	caCert := fs.String("ca-cert", "/certs/ca.crt", "Root CA certificate for TLS verification")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification (development only)")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *aURL == "" && *bURL == "" {
		log.Fatal("--agent-a and --agent-b are required")
	}

	scenario, err := testscenario.NewScenarioTLS(*aURL, *bURL, *cURL, *workspace, *caCert, *insecure)
	if err != nil {
		log.Fatalf("create scenario: %v", err)
	}
	if err := scenario.Run(context.Background()); err != nil {
		log.Fatalf("scenario failed: %v", err)
	}
	log.Println("Scenario PASSED")
}

// ─────────────────────────────────────────────────────────────────
// health command
// ─────────────────────────────────────────────────────────────────

func runHealth(args []string) {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "", "Agent URL (required)")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *url == "" {
		log.Fatal("--url is required")
	}

	client := agent.NewHTTPClient(*url)
	h, err := client.Health(context.Background())
	if err != nil {
		log.Fatalf("health check failed: %v", err)
	}
	fmt.Printf("Agent: %s | Role: %s | Status: %s | Time: %s\n",
		h.AgentID, h.Role, h.Status, h.Timestamp.Format(time.RFC3339))
}
