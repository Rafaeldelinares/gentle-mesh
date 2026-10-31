package joinlog_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/joinlog"
)

func setupTestServer(t *testing.T) (*joinlog.Log, *joinlog.Projector, *httptest.Server) {
	t.Helper()
	tempDir := t.TempDir()

	l, err := joinlog.New(filepath.Join(tempDir, "http_log.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	p, err := joinlog.NewProjector(filepath.Join(tempDir, "http_proj.db"))
	if err != nil {
		t.Fatalf("open projector: %v", err)
	}

	h := joinlog.NewHTTPHandler(l, p)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		_ = p.Close()
		_ = l.Close()
	})

	return l, p, ts
}

func appendTestEvent(t *testing.T, l *joinlog.Log, p *joinlog.Projector, eventType, actor string, payload any) joinlog.Event {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	evt := joinlog.Event{
		Type:      eventType,
		Actor:     actor,
		Payload:   data,
		CreatedAt: time.Now().UTC(),
	}
	if err := l.Append(context.Background(), evt); err != nil {
		t.Fatalf("append event: %v", err)
	}
	headSeq, headHash, err := l.HeadPosition(context.Background())
	if err != nil {
		t.Fatalf("get head position: %v", err)
	}
	evt.Seq = headSeq
	evt.Hash = headHash
	if p != nil {
		if err := p.ProjectEvent(context.Background(), evt); err != nil {
			t.Fatalf("project event: %v", err)
		}
	}
	return evt
}

// 1. Test GET /v1/mesh/snapshot
func TestHTTPSnapshot_Success(t *testing.T) {
	l, p, ts := setupTestServer(t)

	appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "node-1", joinlog.NodeJoinedPayload{
		NodeID:   "node-1",
		Role:     "worker",
		Endpoint: "https://10.0.0.1:8631",
	})
	appendTestEvent(t, l, p, joinlog.EventTypeTaskCreated, "node-1", joinlog.TaskCreatedPayload{
		TaskID:    "task-http-1",
		GitRepo:   "https://github.com/org/repo-http",
		GitBranch: "main",
		Prompt:    "Test snapshot HTTP",
	})

	resp, err := http.Get(ts.URL + "/v1/mesh/snapshot")
	if err != nil {
		t.Fatalf("GET /v1/mesh/snapshot failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var snap joinlog.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot json: %v", err)
	}

	if snap.HeadEventSeq != 2 {
		t.Fatalf("expected HeadEventSeq=2, got %d", snap.HeadEventSeq)
	}
	if snap.HeadEventHash == "" {
		t.Fatalf("expected non-empty HeadEventHash")
	}
	if len(snap.State.Nodes) != 1 || len(snap.State.Tasks) != 1 {
		t.Fatalf("unexpected state content: %+v", snap.State)
	}

	valid, err := joinlog.VerifySnapshot(&snap)
	if err != nil || !valid {
		t.Fatalf("snapshot verification failed: valid=%v, err=%v", valid, err)
	}
}

// 2. Test 409 Conflict when since_hash diverges from actual chain at since_seq
func TestHTTPEvents_ChainDivergenceReturns409(t *testing.T) {
	l, p, ts := setupTestServer(t)

	appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "node-1", joinlog.NodeJoinedPayload{
		NodeID:   "node-1",
		Role:     "worker",
		Endpoint: "https://10.0.0.1:8631",
	})
	appendTestEvent(t, l, p, joinlog.EventTypeTaskCreated, "node-1", joinlog.TaskCreatedPayload{
		TaskID:    "task-2",
		GitRepo:   "https://github.com/org/repo",
		GitBranch: "main",
		Prompt:    "Task 2",
	})

	// Scenario A: Client reconnects with composite Last-Event-ID header where hash is wrong
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "2:bogus-hash-that-diverges-completely")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict on chain divergence, got %d", resp.StatusCode)
	}

	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "chain divergence detected") {
		t.Fatalf("expected 'chain divergence detected' in error body, got: %v", errBody)
	}

	// Scenario B: Client reconnects with query params since_seq=2&since_hash=wrong
	resp2, err := http.Get(ts.URL + "/v1/mesh/events?since_seq=2&since_hash=bogus-hash-again")
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict via query param, got %d", resp2.StatusCode)
	}
}

// 3. Test when client requests since_seq > head: stream stays open and waits (no error)
func TestHTTPEvents_SinceSeqGreaterThanHeadWaits(t *testing.T) {
	l, p, ts := setupTestServer(t)

	// Append only 1 event (head seq = 1)
	appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "node-1", joinlog.NodeJoinedPayload{
		NodeID:   "node-1",
		Role:     "worker",
		Endpoint: "https://10.0.0.1:8631",
	})

	// Client requests since_seq = 5 (ahead of current head)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/mesh/events?since_seq=5", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Must be 200 OK with text/event-stream, NOT an error
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Reader should block waiting since there are no events > 5
	reader := bufio.NewReader(resp.Body)
	lineChan := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		lineChan <- line
	}()

	select {
	case line := <-lineChan:
		t.Fatalf("expected stream to wait without sending events > 5, but got line: %q", line)
	case <-time.After(150 * time.Millisecond):
		// Expected: stream is happily waiting
	}
}

// 4. Test normal stream with historical playback + live events
func TestHTTPEvents_StreamAndResume(t *testing.T) {
	l, p, ts := setupTestServer(t)

	evt1 := appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "node-1", joinlog.NodeJoinedPayload{
		NodeID:   "node-1",
		Role:     "worker",
		Endpoint: "https://10.0.0.1:8631",
	})
	evt2 := appendTestEvent(t, l, p, joinlog.EventTypeTaskCreated, "node-1", joinlog.TaskCreatedPayload{
		TaskID:    "task-stream-1",
		GitRepo:   "https://github.com/org/repo-stream",
		GitBranch: "main",
		Prompt:    "Test stream",
	})

	// Client connects requesting since_seq = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/mesh/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)

	// Helper to read one SSE frame
	readEventFrame := func() (id, eventType, data string) {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read stream: %v", err)
			}
			line = strings.TrimSpace(line)
			if line == "" {
				if id != "" || eventType != "" || data != "" {
					return id, eventType, data
				}
				continue
			}
			if strings.HasPrefix(line, "id: ") {
				id = strings.TrimPrefix(line, "id: ")
			} else if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
	}

	// 1. Read first historical event
	id1, type1, _ := readEventFrame()
	expectedID1 := fmt.Sprintf("1:%s", evt1.Hash)
	if id1 != expectedID1 || type1 != joinlog.EventTypeNodeJoined {
		t.Fatalf("expected id=%q type=%q, got id=%q type=%q", expectedID1, joinlog.EventTypeNodeJoined, id1, type1)
	}

	// 2. Read second historical event
	id2, type2, _ := readEventFrame()
	expectedID2 := fmt.Sprintf("2:%s", evt2.Hash)
	if id2 != expectedID2 || type2 != joinlog.EventTypeTaskCreated {
		t.Fatalf("expected id=%q type=%q, got id=%q type=%q", expectedID2, joinlog.EventTypeTaskCreated, id2, type2)
	}

	// 3. Emit live event while client is connected
	evt3 := appendTestEvent(t, l, p, joinlog.EventTypeTerritoryClaimed, "task-stream-1", joinlog.TerritoryClaimedPayload{
		TaskID:    "task-stream-1",
		GitRepo:   "https://github.com/org/repo-stream",
		GitBranch: "main",
	})

	id3, type3, _ := readEventFrame()
	expectedID3 := fmt.Sprintf("3:%s", evt3.Hash)
	if id3 != expectedID3 || type3 != joinlog.EventTypeTerritoryClaimed {
		t.Fatalf("expected live id=%q type=%q, got id=%q type=%q", expectedID3, joinlog.EventTypeTerritoryClaimed, id3, type3)
	}
}
