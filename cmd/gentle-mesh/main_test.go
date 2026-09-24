package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

func TestCLI_UsageAndHelp(t *testing.T) {
	ctx := context.Background()

	t.Run("no arguments returns error and prints usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error when no args provided, got nil")
		}
		if !strings.Contains(stderr.String(), "Usage: gentle-mesh") {
			t.Errorf("expected usage in stderr, got: %s", stderr.String())
		}
	})

	t.Run("help subcommand prints usage to stdout", func(t *testing.T) {
		for _, arg := range []string{"help", "-h", "--help"} {
			var stdout, stderr bytes.Buffer
			err := runCLI(ctx, []string{arg}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", arg, err)
			}
			if !strings.Contains(stdout.String(), "Usage: gentle-mesh") {
				t.Errorf("expected usage in stdout for %s, got: %s", arg, stdout.String())
			}
		}
	})

	t.Run("unknown subcommand returns error and prints usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{"foobar"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for unknown subcommand, got nil")
		}
		if !strings.Contains(err.Error(), `unknown subcommand "foobar"`) {
			t.Errorf("expected unknown subcommand error message, got: %v", err)
		}
		if !strings.Contains(stderr.String(), "Usage: gentle-mesh") {
			t.Errorf("expected usage in stderr, got: %s", stderr.String())
		}
	})
}

func TestCLI_RPC_Subcommand(t *testing.T) {
	ctx := context.Background()

	t.Run("rpc --help prints flags to stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLIWithIO(ctx, []string{"rpc", "--help"}, strings.NewReader(""), &stdout, &stderr)
		if err != nil {
			t.Fatalf("unexpected error for rpc --help: %v", err)
		}

		out := stdout.String()
		for _, flagName := range []string{"-coordinator", "-token", "-agent", "-mode", "-approve", "-session"} {
			if !strings.Contains(out, flagName) {
				t.Errorf("expected %q in rpc help output, got: %s", flagName, out)
			}
		}
	})

	t.Run("rpc accepts Pi flags and answers get_state handshake", func(t *testing.T) {
		stdin := strings.NewReader(`{"id":"handshake-1","type":"get_state"}` + "\n")
		var stdout, stderr bytes.Buffer

		err := runCLIWithIO(ctx, []string{
			"rpc",
			"--mode", "rpc",
			"--approve",
			"--session", "/path/file",
		}, stdin, &stdout, &stderr)
		if err != nil {
			t.Fatalf("unexpected error running rpc bridge: %v", err)
		}

		var found bool
		for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}

			var resp struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Command string `json:"command"`
				Success bool   `json:"success"`
			}
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				t.Fatalf("expected JSON response line %q, got error: %v (stderr: %s)", line, err, stderr.String())
			}
			if resp.ID == "handshake-1" && resp.Type == "response" && resp.Command == "get_state" && resp.Success {
				found = true
			}
		}

		if !found {
			t.Errorf("expected a successful get_state response for handshake-1 on stdout, got: %s", stdout.String())
		}
	})

	t.Run("rpc honors GENTLE_MESH_COORDINATOR env default", func(t *testing.T) {
		t.Setenv("GENTLE_MESH_COORDINATOR", "http://mesh-env.internal:9999")

		stdin := strings.NewReader(`{"id":"env-1","type":"get_state"}` + "\n")
		var stdout, stderr bytes.Buffer

		if err := runCLIWithIO(ctx, []string{"rpc"}, stdin, &stdout, &stderr); err != nil {
			t.Fatalf("unexpected error running rpc bridge: %v", err)
		}

		if !strings.Contains(stdout.String(), "http://mesh-env.internal:9999") {
			t.Errorf("expected env coordinator URL in bridge state, got: %s", stdout.String())
		}
	})

	t.Run("rpc rejects unknown flags", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLIWithIO(ctx, []string{"rpc", "--bogus"}, strings.NewReader(""), &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for unknown rpc flag, got nil")
		}
		if !strings.Contains(err.Error(), "bogus") {
			t.Errorf("expected error to mention the unknown flag, got: %v", err)
		}
	})
}

func TestFlagParsingAndValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("run command requires -task flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{"run", "-agent", "worker"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error when -task is missing, got nil")
		}
		if !strings.Contains(err.Error(), "-task flag is required") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("server rejects invalid -territory-mode", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{
			"server",
			"-tasks-dir", t.TempDir(),
			"-territory-mode", "invalid-mode",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for invalid -territory-mode, got nil")
		}
		if !strings.Contains(err.Error(), "invalid -territory-mode") {
			t.Errorf("expected error to mention %q, got: %v", "invalid -territory-mode", err)
		}
	})

	t.Run("server accepts valid -territory-mode values", func(t *testing.T) {
		for _, mode := range []string{"queue", "warn", "strict", "disabled"} {
			t.Run(mode, func(t *testing.T) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("failed to allocate free port: %v", err)
				}
				addr := ln.Addr().String()
				_ = ln.Close()

				tasksDir := t.TempDir()
				serverCtx, cancel := context.WithCancel(context.Background())
				defer cancel()

				var stdout, stderr bytes.Buffer
				errCh := make(chan error, 1)
				go func() {
					errCh <- runCLI(serverCtx, []string{
						"server",
						"-addr", addr,
						"-tasks-dir", tasksDir,
						"-db-path", "none",
						"-territory-mode", mode,
					}, &stdout, &stderr)
				}()

				client := &http.Client{Timeout: 500 * time.Millisecond}
				healthzURL := fmt.Sprintf("http://%s/healthz", addr)
				started := false
				for i := 0; i < 40; i++ {
					resp, err := client.Get(healthzURL)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							started = true
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
				}

				if !started {
					cancel()
					select {
					case <-errCh:
					case <-time.After(2 * time.Second):
					}
					t.Fatalf("coordinator did not start with -territory-mode %q on %s. Stderr: %s", mode, addr, stderr.String())
				}

				cancel()
				select {
				case err := <-errCh:
					if err != nil {
						t.Fatalf("runCLI server failed on shutdown with -territory-mode %q: %v", mode, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("coordinator did not shut down within timeout for -territory-mode %q", mode)
				}

				if !strings.Contains(stdout.String(), "territory mode: "+mode) {
					t.Errorf("expected startup log to report territory mode %q, got: %s", mode, stdout.String())
				}
			})
		}
	})

	t.Run("parseCommaSeparated helper", func(t *testing.T) {
		tests := []struct {
			input    string
			expected []string
		}{
			{"", nil},
			{"   ", nil},
			{"worker", []string{"worker"}},
			{"worker,explore", []string{"worker", "explore"}},
			{" go , fast ,  heavy ", []string{"go", "fast", "heavy"}},
			{",,,", nil},
		}

		for _, tc := range tests {
			res := parseCommaSeparated(tc.input)
			if len(res) != len(tc.expected) {
				t.Fatalf("input %q: expected len %d, got %d (%v)", tc.input, len(tc.expected), len(res), res)
			}
			for i := range res {
				if res[i] != tc.expected[i] {
					t.Errorf("input %q at index %d: expected %q, got %q", tc.input, i, tc.expected[i], res[i])
				}
			}
		}
	})

	t.Run("extractPortFromEndpoint helper", func(t *testing.T) {
		tests := []struct {
			endpoint string
			expected string
		}{
			{"http://worker-alpha:8081", ":8081"},
			{"http://localhost:9000/", ":9000"},
			{"worker-beta:7777", ":7777"},
			{"http://localhost", ":8081"},
			{"https://node.internal", ":8081"},
			{"", ":8081"},
			{"invalid-url:::", ":8081"},
		}

		for _, tc := range tests {
			res := extractPortFromEndpoint(tc.endpoint)
			if res != tc.expected {
				t.Errorf("endpoint %q: expected %q, got %q", tc.endpoint, tc.expected, res)
			}
		}
	})
}

func TestServer_StartupAndGracefulShutdown(t *testing.T) {
	// Find a free TCP port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	tasksDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runCLI(ctx, []string{
			"server",
			"-addr", addr,
			"-tasks-dir", tasksDir,
			"-heartbeat-timeout", "10s",
			"-task-ttl", "1h",
		}, &stdout, &stderr)
	}()

	// Poll healthz until server is running
	healthzURL := fmt.Sprintf("http://%s/healthz", addr)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	started := false
	for i := 0; i < 40; i++ {
		resp, err := client.Get(healthzURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				started = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !started {
		t.Fatalf("coordinator server did not start within deadline on %s. Stderr: %s", addr, stderr.String())
	}

	// Trigger graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runCLI server failed on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("coordinator server did not shut down within timeout")
	}

	if !strings.Contains(stdout.String(), "Coordinator stopped gracefully") {
		t.Errorf("expected shutdown log in stdout, got: %s", stdout.String())
	}
}

func TestServer_DBPathFlag(t *testing.T) {
	t.Run("server accepts --db-path flag with custom path", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to allocate free port: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		tasksDir := t.TempDir()
		customDB := filepath.Join(t.TempDir(), "custom-main.db")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		errCh := make(chan error, 1)
		go func() {
			errCh <- runCLI(ctx, []string{
				"server",
				"-addr", addr,
				"-tasks-dir", tasksDir,
				"--db-path", customDB,
			}, &stdout, &stderr)
		}()

		healthzURL := fmt.Sprintf("http://%s/healthz", addr)
		client := &http.Client{Timeout: 500 * time.Millisecond}
		started := false
		for i := 0; i < 40; i++ {
			resp, err := client.Get(healthzURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					started = true
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}

		if !started {
			t.Fatalf("coordinator server did not start within deadline on %s. Stderr: %s", addr, stderr.String())
		}

		if _, err := os.Stat(customDB); err != nil {
			t.Errorf("expected custom db file to exist at %s: %v", customDB, err)
		}

		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("runCLI server failed on shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("coordinator server did not shut down within timeout")
		}
	})

	t.Run("server accepts --db-path none to disable persistence", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to allocate free port: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		tasksDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdout, stderr bytes.Buffer
		errCh := make(chan error, 1)
		go func() {
			errCh <- runCLI(ctx, []string{
				"server",
				"-addr", addr,
				"-tasks-dir", tasksDir,
				"--db-path", "none",
			}, &stdout, &stderr)
		}()

		healthzURL := fmt.Sprintf("http://%s/healthz", addr)
		client := &http.Client{Timeout: 500 * time.Millisecond}
		started := false
		for i := 0; i < 40; i++ {
			resp, err := client.Get(healthzURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					started = true
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}

		if !started {
			t.Fatalf("coordinator server did not start within deadline on %s. Stderr: %s", addr, stderr.String())
		}

		defaultDB := filepath.Join(tasksDir, "gentle-mesh.db")
		if _, err := os.Stat(defaultDB); !os.IsNotExist(err) {
			t.Errorf("expected db file not to exist with --db-path none, got err: %v", err)
		}

		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("runCLI server failed on shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("coordinator server did not shut down within timeout")
		}
	})
}

func TestWorker_JoinAndHeartbeatCycle(t *testing.T) {
	var (
		mu             sync.Mutex
		joinCalled     bool
		joinReq        protocol.NodeJoinRequest
		joinToken      string
		heartbeatCount int
		heartbeats     []protocol.NodeHeartbeatRequest
		heartbeatToken string
	)

	heartbeatDone := make(chan struct{})
	var closeOnce sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/mesh/join":
			mu.Lock()
			joinCalled = true
			joinToken = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&joinReq)
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.NodeInfo{
				NodeID: joinReq.NodeID,
				Status: protocol.NodeStatusOnline,
			})

		case "/v1/mesh/heartbeat":
			var hb protocol.NodeHeartbeatRequest
			_ = json.NewDecoder(r.Body).Decode(&hb)

			mu.Lock()
			heartbeatCount++
			heartbeats = append(heartbeats, hb)
			heartbeatToken = r.Header.Get("Authorization")
			count := heartbeatCount
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})

			if count >= 2 {
				closeOnce.Do(func() {
					close(heartbeatDone)
				})
			}

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	errCh := make(chan error, 1)

	go func() {
		errCh <- runCLI(ctx, []string{
			"worker",
			"-coordinator", ts.URL,
			"-node-id", "test-worker-alpha",
			"-endpoint", "http://worker-alpha:8081",
			"-agents", "worker,explore",
			"-tags", "go,fast,gpu",
			"-concurrency", "4",
			"-heartbeat-interval", "20ms",
			"-token", "test-secret-token",
		}, &stdout, &stderr)
	}()

	select {
	case <-heartbeatDone:
		// Succeeded receiving join + 2 heartbeats
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker join and heartbeats")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("worker returned unexpected error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down within timeout")
	}

	mu.Lock()
	defer mu.Unlock()

	if !joinCalled {
		t.Fatal("join endpoint was not called")
	}
	if joinReq.NodeID != "test-worker-alpha" {
		t.Errorf("expected node_id %q, got %q", "test-worker-alpha", joinReq.NodeID)
	}
	if joinReq.Endpoint != "http://worker-alpha:8081" {
		t.Errorf("expected endpoint %q, got %q", "http://worker-alpha:8081", joinReq.Endpoint)
	}
	if joinReq.MaxConcurrency != 4 {
		t.Errorf("expected max concurrency 4, got %d", joinReq.MaxConcurrency)
	}
	if !joinReq.Hardware.HasGPU {
		t.Error("expected HasGPU=true when 'gpu' tag is provided")
	}
	if len(joinReq.Agents) != 2 {
		t.Fatalf("expected 2 agent profiles, got %d", len(joinReq.Agents))
	}
	if joinToken != "Bearer test-secret-token" {
		t.Errorf("expected join token Bearer test-secret-token, got: %s", joinToken)
	}
	if heartbeatCount < 2 {
		t.Errorf("expected at least 2 heartbeats, got %d", heartbeatCount)
	}
	if heartbeatToken != "Bearer test-secret-token" {
		t.Errorf("expected heartbeat token Bearer test-secret-token, got: %s", heartbeatToken)
	}
	for i, hb := range heartbeats {
		if hb.ActiveTasks != 0 {
			t.Errorf("heartbeat %d: expected ActiveTasks=0, got %d", i, hb.ActiveTasks)
		}
	}
	if !strings.Contains(stdout.String(), "Worker HTTP server started") {
		t.Errorf("expected worker HTTP server start log in stdout, got: %s", stdout.String())
	}
}

func TestWorker_JoinFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"coordinator database unavailable"}`))
	}))
	defer ts.Close()

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	err := runCLI(ctx, []string{
		"worker",
		"-coordinator", ts.URL,
		"-node-id", "fail-worker",
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("expected error on failed join, got nil")
	}
	if !strings.Contains(err.Error(), "rejected join (status 500)") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWorker_ReRegisterOn404Heartbeat(t *testing.T) {
	var joinCount atomic.Int32
	var heartbeatCount atomic.Int32
	reJoinDone := make(chan struct{})
	var closeOnce sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/mesh/join":
			count := joinCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.NodeInfo{
				NodeID: "rejoin-worker",
				Status: protocol.NodeStatusOnline,
			})
			if count >= 2 {
				closeOnce.Do(func() {
					close(reJoinDone)
				})
			}
		case "/v1/mesh/heartbeat":
			heartbeatCount.Add(1)
			// Return 404 to trigger re-join
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"node not found"}`))
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	go func() {
		_ = runCLI(ctx, []string{
			"worker",
			"-coordinator", ts.URL,
			"-node-id", "rejoin-worker",
			"-heartbeat-interval", "20ms",
		}, &stdout, &stderr)
	}()

	select {
	case <-reJoinDone:
		// Successfully re-registered after 404 heartbeat
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker re-registration after 404 heartbeat")
	}
}

func TestNodes_OutputFormatting(t *testing.T) {
	ctx := context.Background()

	t.Run("formats table with nodes correctly", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/mesh/nodes" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Authorization") != "Bearer secret-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			nodes := []*protocol.NodeInfo{
				{
					NodeID:   "worker-alpha",
					Endpoint: "http://worker-alpha:8081",
					Status:   protocol.NodeStatusOnline,
					Agents: []protocol.AgentProfile{
						{Name: "worker", Tags: []string{"go", "fast"}},
					},
					MaxConcurrency: 2,
					ActiveTasks:    0,
				},
				{
					NodeID:   "worker-gamma",
					Endpoint: "http://worker-gamma:8081",
					Status:   protocol.NodeStatusDegraded,
					Agents: []protocol.AgentProfile{
						{Name: "explore", Tags: []string{"research"}},
					},
					MaxConcurrency: 4,
					ActiveTasks:    1,
				},
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		}))
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{
			"nodes",
			"-coordinator", ts.URL,
			"-token", "secret-token",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running nodes: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "NODE ID") || !strings.Contains(out, "ENDPOINT") || !strings.Contains(out, "STATUS") {
			t.Errorf("missing table header in output: %s", out)
		}
		if !strings.Contains(out, "worker-alpha") || !strings.Contains(out, "0/2") || !strings.Contains(out, "go,fast") {
			t.Errorf("missing worker-alpha row in output: %s", out)
		}
		if !strings.Contains(out, "worker-gamma") || !strings.Contains(out, "1/4") || !strings.Contains(out, "research") {
			t.Errorf("missing worker-gamma row in output: %s", out)
		}
	})

	t.Run("handles empty nodes list", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []*protocol.NodeInfo{}})
		}))
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{
			"nodes",
			"-coordinator", ts.URL,
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running nodes: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "No nodes registered in the mesh") {
			t.Errorf("expected no nodes notice in output, got: %s", out)
		}
	})
}

func TestRadar_OutputFormatting(t *testing.T) {
	ctx := context.Background()

	t.Run("formats table with active agents correctly", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/mesh/radar" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Authorization") != "Bearer radar-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			report := protocol.RadarReport{
				ClusterName: "gentle-mesh",
				Timestamp:   time.Now().Unix(),
				ActiveAgents: []protocol.ActiveTerritory{
					{
						TaskID:         "task-001",
						NodeID:         "node-gpu-1",
						Agent:          "worker",
						Phase:          protocol.AgentPhaseApply,
						Domain:         "auth",
						BlastRadius:    protocol.BlastRadiusSharedSchema,
						EditSurfaces:   []string{"pkg/auth/jwt.go", "pkg/auth/middleware.go"},
						CurrentAction:  "Running tests with race detector",
						LastActivityAt: time.Now().Unix(),
					},
					{
						TaskID:         "task-002",
						NodeID:         "",
						Agent:          "explore",
						Phase:          protocol.AgentPhaseExplore,
						Domain:         "database",
						BlastRadius:    protocol.BlastRadiusReadOnly,
						EditSurfaces:   []string{"pkg/db/schema.sql"},
						CurrentAction:  "Running tool read",
						LastActivityAt: time.Now().Unix(),
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(report)
		}))
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{
			"radar",
			"-coordinator", ts.URL,
			"-token", "radar-token",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running radar: %v", err)
		}

		out := stdout.String()
		for _, col := range []string{"NODE/TASK ID", "AGENT", "PHASE", "DOMAIN", "BLAST RADIUS", "SURFACES", "CURRENT ACTION"} {
			if !strings.Contains(out, col) {
				t.Errorf("missing column %q in radar output: %s", col, out)
			}
		}

		if !strings.Contains(out, "node-gpu-1/task-001") ||
			!strings.Contains(out, "worker") ||
			!strings.Contains(out, "apply") ||
			!strings.Contains(out, "auth") ||
			!strings.Contains(out, "shared-schema") ||
			!strings.Contains(out, "pkg/auth/jwt.go,pkg/auth/middleware.go") ||
			!strings.Contains(out, "Running tests with race detector") {
			t.Errorf("missing task-001 row details in output: %s", out)
		}

		if !strings.Contains(out, "task-002") ||
			!strings.Contains(out, "explore") ||
			!strings.Contains(out, "database") ||
			!strings.Contains(out, "read-only") ||
			!strings.Contains(out, "pkg/db/schema.sql") ||
			!strings.Contains(out, "Running tool read") {
			t.Errorf("missing task-002 row details in output: %s", out)
		}
	})

	t.Run("handles empty active agents in radar", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.RadarReport{
				ClusterName:  "gentle-mesh",
				Timestamp:    time.Now().Unix(),
				ActiveAgents: []protocol.ActiveTerritory{},
			})
		}))
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err := runCLI(ctx, []string{
			"radar",
			"-coordinator", ts.URL,
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running radar: %v", err)
		}

		out := stdout.String()
		expectedMsg := "No active agents currently running in the mesh radar."
		if !strings.Contains(out, expectedMsg) {
			t.Errorf("expected %q in output, got: %s", expectedMsg, out)
		}
	})
}

func TestRun_DispatchAndSSEConsumption(t *testing.T) {
	tasksDir := t.TempDir()
	simRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{
		Tokens:           []string{"Reading source...", " Compiling...", "\n"},
		CompletionResult: "Task finished cleanly",
		FilesChanged:     []string{"cmd/gentle-mesh/main.go"},
	})

	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir: tasksDir,
		Runner:   simRunner,
	})
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err = runCLI(ctx, []string{
		"run",
		"-coordinator", ts.URL,
		"-agent", "worker",
		"-task", "Write complete CLI implementation",
		"-repo", "https://github.com/gentleman-programming/gentle-mesh",
		"-branch", "feature/mesh-foundation",
	}, &stdout, &stderr)

	if err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Task ID: task-") {
		t.Errorf("expected Task ID in output, got: %s", out)
	}
	if !strings.Contains(out, "[status] running") {
		t.Errorf("expected status running in output, got: %s", out)
	}
	if !strings.Contains(out, "Reading source...") || !strings.Contains(out, "Compiling...") {
		t.Errorf("expected thought tokens in output, got: %s", out)
	}
	if !strings.Contains(out, "[completion] Task finished cleanly") {
		t.Errorf("expected completion in output, got: %s", out)
	}
	if !strings.Contains(out, "cmd/gentle-mesh/main.go") {
		t.Errorf("expected files changed in output, got: %s", out)
	}
}

func TestRun_DomainBlastRadiusSurfacesFlags(t *testing.T) {
	t.Run("explicit domain, blast-radius, and surfaces flags", func(t *testing.T) {
		var receivedReq protocol.TaskRequest
		var mu sync.Mutex

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/tasks" && r.Method == http.MethodPost {
				mu.Lock()
				_ = json.NewDecoder(r.Body).Decode(&receivedReq)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(protocol.TaskResponse{
					TaskID:    "task-test-custom",
					Status:    protocol.TaskStatusCompleted,
					EventsURL: "/v1/tasks/task-test-custom/events",
				})
				return
			}

			if strings.HasSuffix(r.URL.Path, "/events") {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				evt, _ := protocol.NewEvent(1, "task-test-custom", protocol.EventStatus, protocol.StatusPayload{
					Status: protocol.TaskStatusCompleted,
				})
				sseData := evt.FormatSSE()
				_, _ = w.Write(sseData)
				if flusher != nil {
					flusher.Flush()
				}
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		var stdout, stderr bytes.Buffer

		err := runCLI(ctx, []string{
			"run",
			"-coordinator", ts.URL,
			"-agent", "worker",
			"-task", "Scaffold auth service",
			"-domain", "auth",
			"-blast-radius", "shared-schema",
			"-surfaces", "pkg/auth/jwt.go, pkg/auth/middleware.go",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running task: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()

		if receivedReq.Domain != "auth" {
			t.Errorf("expected domain 'auth', got %q", receivedReq.Domain)
		}
		if receivedReq.BlastRadius != protocol.BlastRadiusSharedSchema {
			t.Errorf("expected blast radius %q, got %q", protocol.BlastRadiusSharedSchema, receivedReq.BlastRadius)
		}
		expectedSurfaces := []string{"pkg/auth/jwt.go", "pkg/auth/middleware.go"}
		if len(receivedReq.EditSurfaces) != len(expectedSurfaces) {
			t.Fatalf("expected %d edit surfaces, got %d (%v)", len(expectedSurfaces), len(receivedReq.EditSurfaces), receivedReq.EditSurfaces)
		}
		for i, s := range expectedSurfaces {
			if receivedReq.EditSurfaces[i] != s {
				t.Errorf("edit surface[%d]: expected %q, got %q", i, s, receivedReq.EditSurfaces[i])
			}
		}
	})

	t.Run("default blast-radius and empty domain and surfaces", func(t *testing.T) {
		var receivedReq protocol.TaskRequest
		var mu sync.Mutex

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/tasks" && r.Method == http.MethodPost {
				mu.Lock()
				_ = json.NewDecoder(r.Body).Decode(&receivedReq)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(protocol.TaskResponse{
					TaskID:    "task-test-default",
					Status:    protocol.TaskStatusCompleted,
					EventsURL: "/v1/tasks/task-test-default/events",
				})
				return
			}

			if strings.HasSuffix(r.URL.Path, "/events") {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				evt, _ := protocol.NewEvent(1, "task-test-default", protocol.EventStatus, protocol.StatusPayload{
					Status: protocol.TaskStatusCompleted,
				})
				sseData := evt.FormatSSE()
				_, _ = w.Write(sseData)
				if flusher != nil {
					flusher.Flush()
				}
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		var stdout, stderr bytes.Buffer

		err := runCLI(ctx, []string{
			"run",
			"-coordinator", ts.URL,
			"-agent", "worker",
			"-task", "Default task prompt",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error running task: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()

		if receivedReq.Domain != "" {
			t.Errorf("expected empty domain, got %q", receivedReq.Domain)
		}
		if receivedReq.BlastRadius != protocol.BlastRadiusIsolated {
			t.Errorf("expected default blast radius %q, got %q", protocol.BlastRadiusIsolated, receivedReq.BlastRadius)
		}
		if len(receivedReq.EditSurfaces) != 0 {
			t.Errorf("expected empty edit surfaces, got %v", receivedReq.EditSurfaces)
		}
	})
}

func TestRun_ToolCallsAndFailure(t *testing.T) {
	t.Run("handles tool calls and queries", func(t *testing.T) {
		tasksDir := t.TempDir()
		simRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{
			ToolCalls: []runner.SimulatedToolCall{
				{
					ToolName: "git_diff",
					Input:    map[string]any{"staged": true},
					Result:   "diff --git a/file.go",
				},
			},
			CompletionResult: "Success after tool",
		})

		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir: tasksDir,
			Runner:   simRunner,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}

		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err = runCLI(context.Background(), []string{
			"run",
			"-coordinator", ts.URL,
			"-task", "Run tools",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "[tool_call] git_diff") {
			t.Errorf("expected tool_call in output, got: %s", out)
		}
		if !strings.Contains(out, "[tool_result] diff --git a/file.go") {
			t.Errorf("expected tool_result in output, got: %s", out)
		}
		if !strings.Contains(out, "[completion] Success after tool") {
			t.Errorf("expected completion in output, got: %s", out)
		}
	})

	t.Run("handles simulated failure event cleanly", func(t *testing.T) {
		tasksDir := t.TempDir()
		simRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{
			FailWithError: errors.New("compilation failed"),
		})

		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir: tasksDir,
			Runner:   simRunner,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}

		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		var stdout, stderr bytes.Buffer
		err = runCLI(context.Background(), []string{
			"run",
			"-coordinator", ts.URL,
			"-task", "Failing task",
		}, &stdout, &stderr)

		if err != nil {
			t.Fatalf("run should exit cleanly on terminal error event, got error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "[error] SIMULATED_FAILURE: compilation failed") {
			t.Errorf("expected error event formatted in output, got: %s", out)
		}
	})
}

func TestPrintEventUnit(t *testing.T) {
	tests := []struct {
		name       string
		event      *protocol.Event
		expectTerm bool
		contains   string
	}{
		{
			name: "status queued",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(1, "t1", protocol.EventStatus, protocol.StatusPayload{
					Status:  protocol.TaskStatusQueued,
					Message: "Waiting for worker slot",
				})
				return e
			}(),
			expectTerm: false,
			contains:   "[status] queued - Waiting for worker slot",
		},
		{
			name: "status completed is terminal",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(2, "t1", protocol.EventStatus, protocol.StatusPayload{
					Status: protocol.TaskStatusCompleted,
				})
				return e
			}(),
			expectTerm: true,
			contains:   "[status] completed",
		},
		{
			name: "thought event",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(3, "t1", protocol.EventThought, protocol.ThoughtPayload{
					Text: "Refactoring code...",
				})
				return e
			}(),
			expectTerm: false,
			contains:   "Refactoring code...",
		},
		{
			name: "tool call event",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(4, "t1", protocol.EventToolCall, protocol.ToolCallPayload{
					CallID: "c1",
					Tool:   "grep",
					Args:   map[string]any{"pattern": "TODO"},
				})
				return e
			}(),
			expectTerm: false,
			contains:   "[tool_call] grep",
		},
		{
			name: "tool result event with error",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(5, "t1", protocol.EventToolResult, protocol.ToolResultPayload{
					CallID:  "c1",
					Output:  "no matches found",
					IsError: true,
				})
				return e
			}(),
			expectTerm: false,
			contains:   "[tool_result:error] no matches found",
		},
		{
			name: "query event",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(6, "t1", protocol.EventQuery, protocol.QueryPayload{
					QueryID: "q1",
					Prompt:  "Should we proceed?",
					Choices: []string{"yes", "no"},
				})
				return e
			}(),
			expectTerm: false,
			contains:   "[query] Should we proceed?",
		},
		{
			name: "completion event with commit hash",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(7, "t1", protocol.EventCompletion, protocol.CompletionPayload{
					Result:     "Task finished successfully",
					CommitHash: "abcdef123456",
				})
				return e
			}(),
			expectTerm: true,
			contains:   "[completion] Task finished successfully",
		},
		{
			name: "error event is terminal",
			event: func() *protocol.Event {
				e, _ := protocol.NewEvent(8, "t1", protocol.EventError, protocol.ErrorPayload{
					Code:    "FATAL_CRASH",
					Message: "Out of memory",
				})
				return e
			}(),
			expectTerm: true,
			contains:   "[error] FATAL_CRASH: Out of memory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			term := printEvent(&buf, tc.event)
			if term != tc.expectTerm {
				t.Errorf("expected isTerminal=%v, got %v", tc.expectTerm, term)
			}
			if !strings.Contains(buf.String(), tc.contains) {
				t.Errorf("expected output to contain %q, got: %s", tc.contains, buf.String())
			}
		})
	}
}

func TestServer_WorkspaceFlag(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "flag-probe.txt"), []byte("probe"), 0o600); err != nil {
		t.Fatalf("failed to write workspace probe file: %v", err)
	}

	tasksDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runCLI(ctx, []string{
			"server",
			"-addr", addr,
			"-tasks-dir", tasksDir,
			"-db-path", "none",
			"-workspace", workspaceDir,
		}, &stdout, &stderr)
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	healthzURL := fmt.Sprintf("http://%s/healthz", addr)
	started := false
	for i := 0; i < 40; i++ {
		resp, err := client.Get(healthzURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				started = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		cancel()
		t.Fatalf("coordinator did not start on %s. Stderr: %s", addr, stderr.String())
	}

	resp, err := client.Get(fmt.Sprintf("http://%s/v1/workspace/tree", addr))
	if err != nil {
		t.Fatalf("GET /v1/workspace/tree failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var tree meshhttp.WorkspaceTreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("failed to decode tree response: %v", err)
	}
	if tree.Root != workspaceDir {
		t.Errorf("expected -workspace root %q, got %q", workspaceDir, tree.Root)
	}

	found := false
	for _, e := range tree.Entries {
		if e.Name == "flag-probe.txt" {
			found = true
			if e.Path != "flag-probe.txt" {
				t.Errorf("expected relative path %q, got %q", "flag-probe.txt", e.Path)
			}
		}
	}
	if !found {
		t.Errorf("expected flag-probe.txt in workspace tree entries, got: %+v", tree.Entries)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runCLI server failed on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("coordinator did not shut down within timeout")
	}
}

// installFakePi writes an executable `pi` stub into a fresh temp dir and
// prepends that dir to PATH, so `server -runner pi` spawns the stub instead of
// a real model call.
func installFakePi(t *testing.T, scriptBody string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake pi harness requires a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte("#!/bin/sh\n"+scriptBody), 0o755); err != nil {
		t.Fatalf("failed to write fake pi binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// piStreamScript renders a fake pi body that replays a canned JSON event stream.
func piStreamScript(stream string) string {
	return "cat <<'PI_EOF'\n" + stream + "PI_EOF\n"
}

// startCoordinator boots `server` with the given extra args on a free port and
// returns the address plus a graceful shutdown function.
func startCoordinator(t *testing.T, args []string) (string, func() error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runCLI(ctx, append([]string{"server", "-addr", addr}, args...), &stdout, &stderr)
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	started := false
	for i := 0; i < 60; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				started = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		cancel()
		t.Fatalf("coordinator server did not start on %s. Stderr: %s", addr, stderr.String())
	}

	stop := func() error {
		cancel()
		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("coordinator did not shut down within timeout")
			return nil
		}
	}
	return addr, stop
}

// dispatchTaskOn creates a task on a running coordinator and polls until it
// reaches a terminal state.
func dispatchTaskOn(t *testing.T, addr string, req protocol.TaskRequest) protocol.TaskState {
	t.Helper()

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal task request: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://%s/v1/tasks", addr), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating task, got %d", resp.StatusCode)
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("failed to decode task response: %v", err)
	}

	var state protocol.TaskState
	deadline := time.Now().Add(10 * time.Second)
	for {
		getResp, err := client.Get(fmt.Sprintf("http://%s/v1/tasks/%s", addr, taskResp.TaskID))
		if err == nil {
			err = json.NewDecoder(getResp.Body).Decode(&state)
			getResp.Body.Close()
			if err == nil {
				if state.Status == protocol.TaskStatusCompleted || state.Status == protocol.TaskStatusFailed || state.Status == protocol.TaskStatusCanceled {
					return state
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not reach a terminal state in time; last state: %+v", taskResp.TaskID, state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServer_RunnerFlag(t *testing.T) {
	t.Run("rejects an unknown runner value", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runCLI(context.Background(), []string{
			"server",
			"-runner", "telepathy",
			"-tasks-dir", t.TempDir(),
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected an error for an unknown -runner value, got nil")
		}
		if !strings.Contains(err.Error(), "runner") {
			t.Errorf("expected the error to mention the runner flag, got: %v", err)
		}
	})

	t.Run("simulated runner starts and shuts down", func(t *testing.T) {
		_, stop := startCoordinator(t, []string{
			"-tasks-dir", t.TempDir(),
			"--db-path", "none",
			"-runner", "simulated",
		})
		if err := stop(); err != nil {
			t.Fatalf("simulated runner coordinator failed on shutdown: %v", err)
		}
	})

	t.Run("pi runner executes tasks through the local pi binary", func(t *testing.T) {
		installFakePi(t, piStreamScript(`{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"mesh says "}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hello"}}
{"type":"agent_settled"}
`))

		addr, stop := startCoordinator(t, []string{
			"-tasks-dir", t.TempDir(),
			"--db-path", "none",
			"-runner", "pi",
		})
		defer func() {
			if err := stop(); err != nil {
				t.Fatalf("pi runner coordinator failed on shutdown: %v", err)
			}
		}()

		state := dispatchTaskOn(t, addr, protocol.TaskRequest{Agent: "worker", Task: "say hello"})
		if state.Status != protocol.TaskStatusCompleted {
			t.Fatalf("expected completed task, got %s (error: %+v)", state.Status, state.Error)
		}
		if state.Completion == nil || state.Completion.Result != "mesh says hello" {
			t.Fatalf("expected completion from the fake pi stream, got %+v", state.Completion)
		}
	})

	t.Run("pi runner marks the task failed when pi exits non-zero", func(t *testing.T) {
		installFakePi(t, "echo 'provider unreachable' >&2\nexit 7\n")

		addr, stop := startCoordinator(t, []string{
			"-tasks-dir", t.TempDir(),
			"--db-path", "none",
			"-runner", "pi",
		})
		defer func() {
			if err := stop(); err != nil {
				t.Fatalf("pi runner coordinator failed on shutdown: %v", err)
			}
		}()

		state := dispatchTaskOn(t, addr, protocol.TaskRequest{Agent: "worker", Task: "fail loudly"})
		if state.Status != protocol.TaskStatusFailed {
			t.Fatalf("expected failed task, got %s", state.Status)
		}
		if state.Error == nil || state.Error.Code != "PI_EXIT_ERROR" || !state.Error.Fatal {
			t.Fatalf("expected a fatal PI_EXIT_ERROR, got %+v", state.Error)
		}
		if !strings.Contains(state.Error.Message, "provider unreachable") {
			t.Errorf("expected stderr detail in the error message, got: %q", state.Error.Message)
		}
	})
}
