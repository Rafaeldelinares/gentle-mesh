package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// (a) TestNodeToken_AllowsJoinAndRejectsTasks
// Demonstrates that with -node-token the worker can join the coordinator,
// but using that same token to call POST /v1/tasks returns 403 Forbidden.
// Fails on base because -node-token flag does not exist.
func TestNodeToken_AllowsJoinAndRejectsTasks(t *testing.T) {
	adminToken := "admin-secret-token"
	nodeToken := "node-scoped-token"

	addr, stop := startCoordinator(t, []string{
		"-token", adminToken,
		"-node-token", nodeToken,
		"-insecure-no-auth",
	})
	defer stop()

	coordURL := "http://" + addr

	// 1. POST /v1/mesh/join with nodeToken must succeed (200)
	joinBody := `{"node_id":"n1","endpoint":"http://127.0.0.1:9999","hardware":{"cpus":2,"ram_gb":4},"agents":[{"name":"worker"}]}`
	reqJoin, err := http.NewRequest(http.MethodPost, coordURL+"/v1/mesh/join", strings.NewReader(joinBody))
	if err != nil {
		t.Fatalf("failed to create join request: %v", err)
	}
	reqJoin.Header.Set("Content-Type", "application/json")
	reqJoin.Header.Set("Authorization", "Bearer "+nodeToken)

	client := &http.Client{Timeout: 5 * time.Second}
	respJoin, err := client.Do(reqJoin)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	defer respJoin.Body.Close()
	if respJoin.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respJoin.Body)
		t.Fatalf("expected node-token join to return 200, got %d: %s", respJoin.StatusCode, string(body))
	}

	// 2. POST /v1/tasks with nodeToken must return 403 Forbidden
	taskBody := `{"agent":"worker","task":"echo hello"}`
	reqTask, err := http.NewRequest(http.MethodPost, coordURL+"/v1/tasks", strings.NewReader(taskBody))
	if err != nil {
		t.Fatalf("failed to create task request: %v", err)
	}
	reqTask.Header.Set("Content-Type", "application/json")
	reqTask.Header.Set("Authorization", "Bearer "+nodeToken)

	respTask, err := client.Do(reqTask)
	if err != nil {
		t.Fatalf("task request failed: %v", err)
	}
	defer respTask.Body.Close()
	if respTask.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(respTask.Body)
		t.Fatalf("expected node-token task create to return 403 Forbidden, got %d: %s", respTask.StatusCode, string(body))
	}
}

// (b) TestDispatchToken_CoordinatorDispatchesWithDispatchToken
// Demonstrates that when -dispatch-token is configured, coordinator dispatches tasks
// to remote workers using the dispatch token (not the admin token).
// Fails on base because -dispatch-token flag does not exist or dispatches with admin token.
func TestDispatchToken_CoordinatorDispatchesWithDispatchToken(t *testing.T) {
	adminToken := "admin-secret-token"
	dispatchToken := "dispatch-secret-token"

	var receivedAuth string
	var authMu sync.Mutex

	// Mock worker server that captures Authorization header on /v1/execute
	workerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/execute" {
			authMu.Lock()
			receivedAuth = r.Header.Get("Authorization")
			authMu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: status\ndata: {\"status\":\"running\"}\n\n"))
			_, _ = w.Write([]byte("event: completion\ndata: {\"result\":\"done\"}\n\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer workerSrv.Close()

	addr, stop := startCoordinator(t, []string{
		"-token", adminToken,
		"-dispatch-token", dispatchToken,
		"-insecure-no-auth",
	})
	defer stop()

	coordURL := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}

	// Register worker node directly into coordinator
	joinBody := fmt.Sprintf(`{"node_id":"w1","endpoint":%q,"hardware":{"cpus":2,"ram_gb":4},"agents":[{"name":"worker"}]}`, workerSrv.URL)
	reqJoin, err := http.NewRequest(http.MethodPost, coordURL+"/v1/mesh/join", strings.NewReader(joinBody))
	if err != nil {
		t.Fatalf("failed to create join request: %v", err)
	}
	reqJoin.Header.Set("Content-Type", "application/json")
	reqJoin.Header.Set("Authorization", "Bearer "+adminToken)

	respJoin, err := client.Do(reqJoin)
	if err != nil {
		t.Fatalf("join request failed: %v", err)
	}
	defer respJoin.Body.Close()
	if respJoin.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respJoin.Body)
		t.Fatalf("join failed: %d: %s", respJoin.StatusCode, string(body))
	}

	// Dispatch task with admin token
	taskBody := `{"agent":"worker","task":"remote task"}`
	reqTask, err := http.NewRequest(http.MethodPost, coordURL+"/v1/tasks", strings.NewReader(taskBody))
	if err != nil {
		t.Fatalf("failed to build task request: %v", err)
	}
	reqTask.Header.Set("Content-Type", "application/json")
	reqTask.Header.Set("Authorization", "Bearer "+adminToken)

	respTask, err := client.Do(reqTask)
	if err != nil {
		t.Fatalf("task request failed: %v", err)
	}
	defer respTask.Body.Close()
	if respTask.StatusCode != http.StatusCreated && respTask.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respTask.Body)
		t.Fatalf("expected task created, got %d: %s", respTask.StatusCode, string(body))
	}

	// Wait up to 5s for the worker to receive the dispatch
	deadline := time.Now().Add(5 * time.Second)
	var gotAuth string
	for time.Now().Before(deadline) {
		authMu.Lock()
		gotAuth = receivedAuth
		authMu.Unlock()
		if gotAuth != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	expectedAuth := "Bearer " + dispatchToken
	if gotAuth != expectedAuth {
		t.Fatalf("expected worker to receive dispatch token %q, got %q", expectedAuth, gotAuth)
	}
}

// (c) TestWorker_AcceptCoordinatorTokenFalseRejectsAdmin
// Demonstrates that with -accept-coordinator-token=false the worker rejects
// the coordinator admin token on /v1/execute with 401 Unauthorized, while
// accepting the worker token.
// Fails on base because -accept-coordinator-token flag does not exist or worker accepts admin token.
func TestWorker_AcceptCoordinatorTokenFalseRejectsAdmin(t *testing.T) {
	adminToken := "admin-secret-token"
	workerToken := "worker-secret-token"

	// Mock coordinator for join
	coordMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"node_id":"w-test","status":"online"}`))
	}))
	defer coordMock.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdout := &syncBuffer{}
	var stderr bytes.Buffer
	done := make(chan error, 1)

	go func() {
		done <- runCLI(ctx, []string{
			"worker",
			"-coordinator", coordMock.URL,
			"-node-id", "w-test",
			"-endpoint", "http://0.0.0.0:0",
			"-addr", "0.0.0.0:0",
			"-heartbeat-interval", "20ms",
			"-token", adminToken,
			"-worker-token", workerToken,
			"-accept-coordinator-token=false",
		}, stdout, &stderr)
	}()

	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`Worker HTTP server started on (\S+)`).FindStringSubmatch(stdout.String()); m != nil {
			addr = m[1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("worker did not start; stdout: %s stderr: %s", stdout.String(), stderr.String())
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("cannot split worker address %q: %v", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	execURL := "http://" + net.JoinHostPort(host, port) + "/v1/execute"

	probe := func(token string) int {
		req, err := http.NewRequest(http.MethodPost, execURL, strings.NewReader(`{"agent":"worker","task":"probe"}`))
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 1. Request with adminToken must be rejected with 401 Unauthorized
	statusAdmin := probe(adminToken)
	if statusAdmin != http.StatusUnauthorized {
		t.Fatalf("expected admin token to be rejected with 401, got %d", statusAdmin)
	}

	// 2. Request with workerToken must be accepted (200 OK)
	statusWorker := probe(workerToken)
	if statusWorker != http.StatusOK {
		t.Fatalf("expected worker token to be accepted with 200, got %d", statusWorker)
	}
}

// (d) TestCoordinator_StartupWarningsWithoutDispatchOrNodeToken
// Verifies that single-line warnings are printed on coordinator startup when
// -dispatch-token or -node-token are missing, without printing any token value.
// Fails on base because flags and warnings do not exist.
func TestCoordinator_StartupWarningsWithoutDispatchOrNodeToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	errCh := make(chan error, 1)

	go func() {
		errCh <- runCLI(ctx, []string{
			"server",
			"-addr", "127.0.0.1:0",
			"-token", "super-secret-admin-token",
		}, stdout, stderr)
	}()

	// Wait for coordinator to output startup messages
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "coordinator starting") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	_ = <-errCh

	out := stdout.String()
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "-dispatch-token") {
		t.Fatalf("expected single-line warning for missing -dispatch-token; got:\n%s", out)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "-node-token") {
		t.Fatalf("expected single-line warning for missing -node-token; got:\n%s", out)
	}
	if strings.Contains(out, "super-secret-admin-token") {
		t.Fatalf("warning must not leak token values; got:\n%s", out)
	}
}
