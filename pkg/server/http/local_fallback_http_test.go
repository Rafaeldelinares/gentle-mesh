package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	meshhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/registry"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/runner"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/store"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/task"
)

func boolPtr(b bool) *bool { return &b }

func newLFBServer(t *testing.T, lf *bool) (*httptest.Server, *registry.Registry) {
	t.Helper()
	reg := registry.NewRegistry(0) // 0 => heartbeatIsFresh always true
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 0,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
		LocalFallback:    lf,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, reg
}

func lfbPost(ts *httptest.Server, body string) string {
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&d)
	id, _ := d["task_id"].(string)
	return id
}

func lfbGet(ts *httptest.Server, id string) map[string]any {
	resp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + id)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var d map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&d)
	return d
}

func lfbWait(ts *httptest.Server, id, want string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d := lfbGet(ts, id)
		if s, _ := d["status"].(string); s == want {
			return d
		}
		time.Sleep(50 * time.Millisecond)
	}
	return lfbGet(ts, id)
}

// lfbNodeServer returns a worker endpoint that echoes back a completion event whose task_id
// matches the request body (so the test can assert worker_id and the SSE contents).
func lfbNodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TaskID string `json:"task_id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		completion, _ := protocol.NewEvent(1, req.TaskID, protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(completion.FormatSSE())
	}))
}

type lfbRecordingRunner struct{ called atomic.Int32 }

func (r *lfbRecordingRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.called.Add(1)
	_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"})
	return nil
}

// (a) LocalFallback nil, no node -> default must be FALSE: fails with DISPATCH_FAILED,
// no silent simulated execution on the coordinator.
func TestLocalFallbackNilNoNodeDefaultsToFalseAndFailsDispatch(t *testing.T) {
	ts, _ := newLFBServer(t, nil) // LocalFallback nil = default must be false
	id := lfbPost(ts, `{"agent":"worker","task":"default-fallback-test","git_repo":"toy/repo","git_branch":"a"}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "failed", 5*time.Second)
	e, _ := d["error"].(map[string]any)
	if code, _ := e["code"].(string); code != "DISPATCH_FAILED" {
		t.Errorf("expected code DISPATCH_FAILED when LocalFallback is nil (default false), got %v", e["code"])
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "no mesh worker available") {
		t.Errorf("expected message to contain 'no mesh worker available', got %q", msg)
	}
	if c, _ := d["completion"].(map[string]any); c != nil {
		t.Errorf("no simulated execution should have happened, got completion %v", c)
	}
}

// (a2) LocalFallback=true explicitly configured -> completes locally and emits warning.
func TestLocalFallbackTrueCompletesLocallyWithWarning(t *testing.T) {
	ts, _ := newLFBServer(t, boolPtr(true))
	id := lfbPost(ts, `{"agent":"worker","task":"explicit-fallback-test","git_repo":"toy/repo","git_branch":"a2"}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "completed", 5*time.Second)
	if s, _ := d["status"].(string); s != "completed" {
		t.Fatalf("expected completed, got %v", s)
	}
	c, _ := d["completion"].(map[string]any)
	if l, _ := c["executed_locally"].(bool); !l {
		t.Errorf("expected executed_locally=true, got %v", c["executed_locally"])
	}
}

// (b) LocalFallback=false, no node -> failed with DISPATCH_FAILED, message and SSE carry the
// text, and no simulated execution happened.
func TestLocalFallbackFalseNoNodeFailsDispatch(t *testing.T) {
	ts, _ := newLFBServer(t, boolPtr(false))
	id := lfbPost(ts, `{"agent":"worker","task":"b","git_repo":"toy/repo","git_branch":"b"}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "failed", 5*time.Second)
	e, _ := d["error"].(map[string]any)
	if code, _ := e["code"].(string); code != "DISPATCH_FAILED" {
		t.Errorf("expected code DISPATCH_FAILED, got %v", e["code"])
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "no mesh worker available") {
		t.Errorf("expected message to contain 'no mesh worker available', got %q", msg)
	}
	if c, _ := d["completion"].(map[string]any); c != nil {
		t.Errorf("no simulated execution should have happened, got completion %v", c)
	}
	// SSE: read /events with a bounded context (5 s) and a bounded buffer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/tasks/"+id+"/events", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	// SSE: read the whole stream (bounded by the request context of 5s) so a slow event
	// (e.g. the error event written by the scheduler after a retry) is not truncated by a
	// partial Read.
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "no mesh worker available") {
		t.Errorf("SSE stream should contain the error message, got %q", bodyStr)
	}
}

// (c) LocalFallback=false, max_retries=1, retry_delay_seconds=1, no node -> ends failed (not
// running) and the git_branch lock is released.
func TestLocalFallbackFalseMaxRetriesFailsAndLockFreed(t *testing.T) {
	ts, _ := newLFBServer(t, boolPtr(false))
	id := lfbPost(ts, `{"agent":"worker","task":"c","git_repo":"toy/repo","git_branch":"c","max_retries":1,"retry_delay_seconds":1}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "failed", 10*time.Second)
	if s, _ := d["status"].(string); s != "failed" {
		t.Fatalf("expected failed, got %v (task stuck in running?)", s)
	}
	// Lock free: another task with the same git_branch must be accepted.
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(`{"agent":"worker","task":"c-bis","git_repo":"toy/repo","git_branch":"c"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("lock should be free: expected 201, got %d", resp.StatusCode)
	}
}

// (d) LocalFallback=false, an eligible node registered -> the task runs in it, with worker_id
// and without a local marker.
func TestLocalFallbackFalseEligibleNodeDispatches(t *testing.T) {
	ts, reg := newLFBServer(t, boolPtr(false))
	node := lfbNodeServer(t)
	if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "n1",
		Endpoint:       node.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	id := lfbPost(ts, `{"agent":"worker","task":"d","git_repo":"toy/repo","git_branch":"d"}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "completed", 5*time.Second)
	if w, _ := d["worker_id"].(string); w != "n1" {
		t.Errorf("expected worker_id=n1, got %v", d["worker_id"])
	}
	c, _ := d["completion"].(map[string]any)
	if l, _ := c["executed_locally"].(bool); l {
		t.Errorf("expected no local marker, got completion %v", c)
	}
}

// (e) LocalFallback=false with a non-nil Runner -> the flag is ignored, the provided Runner runs.
func TestLocalFallbackFalseIgnoredWhenRunnerProvided(t *testing.T) {
	t.Helper()
	rr := &lfbRecordingRunner{}
	reg := registry.NewRegistry(0)
	mgr, _ := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	srv, _ := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 0,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
		Runner:           rr,
		LocalFallback:    boolPtr(false),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	id := lfbPost(ts, `{"agent":"worker","task":"e","git_repo":"toy/repo","git_branch":"e"}`)
	if id == "" {
		t.Fatal("POST failed")
	}
	d := lfbWait(ts, id, "completed", 5*time.Second)
	if s, _ := d["status"].(string); s != "completed" {
		t.Fatalf("expected completed, got %v", s)
	}
	if rr.called.Load() == 0 {
		t.Error("the provided Runner was not called: the flag should have been ignored")
	}
	c, _ := d["completion"].(map[string]any)
	if l, _ := c["executed_locally"].(bool); l {
		t.Errorf("expected no local marker, got %v", c)
	}
}

func lfb429NodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "node queue full"})
	}))
}

// (f) When 2 nodes return 429 and -local-fallback is active (default, nil),
// the task must fail with DISPATCH_FAILED and must NOT complete via simulated local fallback.
func TestLocalFallback_AllNodes429FailsWithoutLocalFallback(t *testing.T) {
	ts, reg := newLFBServer(t, nil) // LocalFallback nil = default active
	node1 := lfb429NodeServer(t)
	defer node1.Close()
	node2 := lfb429NodeServer(t)
	defer node2.Close()

	_, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "n1",
		Endpoint:       node1.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("register n1: %v", err)
	}
	_, err = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "n2",
		Endpoint:       node2.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("register n2: %v", err)
	}

	id := lfbPost(ts, `{"agent":"worker","task":"all-busy-test","git_repo":"toy/repo","git_branch":"b1"}`)
	if id == "" {
		t.Fatal("POST failed")
	}

	d := lfbWait(ts, id, "failed", 5*time.Second)
	status, _ := d["status"].(string)
	if status != "failed" {
		c, _ := d["completion"].(map[string]any)
		loc, _ := c["executed_locally"].(bool)
		t.Fatalf("expected task to fail with DISPATCH_FAILED without local fallback, got status=%s, executed_locally=%v", status, loc)
	}

	errObj, _ := d["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != "DISPATCH_FAILED" {
		t.Fatalf("expected error code DISPATCH_FAILED, got %v", code)
	}
	c, _ := d["completion"].(map[string]any)
	if loc, _ := c["executed_locally"].(bool); loc {
		t.Fatalf("expected executed_locally=false, got %v", loc)
	}
}

// (g) With max_retries=1, when candidate node returns 429 during initial dispatch attempts
// but frees up before the coordinator retry, the task completes on that node exactly once.
func TestLocalFallback_429WithMaxRetriesSucceedsWhenNodeFreed(t *testing.T) {
	ts, reg := newLFBServer(t, nil) // LocalFallback nil = default active
	var attempts atomic.Int32
	var completions atomic.Int32

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := attempts.Add(1)
		if att <= 3 { // Mesh runner exhausts its 3 attempts on the first dispatch
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "node busy"})
			return
		}
		// On retry attempt after delay, node is freed and executes task
		completions.Add(1)
		var req struct {
			TaskID string `json:"task_id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		completion, _ := protocol.NewEvent(1, req.TaskID, protocol.EventCompletion, protocol.CompletionPayload{Result: "success on node"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(completion.FormatSSE())
	}))
	defer node.Close()

	_, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "n1",
		Endpoint:       node.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	id := lfbPost(ts, `{"agent":"worker","task":"retry-task","git_repo":"toy/repo","git_branch":"b2","max_retries":1,"retry_delay_seconds":1}`)
	if id == "" {
		t.Fatal("POST failed")
	}

	d := lfbWait(ts, id, "completed", 10*time.Second)
	status, _ := d["status"].(string)
	if status != "completed" {
		t.Fatalf("expected status completed after retry, got %s: %+v", status, d)
	}
	if comp := completions.Load(); comp != 1 {
		t.Fatalf("expected exactly 1 execution on node, got %d", comp)
	}
	c, _ := d["completion"].(map[string]any)
	if loc, _ := c["executed_locally"].(bool); loc {
		t.Fatalf("expected executed_locally=false, got %v", loc)
	}
}
