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
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
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
	var block []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if len(block) > 0 {
				break
			}
			continue
		}
		block = append(block, line...)
		block = append(block, '\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(block) == 0 {
		return nil, io.EOF
	}
	return protocol.ParseSSE(block)
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

	var conflictResp map[string]string
	_ = json.NewDecoder(resp2.Body).Decode(&conflictResp)
	if !strings.Contains(conflictResp["error"], "branch is locked") {
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
