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

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *agentID == "" {
		log.Fatal("--agent-id is required")
	}

	cfg := agent.Config{
		AgentID:     *agentID,
		Role:        agent.Role(*role),
		ChainDBPath: *chainDB,
		WorkspaceDir: *workspace,
		EvalTimeout: *evalTimeout,
		MaxRemed:   *maxRemed,
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
	aURL := fs.String("agent-a", "http://agent-a:8080", "Agent A URL")
	bURL := fs.String("agent-b", "http://agent-b:8080", "Agent B URL")
	workspace := fs.String("workspace", "/srv/workspace", "Workspace directory")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	// If both URLs are empty, run in-process.
	// Otherwise, run over HTTP against the specified agents.
	if *aURL == "" && *bURL == "" {
		log.Fatal("either --agent-a and --agent-b are required, or run in-process")
	}

	scenario := testscenario.NewScenario(*aURL, *bURL, *workspace)
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
