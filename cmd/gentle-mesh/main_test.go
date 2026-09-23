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
