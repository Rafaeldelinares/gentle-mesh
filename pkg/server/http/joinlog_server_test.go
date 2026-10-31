package http_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/joinlog"
	meshhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
)

// TestServer_JoinLog_ProductionEndpoints verifies that GET /v1/mesh/snapshot and
// GET /v1/mesh/events are mounted on the real production HTTP coordinator server
// with all production middleware (auth, host validation, panic recovery).
func TestServer_JoinLog_ProductionEndpoints(t *testing.T) {
	tempDir := t.TempDir()

	const adminToken = "admin-secret-token"
	const nodeToken = "node-secret-token"

	cfg := meshhttp.ServerConfig{
		TasksDir:    tempDir,
		BearerToken: adminToken,
		NodeToken:   nodeToken,
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create production server: %v", err)
	}
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()

	if srv.JoinLog() == nil || srv.JoinProjector() == nil {
		t.Fatalf("expected JoinLog and JoinProjector to be initialized on production server")
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Verify Authentication on /v1/mesh/snapshot
	// Without token -> 401 Unauthorized
	unauthResp, err := http.Get(ts.URL + "/v1/mesh/snapshot")
	if err != nil {
		t.Fatalf("GET /v1/mesh/snapshot unauthenticated failed: %v", err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without token, got %d", unauthResp.StatusCode)
	}

	// With Node token -> 200 OK (nodes need snapshot to join!)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+nodeToken)
	nodeResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/mesh/snapshot with node token failed: %v", err)
	}
	defer nodeResp.Body.Close()
	if nodeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with node token, got %d", nodeResp.StatusCode)
	}

	// 2. Append event to server's join log and verify snapshot content
	evtPayload, _ := json.Marshal(joinlog.NodeJoinedPayload{
		NodeID:   "worker-alpha",
		Role:     "worker",
		Endpoint: "https://10.0.0.5:8631",
	})
	evt1 := joinlog.Event{
		Type:      joinlog.EventTypeNodeJoined,
		Actor:     "worker-alpha",
		Payload:   evtPayload,
		CreatedAt: time.Now().UTC(),
	}
	if err := srv.JoinLog().Append(context.Background(), evt1); err != nil {
		t.Fatalf("append event to srv.JoinLog(): %v", err)
	}
	if err := srv.JoinProjector().ProjectEvent(context.Background(), evt1); err != nil {
		t.Fatalf("project event to srv.JoinProjector(): %v", err)
	}

	// Fetch snapshot with admin token
	reqAdmin, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/snapshot", nil)
	reqAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	adminResp, err := http.DefaultClient.Do(reqAdmin)
	if err != nil {
		t.Fatalf("GET /v1/mesh/snapshot with admin token failed: %v", err)
	}
	defer adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with admin token, got %d", adminResp.StatusCode)
	}

	var snap joinlog.Snapshot
	if err := json.NewDecoder(adminResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.HeadEventSeq != 1 {
		t.Fatalf("expected HeadEventSeq=1, got %d", snap.HeadEventSeq)
	}
	valid, err := joinlog.VerifySnapshot(&snap)
	if err != nil || !valid {
		t.Fatalf("VerifySnapshot failed: valid=%v, err=%v", valid, err)
	}

	// 3. Verify Chain Divergence 409 Conflict on /v1/mesh/events
	reqDiv, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/events", nil)
	reqDiv.Header.Set("Authorization", "Bearer "+adminToken)
	reqDiv.Header.Set("Last-Event-ID", "1:wrong-chain-divergent-hash")
	divResp, err := http.DefaultClient.Do(reqDiv)
	if err != nil {
		t.Fatalf("divergence request failed: %v", err)
	}
	defer divResp.Body.Close()
	if divResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on chain divergence, got %d", divResp.StatusCode)
	}

	// 4. Verify SSE stream subscription on /v1/mesh/events
	ctxStream, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	reqStream, _ := http.NewRequestWithContext(ctxStream, http.MethodGet, ts.URL+"/v1/mesh/events", nil)
	reqStream.Header.Set("Authorization", "Bearer "+adminToken)
	streamResp, err := http.DefaultClient.Do(reqStream)
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for stream, got %d", streamResp.StatusCode)
	}
	if ct := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(streamResp.Body)

	// Read event 1 (historical replay)
	var readID, readEvent, readData string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if readID != "" {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "id: ") {
			readID = strings.TrimPrefix(line, "id: ")
		} else if strings.HasPrefix(line, "event: ") {
			readEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			readData = strings.TrimPrefix(line, "data: ")
		}
	}

	if !strings.HasPrefix(readID, "1:") || readEvent != joinlog.EventTypeNodeJoined {
		t.Fatalf("unexpected historical frame: id=%q event=%q data=%q", readID, readEvent, readData)
	}

	// Emit live event 2 and verify delivery
	taskPayload, _ := json.Marshal(joinlog.TaskCreatedPayload{
		TaskID:    "prod-task-1",
		GitRepo:   "https://github.com/org/prod-repo",
		GitBranch: "main",
		Prompt:    "Test production stream delivery",
	})
	evt2 := joinlog.Event{
		Type:      joinlog.EventTypeTaskCreated,
		Actor:     "worker-alpha",
		Payload:   taskPayload,
		CreatedAt: time.Now().UTC(),
	}
	if err := srv.JoinLog().Append(context.Background(), evt2); err != nil {
		t.Fatalf("append live event: %v", err)
	}

	// Read live event 2 from the open SSE connection
	var liveID, liveEvent, liveData string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read live stream: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if liveID != "" {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "id: ") {
			liveID = strings.TrimPrefix(line, "id: ")
		} else if strings.HasPrefix(line, "event: ") {
			liveEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			liveData = strings.TrimPrefix(line, "data: ")
		}
	}

	if !strings.HasPrefix(liveID, "2:") || liveEvent != joinlog.EventTypeTaskCreated {
		t.Fatalf("unexpected live frame: id=%q event=%q data=%q", liveID, liveEvent, liveData)
	}

	// 5. Verify since_seq > head waits cleanly on production server
	ctxAhead, cancelAhead := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelAhead()

	reqAhead, _ := http.NewRequestWithContext(ctxAhead, http.MethodGet, ts.URL+"/v1/mesh/events?since_seq=100", nil)
	reqAhead.Header.Set("Authorization", "Bearer "+adminToken)
	respAhead, err := http.DefaultClient.Do(reqAhead)
	if err != nil {
		t.Fatalf("ahead request failed: %v", err)
	}
	defer respAhead.Body.Close()

	if respAhead.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for ahead cursor, got %d", respAhead.StatusCode)
	}
}
