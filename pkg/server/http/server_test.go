package http_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

func setupTestServer(t *testing.T, modifyCfg ...func(*meshhttp.ServerConfig)) (*meshhttp.Server, *httptest.Server) {
	t.Helper()
	tasksDir := t.TempDir()
	cfg := meshhttp.ServerConfig{
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
	}
	for _, fn := range modifyCfg {
		fn(&cfg)
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Shutdown(context.Background())
	})

	return srv, ts
}

func readNextSSEEvent(scanner *bufio.Scanner) (*protocol.Event, error) {
	for {
		var block []byte
		var hasData bool
		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				if len(block) > 0 {
					break
				}
				continue
			}
			if bytes.HasPrefix(trimmed, []byte(":")) {
				// SSE comment / heartbeat - ignore comment lines
				continue
			}
			hasData = true
			block = append(block, line...)
			block = append(block, '\n')
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if len(block) == 0 {
			return nil, io.EOF
		}
		if !hasData {
			continue
		}
		return protocol.ParseSSE(block)
	}
}

func TestServer_Healthz(t *testing.T) {
	_, ts := setupTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var res struct {
		Status        string `json:"status"`
		UptimeSeconds int64  `json:"uptime_seconds"`
		Version       string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.Status != "ok" {
		t.Errorf("expected status ok, got %q", res.Status)
	}
	if res.Version != "v1" {
		t.Errorf("expected version v1, got %q", res.Version)
	}
	if res.UptimeSeconds < 0 {
		t.Errorf("expected uptime >= 0, got %d", res.UptimeSeconds)
	}
}

func TestServer_MeshJoinAndHeartbeat(t *testing.T) {
	_, ts := setupTestServer(t)

	joinReq := protocol.NodeJoinRequest{
		NodeID:   "node-worker-1",
		Endpoint: "http://192.168.1.100:8080",
		Hardware: protocol.NodeHardware{
			CPUs:   8,
			RAMGB:  32,
			HasGPU: true,
			OS:     "linux",
		},
		Agents: []protocol.AgentProfile{
			{Name: "coder", Description: "Coding Agent", Tags: []string{"golang"}},
		},
		MaxConcurrency: 4,
	}
	body, _ := json.Marshal(joinReq)

	// 1. Join
	resp, err := ts.Client().Post(ts.URL+"/v1/mesh/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/mesh/join failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var nodeInfo protocol.NodeInfo
	if err := json.NewDecoder(resp.Body).Decode(&nodeInfo); err != nil {
		t.Fatalf("failed to decode join response: %v", err)
	}
	if nodeInfo.NodeID != "node-worker-1" {
		t.Errorf("expected node ID node-worker-1, got %q", nodeInfo.NodeID)
	}
	if nodeInfo.Status != protocol.NodeStatusOnline {
		t.Errorf("expected status online, got %q", nodeInfo.Status)
	}
	if nodeInfo.MaxConcurrency != 4 {
		t.Errorf("expected concurrency 4, got %d", nodeInfo.MaxConcurrency)
	}

	// 2. Heartbeat
	hbReq := protocol.NodeHeartbeatRequest{
		NodeID:      "node-worker-1",
		ActiveTasks: 2,
		Timestamp:   time.Now().Unix(),
	}
	hbBody, _ := json.Marshal(hbReq)

	hbResp, err := ts.Client().Post(ts.URL+"/v1/mesh/heartbeat", "application/json", bytes.NewReader(hbBody))
	if err != nil {
		t.Fatalf("POST /v1/mesh/heartbeat failed: %v", err)
	}
	defer hbResp.Body.Close()

	if hbResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", hbResp.StatusCode)
	}
	var hbResult map[string]string
	if err := json.NewDecoder(hbResp.Body).Decode(&hbResult); err != nil {
		t.Fatalf("failed to decode heartbeat response: %v", err)
	}
	if hbResult["status"] != "acknowledged" {
		t.Errorf("expected acknowledged, got %q", hbResult["status"])
	}

	// 3. List Nodes
	nodesResp, err := ts.Client().Get(ts.URL + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET /v1/mesh/nodes failed: %v", err)
	}
	defer nodesResp.Body.Close()

	if nodesResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", nodesResp.StatusCode)
	}

	var listResult struct {
		Nodes []*protocol.NodeInfo `json:"nodes"`
	}
	if err := json.NewDecoder(nodesResp.Body).Decode(&listResult); err != nil {
		t.Fatalf("failed to decode nodes list: %v", err)
	}
	if len(listResult.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(listResult.Nodes))
	}
	if listResult.Nodes[0].NodeID != "node-worker-1" {
		t.Errorf("expected node ID node-worker-1, got %q", listResult.Nodes[0].NodeID)
	}
	if listResult.Nodes[0].ActiveTasks != 2 {
		t.Errorf("expected active tasks 2, got %d", listResult.Nodes[0].ActiveTasks)
	}
}

func TestServer_TaskCreateAndEventsStreaming(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens:           []string{"Thinking", "...", "Done!"},
			TokenDelay:       5 * time.Millisecond,
			CompletionResult: "Task complete",
		})
	})

	taskReq := protocol.TaskRequest{
		Agent: "coder",
		Task:  "write code",
	}
	reqBody, _ := json.Marshal(taskReq)

	// Create task
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d", resp.StatusCode)
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("failed to decode task response: %v", err)
	}
	if taskResp.TaskID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if taskResp.EventsURL != "/v1/tasks/"+taskResp.TaskID+"/events" {
		t.Errorf("expected events URL /v1/tasks/%s/events, got %q", taskResp.TaskID, taskResp.EventsURL)
	}

	// Connect to SSE stream
	sseResp, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", sseResp.StatusCode)
	}
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content type, got %q", ct)
	}

	scanner := bufio.NewScanner(sseResp.Body)
	var (
		sawStatus     bool
		sawThought    bool
		sawCompletion bool
	)

	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("error reading SSE event: %v", err)
		}

		switch evt.Type {
		case protocol.EventStatus:
			sawStatus = true
		case protocol.EventThought:
			sawThought = true
		case protocol.EventCompletion:
			sawCompletion = true
		}
	}

	if !sawStatus {
		t.Error("expected to see status event in stream")
	}
	if !sawThought {
		t.Error("expected to see thought event in stream")
	}
	if !sawCompletion {
		t.Error("expected to see completion event in stream")
	}

	// Verify task snapshot
	snapResp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + taskResp.TaskID)
	if err != nil {
		t.Fatalf("GET /v1/tasks/{id} failed: %v", err)
	}
	defer snapResp.Body.Close()

	var state protocol.TaskState
	if err := json.NewDecoder(snapResp.Body).Decode(&state); err != nil {
		t.Fatalf("failed to decode task snapshot: %v", err)
	}
	if state.Status != protocol.TaskStatusCompleted {
		t.Errorf("expected status completed, got %q", state.Status)
	}
	if state.Completion == nil || state.Completion.Result != "Task complete" {
		t.Errorf("unexpected completion payload: %+v", state.Completion)
	}
}

func TestServer_LastEventIDReconnection(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens:     []string{"tok1", "tok2", "tok3", "tok4"},
			TokenDelay: 15 * time.Millisecond,
		})
	})

	taskReq := protocol.TaskRequest{
		Agent: "coder",
		Task:  "reconnection test",
	}
	reqBody, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	var taskResp protocol.TaskResponse
	_ = json.NewDecoder(resp.Body).Decode(&taskResp)

	// Connect first time, read 2 events
	sseResp1, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events 1 failed: %v", err)
	}
	scanner1 := bufio.NewScanner(sseResp1.Body)

	evt1, err := readNextSSEEvent(scanner1)
	if err != nil {
		t.Fatalf("failed reading event 1: %v", err)
	}
	evt2, err := readNextSSEEvent(scanner1)
	if err != nil {
		t.Fatalf("failed reading event 2: %v", err)
	}

	if evt1.ID != 1 {
		t.Errorf("expected first event ID 1, got %d", evt1.ID)
	}
	if evt2.ID != 2 {
		t.Errorf("expected second event ID 2, got %d", evt2.ID)
	}

	// Disconnect
	sseResp1.Body.Close()

	// Wait briefly for more events to be emitted or task to finish
	time.Sleep(100 * time.Millisecond)

	// Reconnect with Last-Event-ID: 2
	req, err := http.NewRequest(http.MethodGet, ts.URL+taskResp.EventsURL, nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "2")

	sseResp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events 2 failed: %v", err)
	}
	defer sseResp2.Body.Close()

	scanner2 := bufio.NewScanner(sseResp2.Body)
	var reconnectedEvents []*protocol.Event
	for {
		evt, err := readNextSSEEvent(scanner2)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("error reading reconnected SSE event: %v", err)
		}
		reconnectedEvents = append(reconnectedEvents, evt)
	}

	if len(reconnectedEvents) == 0 {
		t.Fatal("expected to receive events after reconnection")
	}

	for _, evt := range reconnectedEvents {
		if evt.ID <= 2 {
			t.Errorf("expected event ID > 2 after reconnection with Last-Event-ID=2, got ID %d", evt.ID)
		}
	}

	// Also test since_id query param
	reqSince, _ := http.NewRequest(http.MethodGet, ts.URL+taskResp.EventsURL+"?since_id=2", nil)
	sseRespSince, err := ts.Client().Do(reqSince)
	if err != nil {
		t.Fatalf("GET events since_id failed: %v", err)
	}
	defer sseRespSince.Body.Close()

	scannerSince := bufio.NewScanner(sseRespSince.Body)
	evtSince, err := readNextSSEEvent(scannerSince)
	if err != nil {
		t.Fatalf("failed reading event since_id: %v", err)
	}
	if evtSince.ID <= 2 {
		t.Errorf("expected event ID > 2 with since_id=2, got ID %d", evtSince.ID)
	}
}

func TestServer_InteractiveQueryAndReply(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "query-step-1",
				Question: "Permission to format disk?",
			},
			Tokens:           []string{"Formatting completed safely."},
			CompletionResult: "Operation finished",
		})
	})

	taskReq := protocol.TaskRequest{
		Agent: "system-admin",
		Task:  "disk format operation",
	}
	reqBody, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	var taskResp protocol.TaskResponse
	_ = json.NewDecoder(resp.Body).Decode(&taskResp)

	// Stream events until query
	sseResp, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	scanner := bufio.NewScanner(sseResp.Body)
	var queryFound bool

	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			t.Fatalf("failed reading SSE event: %v", err)
		}
		if evt.Type == protocol.EventQuery {
			queryFound = true
			var qp protocol.QueryPayload
			if err := evt.UnmarshalPayload(&qp); err != nil {
				t.Fatalf("failed to unmarshal query payload: %v", err)
			}
			if qp.QueryID != "query-step-1" {
				t.Errorf("expected query ID query-step-1, got %q", qp.QueryID)
			}
			break
		}
	}

	if !queryFound {
		t.Fatal("did not receive query event")
	}

	// Submit reply
	replyReq := protocol.TaskReplyRequest{
		QueryID: "query-step-1",
		Answer:  "yes, proceed",
	}
	replyBody, _ := json.Marshal(replyReq)

	replyResp, err := ts.Client().Post(
		ts.URL+"/v1/tasks/"+taskResp.TaskID+"/reply",
		"application/json",
		bytes.NewReader(replyBody),
	)
	if err != nil {
		t.Fatalf("POST reply failed: %v", err)
	}
	defer replyResp.Body.Close()

	if replyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", replyResp.StatusCode)
	}

	var replyResult map[string]string
	_ = json.NewDecoder(replyResp.Body).Decode(&replyResult)
	if replyResult["status"] != "accepted" {
		t.Errorf("expected accepted, got %q", replyResult["status"])
	}

	// Stream should resume and finish with completion
	var sawCompletion bool
	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("failed reading post-reply event: %v", err)
		}
		if evt.Type == protocol.EventCompletion {
			sawCompletion = true
		}
	}

	if !sawCompletion {
		t.Error("expected completion event after resolving query")
	}

	// Verify task completed
	getResp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + taskResp.TaskID)
	if err != nil {
		t.Fatalf("GET /v1/tasks failed: %v", err)
	}
	defer getResp.Body.Close()

	var state protocol.TaskState
	_ = json.NewDecoder(getResp.Body).Decode(&state)
	if state.Status != protocol.TaskStatusCompleted {
		t.Errorf("expected status completed, got %q", state.Status)
	}
}

func TestServer_TaskCancellation(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "cancel-wait",
				Question: "Waiting to be canceled...",
			},
		})
	})

	taskReq := protocol.TaskRequest{
		Agent: "coder",
		Task:  "will be canceled",
	}
	reqBody, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	var taskResp protocol.TaskResponse
	_ = json.NewDecoder(resp.Body).Decode(&taskResp)

	// Connect to SSE stream
	sseResp, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	scanner := bufio.NewScanner(sseResp.Body)
	// Wait until query event is emitted (so task is definitely running)
	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			t.Fatalf("failed reading event: %v", err)
		}
		if evt.Type == protocol.EventQuery {
			break
		}
	}

	// Cancel task
	cancelBody := bytes.NewReader([]byte(`{"reason":"user abort"}`))
	cancelResp, err := ts.Client().Post(
		ts.URL+"/v1/tasks/"+taskResp.TaskID+"/cancel",
		"application/json",
		cancelBody,
	)
	if err != nil {
		t.Fatalf("POST cancel failed: %v", err)
	}
	defer cancelResp.Body.Close()

	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", cancelResp.StatusCode)
	}

	// Verify SSE stream receives status canceled and closes
	var sawCanceledStatus bool
	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("error reading canceled stream: %v", err)
		}
		if evt.Type == protocol.EventStatus {
			var sp protocol.StatusPayload
			_ = evt.UnmarshalPayload(&sp)
			if sp.Status == protocol.TaskStatusCanceled {
				sawCanceledStatus = true
			}
		}
	}

	if !sawCanceledStatus {
		t.Error("expected status canceled event in stream")
	}

	// Verify snapshot state is canceled
	getResp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + taskResp.TaskID)
	if err != nil {
		t.Fatalf("GET task failed: %v", err)
	}
	defer getResp.Body.Close()

	var state protocol.TaskState
	_ = json.NewDecoder(getResp.Body).Decode(&state)
	if state.Status != protocol.TaskStatusCanceled {
		t.Errorf("expected status canceled, got %q", state.Status)
	}
}

func TestServer_Idempotency(t *testing.T) {
	srv, ts := setupTestServer(t)

	taskReq := protocol.TaskRequest{
		IdempotencyKey: "test-idem-key-100",
		Agent:          "coder",
		Task:           "idempotent work",
	}
	body, _ := json.Marshal(taskReq)

	// First request: 201 Created
	resp1, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first POST failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created on first request, got %d", resp1.StatusCode)
	}

	var taskResp1 protocol.TaskResponse
	if err := json.NewDecoder(resp1.Body).Decode(&taskResp1); err != nil {
		t.Fatalf("failed decoding first response: %v", err)
	}

	// Second request with same idempotency key: 200 OK returning same task ID
	resp2, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second POST failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK on duplicate request, got %d", resp2.StatusCode)
	}

	var taskResp2 protocol.TaskResponse
	if err := json.NewDecoder(resp2.Body).Decode(&taskResp2); err != nil {
		t.Fatalf("failed decoding second response: %v", err)
	}

	if taskResp1.TaskID != taskResp2.TaskID {
		t.Errorf("expected same task ID for idempotent requests, got %q and %q", taskResp1.TaskID, taskResp2.TaskID)
	}

	// Verify only 1 task was stored
	tasks := srv.TaskManager().ListTasks()
	if len(tasks) != 1 {
		t.Errorf("expected exactly 1 task in manager, got %d", len(tasks))
	}
}

func TestServer_ExclusiveBranchLock(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeStrict
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "lock-wait",
				Question: "Holding lock...",
			},
		})
	})

	task1Req := protocol.TaskRequest{
		Agent:     "coder",
		Task:      "work on branch",
		GitRepo:   "gentleman/gentle-mesh",
		GitBranch: "feature/mesh-locks",
	}
	body1, _ := json.Marshal(task1Req)

	// 1. Task 1 creates lock
	resp1, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body1))
	if err != nil {
		t.Fatalf("Task 1 POST failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected Task 1 status 201, got %d", resp1.StatusCode)
	}

	var t1Resp protocol.TaskResponse
	_ = json.NewDecoder(resp1.Body).Decode(&t1Resp)

	// 2. Task 2 on SAME repo/branch should get 409 Conflict
	task2Req := protocol.TaskRequest{
		Agent:     "reviewer",
		Task:      "conflicting work",
		GitRepo:   "gentleman/gentle-mesh",
		GitBranch: "feature/mesh-locks",
	}
	body2, _ := json.Marshal(task2Req)

	resp2, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatalf("Task 2 POST failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected Task 2 status 409 Conflict, got %d", resp2.StatusCode)
	}

	var conflictResp map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&conflictResp)
	errStr, _ := conflictResp["error"].(string)
	if !strings.Contains(errStr, "branch is locked") {
		t.Errorf("expected branch is locked error, got %v", conflictResp)
	}

	// 3. Cancel Task 1 to release lock
	cancelResp, err := ts.Client().Post(ts.URL+"/v1/tasks/"+t1Resp.TaskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("cancel Task 1 failed: %v", err)
	}
	cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("expected cancel status 200, got %d", cancelResp.StatusCode)
	}

	// 4. Task 3 on SAME repo/branch should now succeed (201 Created)
	task3Req := protocol.TaskRequest{
		Agent:     "reviewer",
		Task:      "subsequent work",
		GitRepo:   "gentleman/gentle-mesh",
		GitBranch: "feature/mesh-locks",
	}
	body3, _ := json.Marshal(task3Req)

	resp3, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body3))
	if err != nil {
		t.Fatalf("Task 3 POST failed: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("expected Task 3 status 201 Created after lock release, got %d", resp3.StatusCode)
	}
}

func TestServer_AuthBearer(t *testing.T) {
	const secret = "mesh-token-xyz"
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.BearerToken = secret
	})

	// /healthz should bypass auth
	respHealth, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer respHealth.Body.Close()
	if respHealth.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on /healthz without auth, got %d", respHealth.StatusCode)
	}

	// Protected endpoint without token -> 401
	respNoAuth, err := ts.Client().Get(ts.URL + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET nodes without auth failed: %v", err)
	}
	defer respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", respNoAuth.StatusCode)
	}

	// Protected endpoint with invalid token -> 401
	reqWrong, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/nodes", nil)
	reqWrong.Header.Set("Authorization", "Bearer wrong-token")
	respWrong, err := ts.Client().Do(reqWrong)
	if err != nil {
		t.Fatalf("GET nodes with wrong token failed: %v", err)
	}
	defer respWrong.Body.Close()
	if respWrong.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", respWrong.StatusCode)
	}

	// Protected endpoint with valid token -> 200
	reqValid, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/mesh/nodes", nil)
	reqValid.Header.Set("Authorization", "Bearer "+secret)
	respValid, err := ts.Client().Do(reqValid)
	if err != nil {
		t.Fatalf("GET nodes with valid token failed: %v", err)
	}
	defer respValid.Body.Close()
	if respValid.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", respValid.StatusCode)
	}
}

func TestServer_PanicRecovery(t *testing.T) {
	panickingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate panic for recovery test")
	})

	recoveredHandler := meshhttp.PanicRecoveryMiddleware(panickingHandler)
	ts := httptest.NewServer(recoveredHandler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/cause-panic")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", resp.StatusCode)
	}

	var res map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding panic response: %v", err)
	}
	if res["error"] != "internal server error" {
		t.Errorf("expected internal server error message, got %q", res["error"])
	}
}

func TestServer_ValidationErrors(t *testing.T) {
	_, ts := setupTestServer(t)

	// 1. Missing agent/task
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(`{"agent":"","task":""}`))
	if err != nil {
		t.Fatalf("POST invalid task failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for empty task, got %d", resp.StatusCode)
	}

	// 2. Nonexistent task snapshot
	resp2, err := ts.Client().Get(ts.URL + "/v1/tasks/nonexistent-id")
	if err != nil {
		t.Fatalf("GET nonexistent task failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent task, got %d", resp2.StatusCode)
	}

	// 3. Nonexistent task events
	resp3, err := ts.Client().Get(ts.URL + "/v1/tasks/nonexistent-id/events")
	if err != nil {
		t.Fatalf("GET nonexistent task events failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent task events, got %d", resp3.StatusCode)
	}

	// 4. Nonexistent task reply
	resp4, err := ts.Client().Post(ts.URL+"/v1/tasks/nonexistent-id/reply", "application/json", strings.NewReader(`{"query_id":"q1","answer":"ok"}`))
	if err != nil {
		t.Fatalf("POST nonexistent task reply failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent task reply, got %d", resp4.StatusCode)
	}

	// 5. Nonexistent task cancel
	resp5, err := ts.Client().Post(ts.URL+"/v1/tasks/nonexistent-id/cancel", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST nonexistent task cancel failed: %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent task cancel, got %d", resp5.StatusCode)
	}
}

func TestServer_MeshRadar(t *testing.T) {
	srv, ts := setupTestServer(t)

	// 1. Initial radar when empty
	resp, err := ts.Client().Get(ts.URL + "/v1/mesh/radar")
	if err != nil {
		t.Fatalf("GET /v1/mesh/radar failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var report protocol.RadarReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("failed decoding radar report: %v", err)
	}

	if report.ClusterName != "gentle-mesh" {
		t.Errorf("expected cluster_name gentle-mesh, got %q", report.ClusterName)
	}
	if len(report.ActiveAgents) != 0 {
		t.Fatalf("expected 0 active agents initially, got %d", len(report.ActiveAgents))
	}

	// 2. Create task directly via task manager to control live event emission
	taskReq := protocol.TaskRequest{
		Agent:     "worker",
		Task:      "Refactor authentication middleware",
		GitRepo:   "github.com/gentleman-programming/gentle-mesh",
		GitBranch: "feature/auth-radar",
	}
	mt, err := srv.TaskManager().CreateTask(taskReq)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Verify task appears in radar (currently queued)
	resp2, err := ts.Client().Get(ts.URL + "/v1/mesh/radar")
	if err != nil {
		t.Fatalf("GET /v1/mesh/radar failed: %v", err)
	}
	defer resp2.Body.Close()

	if err := json.NewDecoder(resp2.Body).Decode(&report); err != nil {
		t.Fatalf("failed decoding radar report: %v", err)
	}

	if len(report.ActiveAgents) != 1 {
		t.Fatalf("expected 1 active agent in radar, got %d", len(report.ActiveAgents))
	}
	agent0 := report.ActiveAgents[0]
	if agent0.TaskID != mt.TaskID {
		t.Errorf("expected task ID %s, got %s", mt.TaskID, agent0.TaskID)
	}
	if agent0.BlastRadius != protocol.BlastRadiusIsolated {
		t.Errorf("expected default blast radius isolated-branch, got %s", agent0.BlastRadius)
	}

	// 3. Set custom scope
	mt.SetScope("auth", protocol.BlastRadiusSharedSchema, protocol.AgentPhasePlan, "Planning auth migration")
	mt.SetEditSurfaces([]string{"pkg/auth/jwt.go", "pkg/auth/middleware.go"})

	resp3, err := ts.Client().Get(ts.URL + "/v1/mesh/radar")
	if err != nil {
		t.Fatalf("GET /v1/mesh/radar failed: %v", err)
	}
	defer resp3.Body.Close()

	if err := json.NewDecoder(resp3.Body).Decode(&report); err != nil {
		t.Fatalf("failed decoding radar report: %v", err)
	}

	agent0 = report.ActiveAgents[0]
	if agent0.Domain != "auth" {
		t.Errorf("expected domain auth, got %q", agent0.Domain)
	}
	if agent0.BlastRadius != protocol.BlastRadiusSharedSchema {
		t.Errorf("expected blast radius shared-schema, got %q", agent0.BlastRadius)
	}
	if agent0.Phase != protocol.AgentPhasePlan {
		t.Errorf("expected phase plan, got %q", agent0.Phase)
	}
	if agent0.CurrentAction != "Planning auth migration" {
		t.Errorf("expected action 'Planning auth migration', got %q", agent0.CurrentAction)
	}
	if len(agent0.EditSurfaces) != 2 {
		t.Errorf("expected 2 edit surfaces, got %d", len(agent0.EditSurfaces))
	}

	// 4. Live action transitions via events:
	// Tool call "read" -> Phase explore
	_, err = mt.EmitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
		CallID: "call-1",
		Tool:   "read",
		Args:   map[string]any{"path": "pkg/auth/jwt.go"},
	})
	if err != nil {
		t.Fatalf("EmitEvent tool call read failed: %v", err)
	}
	terr := mt.Territory()
	if terr.Phase != protocol.AgentPhaseExplore || terr.CurrentAction != "Running tool read" {
		t.Errorf("expected phase explore and action 'Running tool read', got phase=%q action=%q", terr.Phase, terr.CurrentAction)
	}

	// Tool call "edit" -> Phase apply
	_, err = mt.EmitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
		CallID: "call-2",
		Tool:   "edit",
		Args:   map[string]any{"path": "pkg/auth/claims.go"},
	})
	if err != nil {
		t.Fatalf("EmitEvent tool call edit failed: %v", err)
	}
	terr = mt.Territory()
	if terr.Phase != protocol.AgentPhaseApply || terr.CurrentAction != "Running tool edit" {
		t.Errorf("expected phase apply and action 'Running tool edit', got phase=%q action=%q", terr.Phase, terr.CurrentAction)
	}

	// Tool call "bash" -> Phase verify
	_, err = mt.EmitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
		CallID: "call-3",
		Tool:   "bash",
		Args:   map[string]any{"command": "go test ./pkg/auth"},
	})
	if err != nil {
		t.Fatalf("EmitEvent tool call bash failed: %v", err)
	}
	terr = mt.Territory()
	if terr.Phase != protocol.AgentPhaseVerify || terr.CurrentAction != "Running tool bash" {
		t.Errorf("expected phase verify and action 'Running tool bash', got phase=%q action=%q", terr.Phase, terr.CurrentAction)
	}

	// Thought event -> sets truncated text
	_, err = mt.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{
		Text: "Analyzing test failure in TestTokenExpiry\nExtra line to be truncated",
	})
	if err != nil {
		t.Fatalf("EmitEvent thought failed: %v", err)
	}
	terr = mt.Territory()
	if terr.CurrentAction != "Analyzing test failure in TestTokenExpiry" {
		t.Errorf("expected truncated single-line thought action, got %q", terr.CurrentAction)
	}

	// 5. Completion event -> Phase verify, Action "Task completed"
	_, err = mt.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{
		Result: "Successfully refactored auth middleware",
	})
	if err != nil {
		t.Fatalf("EmitEvent completion failed: %v", err)
	}

	// Radar should no longer show the completed task
	resp4, err := ts.Client().Get(ts.URL + "/v1/mesh/radar")
	if err != nil {
		t.Fatalf("GET /v1/mesh/radar failed: %v", err)
	}
	defer resp4.Body.Close()

	if err := json.NewDecoder(resp4.Body).Decode(&report); err != nil {
		t.Fatalf("failed decoding radar report: %v", err)
	}
	if len(report.ActiveAgents) != 0 {
		t.Errorf("expected completed task to be omitted from active radar, got %d agents", len(report.ActiveAgents))
	}
}

type panicRunner struct{}

func (p *panicRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	panic("simulated runner crash")
}

func TestServer_TerritorySurfaceOverlapConflict(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeStrict
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "keep-running",
				Question: "Waiting...",
			},
		})
	})

	task1Req := protocol.TaskRequest{
		Agent:        "coder",
		Task:         "working on auth package",
		GitRepo:      "org/repo",
		EditSurfaces: []string{"pkg/auth/*"},
	}
	body1, _ := json.Marshal(task1Req)

	resp1, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body1))
	if err != nil {
		t.Fatalf("Task 1 POST failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected Task 1 status 201 Created, got %d", resp1.StatusCode)
	}

	task2Req := protocol.TaskRequest{
		Agent:        "reviewer",
		Task:         "fix login endpoint",
		GitRepo:      "org/repo",
		EditSurfaces: []string{"pkg/auth/login.go"},
	}
	body2, _ := json.Marshal(task2Req)

	resp2, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatalf("Task 2 POST failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected Task 2 status 409 Conflict, got %d", resp2.StatusCode)
	}

	var conflictResp struct {
		Error    string `json:"error"`
		TaskID   string `json:"task_id"`
		Conflict struct {
			ConflictType string `json:"conflict_type"`
			Message      string `json:"message"`
		} `json:"conflict"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&conflictResp); err != nil {
		t.Fatalf("failed decoding conflict response: %v", err)
	}

	if conflictResp.Conflict.ConflictType != "surface_overlap" {
		t.Errorf("expected conflict_type 'surface_overlap', got %q", conflictResp.Conflict.ConflictType)
	}
}

func TestServer_RunnerPanicRecovery(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.Runner = &panicRunner{}
	})

	taskReq := protocol.TaskRequest{
		Agent: "coder",
		Task:  "risky task that panics",
	}
	body, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d", resp.StatusCode)
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("failed decoding task response: %v", err)
	}

	sseResp, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	scanner := bufio.NewScanner(sseResp.Body)
	var sawPanicError bool
	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("error reading SSE event: %v", err)
		}

		if evt.Type == protocol.EventError {
			var ep protocol.ErrorPayload
			if err := evt.UnmarshalPayload(&ep); err != nil {
				t.Fatalf("failed unmarshaling error payload: %v", err)
			}
			if ep.Code == "RUNNER_PANIC" && ep.Fatal {
				sawPanicError = true
			}
		}
	}

	if !sawPanicError {
		t.Fatal("expected to observe EventError with code RUNNER_PANIC and fatal: true")
	}

	// Verify server does NOT crash and is still responsive
	healthResp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Errorf("expected healthz status 200, got %d", healthResp.StatusCode)
	}
}

func TestServer_MaxBytesReader(t *testing.T) {
	_, ts := setupTestServer(t)

	// Sending a body > 1MB (e.g. 1.5MB) to /v1/tasks
	largePayload := make([]byte, 1500*1024)
	for i := range largePayload {
		largePayload[i] = 'x'
	}
	taskReq := protocol.TaskRequest{
		Agent: "worker",
		Task:  string(largePayload),
	}
	body, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		// When MaxBytesReader hits the limit, connection may be closed/reset
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 Bad Request for oversized body, got %d", resp.StatusCode)
	}
}

func TestServer_SSEHeartbeat(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.SSEHeartbeatInterval = 20 * time.Millisecond
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "hb-hold",
				Question: "holding task open for heartbeat",
			},
		})
	})

	taskReq := protocol.TaskRequest{
		Agent: "worker",
		Task:  "sse-heartbeat-test",
	}
	body, _ := json.Marshal(taskReq)

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("failed decoding task response: %v", err)
	}

	sseResp, err := ts.Client().Get(ts.URL + taskResp.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", sseResp.StatusCode)
	}

	scanner := bufio.NewScanner(sseResp.Body)
	sawPing := make(chan bool, 1)

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(strings.TrimSpace(line), ": ping") {
				sawPing <- true
				return
			}
		}
		sawPing <- false
	}()

	select {
	case ok := <-sawPing:
		if !ok {
			t.Fatal("stream closed without sending : ping heartbeat")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE heartbeat ping comment")
	}
}

func TestServer_PeeringEndpoints(t *testing.T) {
	srv, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.PeerID = "local-coordinator"
		cfg.ClusterName = "local-cluster"
	})

	if srv.TerritoryManager() == nil {
		t.Fatal("expected TerritoryManager to be initialized, got nil")
	}

	// 1. Get initial local manifest via GET /v1/mesh/territory
	resp, err := ts.Client().Get(ts.URL + "/v1/mesh/territory")
	if err != nil {
		t.Fatalf("GET /v1/mesh/territory failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var initManifest protocol.TerritoryManifest
	if err := json.NewDecoder(resp.Body).Decode(&initManifest); err != nil {
		t.Fatalf("failed decoding territory manifest: %v", err)
	}
	if initManifest.PeerID != "local-coordinator" {
		t.Errorf("expected peer ID local-coordinator, got %q", initManifest.PeerID)
	}
	if initManifest.ClusterName != "local-cluster" {
		t.Errorf("expected cluster name local-cluster, got %q", initManifest.ClusterName)
	}
	if len(initManifest.Territories) != 0 {
		t.Errorf("expected 0 initial territories, got %d", len(initManifest.Territories))
	}

	// 2. Register peer via POST /v1/mesh/peers/register
	regReq := protocol.PeerRegisterRequest{
		PeerID:      "remote-peer-1",
		ClusterName: "remote-cluster-1",
		Endpoint:    "http://192.168.1.50:8080",
		AuthToken:   "secret-token",
	}
	body, _ := json.Marshal(regReq)
	resp, err = ts.Client().Post(ts.URL+"/v1/mesh/peers/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/mesh/peers/register failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var peerInfo protocol.PeerInfo
	if err := json.NewDecoder(resp.Body).Decode(&peerInfo); err != nil {
		t.Fatalf("failed decoding peer info: %v", err)
	}
	if peerInfo.PeerID != "remote-peer-1" {
		t.Errorf("expected peer ID remote-peer-1, got %q", peerInfo.PeerID)
	}
	if peerInfo.ClusterName != "remote-cluster-1" {
		t.Errorf("expected cluster name remote-cluster-1, got %q", peerInfo.ClusterName)
	}
	if peerInfo.Endpoint != "http://192.168.1.50:8080" {
		t.Errorf("expected endpoint http://192.168.1.50:8080, got %q", peerInfo.Endpoint)
	}
	if peerInfo.Status != protocol.PeerStatusActive {
		t.Errorf("expected status active, got %q", peerInfo.Status)
	}

	// Register second peer via alias POST /v1/mesh/peers
	regReq2 := protocol.PeerRegisterRequest{
		PeerID:      "remote-peer-2",
		ClusterName: "remote-cluster-2",
		Endpoint:    "http://192.168.1.51:8080",
	}
	body2, _ := json.Marshal(regReq2)
	resp2, err := ts.Client().Post(ts.URL+"/v1/mesh/peers", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatalf("POST /v1/mesh/peers failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on /v1/mesh/peers, got %d", resp2.StatusCode)
	}

	// Register with empty info returns 400
	badReq := protocol.PeerRegisterRequest{PeerID: ""}
	badBody, _ := json.Marshal(badReq)
	badResp, err := ts.Client().Post(ts.URL+"/v1/mesh/peers/register", "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatalf("POST invalid peer failed: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for empty peer ID, got %d", badResp.StatusCode)
	}

	// Self-peering returns 400
	selfReq := protocol.PeerRegisterRequest{
		PeerID:   "local-coordinator",
		Endpoint: "http://127.0.0.1:8080",
	}
	selfBody, _ := json.Marshal(selfReq)
	selfResp, err := ts.Client().Post(ts.URL+"/v1/mesh/peers/register", "application/json", bytes.NewReader(selfBody))
	if err != nil {
		t.Fatalf("POST self peering failed: %v", err)
	}
	selfResp.Body.Close()
	if selfResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for self-peering, got %d", selfResp.StatusCode)
	}

	// 3. List peers via GET /v1/mesh/peers
	resp, err = ts.Client().Get(ts.URL + "/v1/mesh/peers")
	if err != nil {
		t.Fatalf("GET /v1/mesh/peers failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var listResp struct {
		Peers []protocol.PeerInfo `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("failed decoding peers list: %v", err)
	}
	if len(listResp.Peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(listResp.Peers))
	}
	if listResp.Peers[0].PeerID != "remote-peer-1" || listResp.Peers[1].PeerID != "remote-peer-2" {
		t.Errorf("unexpected peer list order: %+v", listResp.Peers)
	}

	// 4. Delete peer via DELETE /v1/mesh/peers/{id}
	delReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/mesh/peers/remote-peer-2", nil)
	if err != nil {
		t.Fatalf("creating DELETE request failed: %v", err)
	}
	resp, err = ts.Client().Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /v1/mesh/peers/remote-peer-2 failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var delResp map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&delResp); err != nil {
		t.Fatalf("failed decoding delete response: %v", err)
	}
	if delResp["status"] != "deregistered" {
		t.Errorf("expected status 'deregistered', got %q", delResp["status"])
	}

	// Verify remaining peer count
	resp, err = ts.Client().Get(ts.URL + "/v1/mesh/peers")
	if err != nil {
		t.Fatalf("GET /v1/mesh/peers after delete failed: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("failed decoding peers list: %v", err)
	}
	if len(listResp.Peers) != 1 {
		t.Fatalf("expected 1 peer after delete, got %d", len(listResp.Peers))
	}

	// 5. Verify 404 on deleting non-existent peer
	req404, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/mesh/peers/non-existent-peer", nil)
	if err != nil {
		t.Fatalf("creating DELETE request failed: %v", err)
	}
	resp404, err := ts.Client().Do(req404)
	if err != nil {
		t.Fatalf("DELETE non-existent peer failed: %v", err)
	}
	defer resp404.Body.Close()

	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp404.StatusCode)
	}

	// 6. Verify 404 on syncing non-existent peer
	syncResp404, err := ts.Client().Post(ts.URL+"/v1/mesh/peers/non-existent-peer/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST sync non-existent peer failed: %v", err)
	}
	defer syncResp404.Body.Close()

	if syncResp404.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 on syncing non-existent peer, got %d", syncResp404.StatusCode)
	}

	// 7. Verify 502 Bad Gateway on syncing unreachable peer
	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dummyURL := dummy.URL
	dummy.Close() // immediately closed, causes connection refused instantly

	unreachableReq := protocol.PeerRegisterRequest{
		PeerID:   "unreachable-peer",
		Endpoint: dummyURL,
	}
	unreachBody, _ := json.Marshal(unreachableReq)
	resp, err = ts.Client().Post(ts.URL+"/v1/mesh/peers/register", "application/json", bytes.NewReader(unreachBody))
	if err != nil {
		t.Fatalf("POST register unreachable peer failed: %v", err)
	}
	resp.Body.Close()

	syncResp502, err := ts.Client().Post(ts.URL+"/v1/mesh/peers/unreachable-peer/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST sync unreachable peer failed: %v", err)
	}
	defer syncResp502.Body.Close()

	if syncResp502.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected status 502 Bad Gateway on syncing unreachable peer, got %d", syncResp502.StatusCode)
	}
}

func TestServer_FederatedTerritoryConflict(t *testing.T) {
	_, tsA := setupTestServer(t, func(c *meshhttp.ServerConfig) {
		c.PeerID = "coord-a"
		c.ClusterName = "cluster-a"
		c.TerritoryMode = protocol.TerritoryModeStrict
	})
	srvB, tsB := setupTestServer(t, func(c *meshhttp.ServerConfig) {
		c.PeerID = "coord-b"
		c.ClusterName = "cluster-b"
	})

	// On Coord B, create an active task touching GitRepo "github.com/gentleman-programming/gentle-mesh",
	// branch "main", EditSurfaces ["pkg/auth/*"].
	taskB, err := srvB.TaskManager().CreateTask(protocol.TaskRequest{
		Agent:        "worker",
		Task:         "Implement auth subsystem",
		GitRepo:      "github.com/gentleman-programming/gentle-mesh",
		GitBranch:    "main",
		EditSurfaces: []string{"pkg/auth/*"},
	})
	if err != nil {
		t.Fatalf("failed to create task on Coord B: %v", err)
	}

	// On Coord A, register Coord B as peer pointing to Coord B's test server URL.
	regPayload := protocol.PeerRegisterRequest{
		PeerID:      "coord-b",
		ClusterName: "cluster-b",
		Endpoint:    tsB.URL,
	}
	body, _ := json.Marshal(regPayload)
	regResp, err := tsA.Client().Post(tsA.URL+"/v1/mesh/peers/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register peer on Coord A failed: %v", err)
	}
	defer regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on peer register, got %d", regResp.StatusCode)
	}

	// On Coord A, call POST /v1/mesh/peers/coord-b/sync.
	syncResp, err := tsA.Client().Post(tsA.URL+"/v1/mesh/peers/coord-b/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("sync peer on Coord A failed: %v", err)
	}
	defer syncResp.Body.Close()
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on peer sync, got %d", syncResp.StatusCode)
	}

	var syncManifest protocol.TerritoryManifest
	if err := json.NewDecoder(syncResp.Body).Decode(&syncManifest); err != nil {
		t.Fatalf("failed decoding sync response: %v", err)
	}
	if syncManifest.PeerID != "coord-b" {
		t.Errorf("expected sync manifest PeerID coord-b, got %q", syncManifest.PeerID)
	}
	if len(syncManifest.Territories) != 1 {
		t.Fatalf("expected 1 active territory in Coord B sync manifest, got %d", len(syncManifest.Territories))
	}

	// On Coord A, attempt to create a task touching GitRepo "git@github.com:gentleman-programming/gentle-mesh.git",
	// branch "feature/auth", EditSurfaces ["pkg/auth/login.go"].
	taskReqA := protocol.TaskRequest{
		Agent:        "worker",
		Task:         "Add login endpoint",
		GitRepo:      "git@github.com:gentleman-programming/gentle-mesh.git",
		GitBranch:    "feature/auth",
		EditSurfaces: []string{"pkg/auth/login.go"},
	}
	taskBodyA, _ := json.Marshal(taskReqA)
	createResp, err := tsA.Client().Post(tsA.URL+"/v1/tasks", "application/json", bytes.NewReader(taskBodyA))
	if err != nil {
		t.Fatalf("POST /v1/tasks on Coord A failed: %v", err)
	}
	defer createResp.Body.Close()

	// Verify Coord A returns HTTP 409 Conflict with conflict details!
	if createResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict, got %d", createResp.StatusCode)
	}

	var conflictResp struct {
		Error    string                     `json:"error"`
		Conflict *protocol.TerritoryConflict `json:"conflict"`
		TaskID   string                     `json:"task_id"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&conflictResp); err != nil {
		t.Fatalf("failed decoding conflict response: %v", err)
	}

	if conflictResp.Conflict == nil {
		t.Fatal("expected conflict object in response, got nil")
	}
	if conflictResp.TaskID != taskB.TaskID {
		t.Errorf("expected conflict task_id %q, got %q", taskB.TaskID, conflictResp.TaskID)
	}
	if conflictResp.Conflict.ConflictType != protocol.ConflictSurfaceOverlap {
		t.Errorf("expected conflict type %q, got %q", protocol.ConflictSurfaceOverlap, conflictResp.Conflict.ConflictType)
	}
	if !strings.Contains(conflictResp.Error, "overlap") {
		t.Errorf("expected error message to mention overlap, got %q", conflictResp.Error)
	}
}

func TestServer_SQLiteStoreInitialization(t *testing.T) {
	t.Run("default DBPath initializes SQLite store file gentle-mesh.db in TasksDir", func(t *testing.T) {
		tasksDir := t.TempDir()
		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir:         tasksDir,
			HeartbeatTimeout: 5 * time.Second,
			TaskTTL:          1 * time.Hour,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}
		defer func() {
			_ = srv.Shutdown(context.Background())
		}()

		if srv.TaskManager().Store() == nil {
			t.Fatal("expected TaskManager store to be non-nil with default DBPath")
		}

		dbFile := filepath.Join(tasksDir, "gentle-mesh.db")
		if info, err := os.Stat(dbFile); err != nil {
			t.Fatalf("expected SQLite db file to exist at %s: %v", dbFile, err)
		} else if info.IsDir() {
			t.Fatalf("expected SQLite db file at %s, but found directory", dbFile)
		}
	})

	t.Run("DBPath none disables SQLite store", func(t *testing.T) {
		tasksDir := t.TempDir()
		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir:         tasksDir,
			DBPath:           "none",
			HeartbeatTimeout: 5 * time.Second,
			TaskTTL:          1 * time.Hour,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}
		defer func() {
			_ = srv.Shutdown(context.Background())
		}()

		if srv.TaskManager().Store() != nil {
			t.Errorf("expected TaskManager store to be nil with DBPath: 'none', got %v", srv.TaskManager().Store())
		}

		dbFile := filepath.Join(tasksDir, "gentle-mesh.db")
		if _, err := os.Stat(dbFile); !os.IsNotExist(err) {
			t.Errorf("expected SQLite db file to not exist when disabled, got err: %v", err)
		}
	})

	t.Run("explicit Store in config is used", func(t *testing.T) {
		tasksDir := t.TempDir()
		customDB := filepath.Join(t.TempDir(), "custom.db")
		customStore, err := store.NewSQLiteStore(customDB)
		if err != nil {
			t.Fatalf("failed to create custom store: %v", err)
		}

		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir:         tasksDir,
			Store:            customStore,
			HeartbeatTimeout: 5 * time.Second,
			TaskTTL:          1 * time.Hour,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}
		defer func() {
			_ = srv.Shutdown(context.Background())
		}()

		if srv.TaskManager().Store() != customStore {
			t.Errorf("expected TaskManager to use custom store")
		}
		defaultDBFile := filepath.Join(tasksDir, "gentle-mesh.db")
		if _, err := os.Stat(defaultDBFile); !os.IsNotExist(err) {
			t.Errorf("expected default db file not to exist when explicit Store is provided")
		}
	})

	t.Run("custom DBPath initializes at specified path", func(t *testing.T) {
		tasksDir := t.TempDir()
		customDBDir := t.TempDir()
		customDBPath := filepath.Join(customDBDir, "custom-tasks.db")
		srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
			TasksDir:         tasksDir,
			DBPath:           customDBPath,
			HeartbeatTimeout: 5 * time.Second,
			TaskTTL:          1 * time.Hour,
		})
		if err != nil {
			t.Fatalf("failed to create server: %v", err)
		}
		defer func() {
			_ = srv.Shutdown(context.Background())
		}()

		if srv.TaskManager().Store() == nil {
			t.Fatal("expected TaskManager store to be non-nil with custom DBPath")
		}

		if _, err := os.Stat(customDBPath); err != nil {
			t.Fatalf("expected custom SQLite db file at %s: %v", customDBPath, err)
		}
	})
}

// postTaskHTTP submits a task creation request, asserts HTTP 201 Created, and
// returns the decoded protocol.TaskResponse.
func postTaskHTTP(t *testing.T, ts *httptest.Server, req protocol.TaskRequest) protocol.TaskResponse {
	t.Helper()

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed marshaling task request: %v", err)
	}

	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d", resp.StatusCode)
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("failed decoding task response: %v", err)
	}
	return taskResp
}

func TestServer_TerritoryQueueMode_TransparentFIFO(t *testing.T) {
	gate := newSchedulerGateRunner()
	srv, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeQueue
		cfg.Runner = gate
	})

	const repo = "github.com/gentleman-programming/gentle-mesh"
	const branch = "feature/queue-test"

	first := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:     "worker",
		Task:      "queue-fifo-1",
		GitRepo:   repo,
		GitBranch: branch,
	})
	if first.Status != protocol.TaskStatusRunning {
		t.Fatalf("expected first task status running, got %q", first.Status)
	}

	second := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:     "worker",
		Task:      "queue-fifo-2",
		GitRepo:   repo,
		GitBranch: branch,
	})
	if second.Status != protocol.TaskStatusQueued {
		t.Fatalf("expected conflicting task to be accepted as queued, got %q", second.Status)
	}
	if got := srv.Scheduler().QueueLen(); got != 1 {
		t.Fatalf("expected scheduler queue length 1, got %d", got)
	}

	// Completing the running task must drain the queued task automatically.
	gate.release("queue-fifo-1")
	waitForCondition(t, func() bool {
		mt, ok := srv.TaskManager().GetTask(second.TaskID)
		return ok && mt.CurrentStatus() == protocol.TaskStatusRunning && srv.Scheduler().QueueLen() == 0
	})

	gate.release("queue-fifo-2")
	waitForCondition(t, func() bool {
		mt, ok := srv.TaskManager().GetTask(second.TaskID)
		return ok && mt.CurrentStatus() == protocol.TaskStatusCompleted
	})
}

func TestServer_TerritoryWarnMode(t *testing.T) {
	gate := newSchedulerGateRunner()
	srv, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeWarn
		cfg.Runner = gate
	})

	const repo = "org/repo-warn"

	first := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:        "worker",
		Task:         "warn-1",
		GitRepo:      repo,
		GitBranch:    "main",
		EditSurfaces: []string{"pkg/auth/*"},
	})
	if first.Status != protocol.TaskStatusRunning {
		t.Fatalf("expected first task status running, got %q", first.Status)
	}

	second := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:        "worker",
		Task:         "warn-2",
		GitRepo:      repo,
		GitBranch:    "feature/warn",
		EditSurfaces: []string{"pkg/auth/login.go"},
	})
	if second.Status != protocol.TaskStatusRunning {
		t.Fatalf("warn mode must accept the conflicting task as running, got %q", second.Status)
	}
	if got := srv.Scheduler().QueueLen(); got != 0 {
		t.Fatalf("warn mode must not queue tasks, got %d", got)
	}

	// The conflicting task must still be told about the collision through a warning.
	// A bounded client timeout prevents this read from hanging if the warning regresses.
	streamClient := &http.Client{Timeout: 5 * time.Second}
	sseResp, err := streamClient.Get(ts.URL + second.EventsURL)
	if err != nil {
		t.Fatalf("GET events failed: %v", err)
	}
	defer sseResp.Body.Close()

	scanner := bufio.NewScanner(sseResp.Body)
	var sawWarning bool
	for !sawWarning {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			t.Fatalf("failed reading warning event: %v", err)
		}
		if evt.Type != protocol.EventThought {
			continue
		}
		var tp protocol.ThoughtPayload
		if err := evt.UnmarshalPayload(&tp); err != nil {
			t.Fatalf("failed unmarshaling thought payload: %v", err)
		}
		if strings.Contains(tp.Text, "territory conflict warning") {
			sawWarning = true
		}
	}

	gate.release("warn-1")
	gate.release("warn-2")
	waitForCondition(t, func() bool { return srv.Scheduler().RunningLen() == 0 })
}

func TestServer_TerritoryDisabledMode(t *testing.T) {
	gate := newSchedulerGateRunner()
	srv, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeDisabled
		cfg.Runner = gate
	})

	const repo = "org/repo-disabled"

	first := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:        "worker",
		Task:         "disabled-1",
		GitRepo:      repo,
		GitBranch:    "main",
		EditSurfaces: []string{"pkg/auth/*"},
	})
	if first.Status != protocol.TaskStatusRunning {
		t.Fatalf("expected first task status running, got %q", first.Status)
	}

	second := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:        "worker",
		Task:         "disabled-2",
		GitRepo:      repo,
		GitBranch:    "feature/disabled",
		EditSurfaces: []string{"pkg/auth/login.go"},
	})
	if second.Status != protocol.TaskStatusRunning {
		t.Fatalf("disabled mode must accept the conflicting task as running, got %q", second.Status)
	}
	if got := srv.Scheduler().QueueLen(); got != 0 {
		t.Fatalf("disabled mode must never queue tasks, got %d", got)
	}
	if got := srv.Scheduler().RunningLen(); got != 2 {
		t.Fatalf("expected both tasks dispatched in disabled mode, got %d running", got)
	}

	gate.release("disabled-1")
	gate.release("disabled-2")
	waitForCondition(t, func() bool { return srv.Scheduler().RunningLen() == 0 })
}

func TestServer_TerritoryQueueMode_CancelQueuedTask(t *testing.T) {
	gate := newSchedulerGateRunner()
	srv, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.TerritoryMode = protocol.TerritoryModeQueue
		cfg.Runner = gate
	})

	const repo = "org/repo-cancel-queue"
	const branch = "feature/cancel-queue"

	first := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:     "worker",
		Task:      "cancel-queue-1",
		GitRepo:   repo,
		GitBranch: branch,
	})
	if first.Status != protocol.TaskStatusRunning {
		t.Fatalf("expected first task status running, got %q", first.Status)
	}

	second := postTaskHTTP(t, ts, protocol.TaskRequest{
		Agent:     "worker",
		Task:      "cancel-queue-2",
		GitRepo:   repo,
		GitBranch: branch,
	})
	if second.Status != protocol.TaskStatusQueued {
		t.Fatalf("expected second task status queued, got %q", second.Status)
	}
	if got := srv.Scheduler().QueueLen(); got != 1 {
		t.Fatalf("expected scheduler queue length 1, got %d", got)
	}

	cancelResp, err := ts.Client().Post(ts.URL+"/v1/tasks/"+second.TaskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("POST cancel failed: %v", err)
	}
	defer cancelResp.Body.Close()

	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK canceling queued task, got %d", cancelResp.StatusCode)
	}

	var cancelBody map[string]string
	if err := json.NewDecoder(cancelResp.Body).Decode(&cancelBody); err != nil {
		t.Fatalf("failed decoding cancel response: %v", err)
	}
	if cancelBody["task_id"] != second.TaskID {
		t.Errorf("expected cancel task_id %q, got %q", second.TaskID, cancelBody["task_id"])
	}
	if cancelBody["status"] != string(protocol.TaskStatusCanceled) {
		t.Errorf("expected cancel status %q, got %q", protocol.TaskStatusCanceled, cancelBody["status"])
	}

	if got := srv.Scheduler().QueueLen(); got != 0 {
		t.Fatalf("expected queued task to be removed from scheduler queue, got %d", got)
	}
	if queued := srv.Scheduler().QueuedTasks(); len(queued) != 0 {
		t.Fatalf("expected empty scheduler queue after cancel, got %v", queued)
	}

	if mt, ok := srv.TaskManager().GetTask(second.TaskID); !ok || mt.CurrentStatus() != protocol.TaskStatusCanceled {
		t.Fatalf("expected canceled queued task, got found=%v", ok)
	}
	if mt, ok := srv.TaskManager().GetTask(first.TaskID); !ok || mt.CurrentStatus() != protocol.TaskStatusRunning {
		t.Fatalf("expected running task to remain unaffected, got found=%v", ok)
	}

	gate.release("cancel-queue-1")
	waitForCondition(t, func() bool { return srv.Scheduler().RunningLen() == 0 })
}
