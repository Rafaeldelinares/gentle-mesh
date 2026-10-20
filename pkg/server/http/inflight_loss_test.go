package http_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
)

func newInflightServer(t *testing.T, heartbeat time.Duration) (*meshhttp.Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{TasksDir: dir, HeartbeatTimeout: heartbeat, TaskTTL: time.Hour})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Shutdown(context.Background()) })
	return srv, ts, filepath.Join(dir, "gentle-mesh.db")
}

// joinInflightNode registers a node that answers /v1/execute with the given SSE body and then closes
// the connection, which is exactly what a node that accepts a task and vanishes does.
func joinInflightNode(t *testing.T, ts *httptest.Server, nodeID string, sse string) {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execute" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(node.Close)
	raw, _ := json.Marshal(map[string]any{"node_id": nodeID, "endpoint": node.URL, "hardware": map[string]any{},
		"agents": []map[string]any{{"name": "worker"}}, "max_concurrency": 4})
	resp, err := ts.Client().Post(ts.URL+"/v1/mesh/join", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join status %d", resp.StatusCode)
	}
}

func inflightPost(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var tr protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tr.TaskID
}

func inflightWait(t *testing.T, ts *httptest.Server, taskID string, want protocol.TaskStatus) protocol.TaskState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var st protocol.TaskState
	for time.Now().Before(deadline) {
		resp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + taskID)
		if err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			if st.Status == want {
				return st
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %s (last %s, error %+v)", taskID, want, st.Status, st.Error)
	return st
}

func inflightStream(t *testing.T, ts *httptest.Server, taskID string) string {
	t.Helper()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(ts.URL + "/v1/tasks/" + taskID + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

func inflightErrMsg(t *testing.T, dbPath, taskID string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	defer db.Close()
	var msg string
	if err := db.QueryRow("select error_message from tasks where task_id = ?", taskID).Scan(&msg); err != nil {
		t.Fatalf("select: %v", err)
	}
	return msg
}

// The stream of a node ends without a terminal event: the task must be finalized with NODE_LOST, the
// cause must reach the state, the SSE stream and the store, and the territory must be released.
func TestServerStreamWithoutTerminalFinalizesTask(t *testing.T) {
	_, ts, dbPath := newInflightServer(t, 5*time.Second)
	joinInflightNode(t, ts, "node-gone", "event: status\ndata: {\"id\":1,\"task_id\":\"t\",\"timestamp\":0,\"type\":\"status\",\"payload\":{\"status\":\"running\"}}\n\n")

	id := inflightPost(t, ts, `{"agent":"worker","task":"lost stream","git_repo":"acme/mesh","git_branch":"feature/x"}`)
	st := inflightWait(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "NODE_LOST" {
		t.Fatalf("state error = %+v, want NODE_LOST", st.Error)
	}
	if stream := inflightStream(t, ts, id); !strings.Contains(stream, "NODE_LOST") {
		t.Errorf("the SSE stream must carry NODE_LOST, got %s", stream)
	}
	if msg := inflightErrMsg(t, dbPath, id); !strings.Contains(msg, "node lost") {
		t.Errorf("stored error_message = %q, want the node lost cause", msg)
	}
	// The territory must be free again: a second task on the same repo/branch is accepted.
	second := inflightPost(t, ts, `{"agent":"worker","task":"after the loss","git_repo":"acme/mesh","git_branch":"feature/x"}`)
	if second == "" {
		t.Fatal("the task after the loss was not accepted: the territory was not released")
	}
}
