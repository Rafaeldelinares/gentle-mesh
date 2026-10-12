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

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
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

// (a) LocalFallback nil, no node -> completed + executed_locally=true (current behaviour).
func TestLocalFallbackNilNoNodeCompletesLocally(t *testing.T) {
	ts, _ := newLFBServer(t, nil)
	id := lfbPost(ts, `{"agent":"worker","task":"a","git_repo":"toy/repo","git_branch":"a"}`)
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
