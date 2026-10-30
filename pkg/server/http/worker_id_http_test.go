package http_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	meshhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
)

func newWhoServerWithFallback(t *testing.T, fallback bool) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         dir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		LocalFallback:    &fallback,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Shutdown(context.Background())
	})
	return ts, filepath.Join(dir, "gentle-mesh.db")
}

func newWhoServer(t *testing.T) (*httptest.Server, string) {
	return newWhoServerWithFallback(t, false)
}

// joinFakeNode starts a mesh node that joins the coordinator and answers every task with the SSE
// envelope the protocol expects: data carries a full protocol.Event whose payload is the result or
// the failure the node reports.
func joinFakeNode(t *testing.T, ts *httptest.Server, nodeID, eventType, payload string) *httptest.Server {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execute" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: %s\ndata: {\"id\":1,\"task_id\":\"fake\",\"timestamp\":0,\"type\":\"%s\",\"payload\":%s}\n\n", eventType, eventType, payload)
	}))
	t.Cleanup(node.Close)

	join := map[string]any{
		"node_id": nodeID, "endpoint": node.URL, "hardware": map[string]any{},
		"agents": []map[string]any{{"name": "worker"}}, "max_concurrency": 4,
	}
	raw, err := json.Marshal(join)
	if err != nil {
		t.Fatalf("marshal join: %v", err)
	}
	resp, err := ts.Client().Post(ts.URL+"/v1/mesh/join", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST /v1/mesh/join failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("join status %d: %s", resp.StatusCode, body)
	}
	return node
}

func postWhoTask(t *testing.T, ts *httptest.Server, body string) protocol.TaskResponse {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var tr protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	return tr
}

func waitWhoState(t *testing.T, ts *httptest.Server, taskID string, want protocol.TaskStatus) protocol.TaskState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var state protocol.TaskState
	for time.Now().Before(deadline) {
		resp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + taskID)
		if err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&state)
			resp.Body.Close()
			if state.Status == want {
				return state
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %s (last %s, error %+v)", taskID, want, state.Status, state.Error)
	return state
}

func whoDBRow(t *testing.T, dbPath, taskID string) (string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var workerID, errMsg string
	if err := db.QueryRow("select worker_id, error_message from tasks where task_id = ?", taskID).Scan(&workerID, &errMsg); err != nil {
		t.Fatalf("select task row: %v", err)
	}
	return workerID, errMsg
}

// waitWhoError polls the task row until error_message matches want. The API can report the task
// as failed before the store has flushed the error_message, so reading it immediately races the
// synchronous store write that happens right after the status transition is published.
func waitWhoError(t *testing.T, dbPath, taskID, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var errMsg string
	for time.Now().Before(deadline) {
		_, errMsg = whoDBRow(t, dbPath, taskID)
		if errMsg == want {
			return errMsg
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errMsg
}

func whoTaskEvents(t *testing.T, ts *httptest.Server, taskID string) string {
	t.Helper()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(ts.URL + "/v1/tasks/" + taskID + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body) // the stream stays open: a read deadline ends it
	return string(raw)
}

// TestServerTaskStateAndStoreRecordTheWorkerNode pins finding 20: a task dispatched to a mesh node
// carries that node in the state and in the store, and an unattributed task carries neither.
func TestServerTaskStateAndStoreRecordTheWorkerNode(t *testing.T) {
	ts, dbPath := newWhoServer(t)
	joinFakeNode(t, ts, "node-alpha", "completion", `{"result":"ok"}`)

	tr := postWhoTask(t, ts, `{"agent":"worker","task":"who ran this"}`)
	state := waitWhoState(t, ts, tr.TaskID, protocol.TaskStatusCompleted)

	if state.WorkerID != "node-alpha" {
		t.Errorf("state worker_id = %q, want node-alpha", state.WorkerID)
	}
	if workerID, _ := whoDBRow(t, dbPath, tr.TaskID); workerID != "node-alpha" {
		t.Errorf("stored worker_id = %q, want node-alpha", workerID)
	}

	// The local fallback (no nodes at all) must never invent a node.
	ts2, dbPath2 := newWhoServerWithFallback(t, true)
	tr2 := postWhoTask(t, ts2, `{"agent":"worker","task":"nobody around"}`)
	state2 := waitWhoState(t, ts2, tr2.TaskID, protocol.TaskStatusCompleted)
	if state2.WorkerID != "" {
		t.Errorf("a locally executed task must not carry a worker id, got %q", state2.WorkerID)
	}
	raw, err := json.Marshal(state2)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if strings.Contains(string(raw), "worker_id") {
		t.Errorf("an unattributed task must keep the exact JSON shape: %s", raw)
	}
	if workerID, _ := whoDBRow(t, dbPath2, tr2.TaskID); workerID != "" {
		t.Errorf("stored worker_id = %q, want empty", workerID)
	}
}

// TestServerNodeFailureCauseReachesStateStreamAndStore is the regression for the corrected finding
// 22: the code and message a node reports in its error event land in the state, the SSE stream and
// the store, and the cause is not replaced afterwards.
func TestServerNodeFailureCauseReachesStateStreamAndStore(t *testing.T) {
	ts, dbPath := newWhoServer(t)
	joinFakeNode(t, ts, "node-broken", "error", `{"code":"NODE_FAILED","message":"the node refused to work"}`)

	tr := postWhoTask(t, ts, `{"agent":"worker","task":"who failed"}`)
	state := waitWhoState(t, ts, tr.TaskID, protocol.TaskStatusFailed)

	if state.Error == nil {
		t.Fatalf("expected the node cause in the state, got %+v", state)
	}
	if state.Error.Code != "NODE_FAILED" || state.Error.Message != "the node refused to work" {
		t.Errorf("state error = %+v, want NODE_FAILED/the node refused to work", state.Error)
	}
	if workerID, _ := whoDBRow(t, dbPath, tr.TaskID); workerID != "node-broken" {
		t.Errorf("stored worker_id = %q, want node-broken", workerID)
	}
	if errMsg := waitWhoError(t, dbPath, tr.TaskID, "the node refused to work"); errMsg != "the node refused to work" {
		t.Errorf("stored error_message = %q, want the node message", errMsg)
	}
	stream := whoTaskEvents(t, ts, tr.TaskID)
	if !strings.Contains(stream, "NODE_FAILED") || !strings.Contains(stream, "the node refused to work") {
		t.Errorf("the SSE stream must carry the node cause, got %s", stream)
	}
}
