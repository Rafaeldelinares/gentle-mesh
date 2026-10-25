package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

func getLFBTask(ts *httptest.Server, id string) map[string]any {
	resp, err := http.Get(ts.URL + "/v1/tasks/" + id)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var d map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&d)
	return d
}

// waitFor polls predicate every 10 ms until it returns true or timeout elapses.
// Returns true iff predicate observed true within the timeout.
func waitFor(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate()
}

// TestSilentNodeTasksFailAsNODELOST verifies finding 29: when a registered node
// stops heartbeating and the registry prunes it to offline, every running task
// attributed to that node must transition to failed with error.code=NODE_LOST
// and the SSE request dispatched to the node must be cancelled (the handler
// returns). The test fails if the task is still running, if the error code is
// not NODE_LOST, or if the node's /v1/execute handler is still blocked when
// the poll deadline expires.
func TestSilentNodeTasksFailAsNODELOST(t *testing.T) {
	const heartbeatTimeout = 200 * time.Millisecond
	const nodeID = "silent-node"

	// handlerReturned is closed when the simulated node's handler returns (its
	// /v1/execute request ended because the server cancelled the runner's HTTP
	// context after the sweep finalized the task). The test asserts this close.
	handlerReturned := make(chan struct{})
	// fallbackExit is closed by t.Cleanup so the handler always returns and the
	// goroutine does not leak even when the request cancellation is delayed.
	fallbackExit := make(chan struct{})
	nodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		// Use the task identifier received in /v1/execute (carried via
		// idempotency_key in the request body) as the TaskID on the protocol
		// event so the server can correlate events with the task.
		evt, _ := protocol.NewEvent(1, req.IdempotencyKey, protocol.EventStatus,
			protocol.StatusPayload{Status: "running"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evt.FormatSSE())
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			// The server cancelled the runner's HTTP context after the sweep
			// failed the task: the request is closed, the handler returns.
		case <-fallbackExit:
			// Test cleanup forced exit; the goroutine must not leak.
		}
	}))
	t.Cleanup(func() { close(fallbackExit); nodeTS.Close() })

	reg := registry.NewRegistry(heartbeatTimeout)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	srvHelper, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: heartbeatTimeout,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srvHelper.Handler())
	defer ts.Close()

	if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         nodeID,
		Endpoint:       nodeTS.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Heartbeat goroutine: refreshes every 50ms until "stop" is closed.
	stopHB := make(chan struct{})
	var closeOnce sync.Once
	stopHeartbeats := func() { closeOnce.Do(func() { close(stopHB) }) }
	t.Cleanup(stopHeartbeats)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ticker.C:
				_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
					NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
				})
			}
		}
	}()

	if err := reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
	}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// POST a task with idempotency_key so the node receives a non-empty task
	// identifier in the /v1/execute body.
	const idempKey = "silent-node-task-1"
	resp, err := http.Post(ts.URL+"/v1/tasks", "application/json",
		strings.NewReader(`{"agent":"worker","task":"silent","git_repo":"toy/repo","git_branch":"29","idempotency_key":"`+idempKey+`"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var tr struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	if tr.TaskID == "" {
		t.Fatal("POST returned no task_id")
	}

	// Poll (2 s, every 10 ms) until running with worker_id=silent-node.
	var state map[string]any
	reachedRunning := waitFor(2*time.Second, func() bool {
		// Refresh heartbeat manually (the node is "alive" until we go silent).
		_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
			NodeID: nodeID, ActiveTasks: 1, Timestamp: time.Now().Add(time.Second).Unix(),
		})
		s := getLFBTask(ts, tr.TaskID)
		st, _ := s["status"].(string)
		wid, _ := s["worker_id"].(string)
		if st == "running" && wid == nodeID {
			state = s
			return true
		}
		return false
	})
	if !reachedRunning || state == nil {
		t.Fatalf("task never reached running with worker_id=%s within 2s", nodeID)
	}

	// Node goes silent: stop refreshing heartbeats.
	stopHeartbeats()
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: nodeID, ActiveTasks: 1, Timestamp: time.Now().Unix(),
	})

	// Poll (5 s, every 10 ms) until the registry reports the node offline.
	offlineFound := waitFor(5*time.Second, func() bool {
		reg.PruneStaleNodes()
		for _, n := range reg.ListNodes() {
			if n.Status == protocol.NodeStatusOffline {
				return true
			}
		}
		return false
	})
	if !offlineFound {
		t.Fatalf("node %s never went offline within 5s", nodeID)
	}

	// Invoke the sweep (only on the branch with the fix).
	srvHelper.FailTasksOfOfflineNodes()

	// Poll (2 s, every 10 ms) until failed NODE_LOST.
	var lastStatus, lastCode string
	failObserved := waitFor(2*time.Second, func() bool {
		s := getLFBTask(ts, tr.TaskID)
		lastStatus, _ = s["status"].(string)
		if lastStatus == "failed" {
			e, _ := s["error"].(map[string]any)
			lastCode, _ = e["code"].(string)
			return lastCode == "NODE_LOST"
		}
		return false
	})
	if !failObserved {
		t.Fatalf("expected status=failed code=NODE_LOST, got status=%s code=%s (finding 29 not fixed)", lastStatus, lastCode)
	}

	// Poll (2 s, every 10 ms) until the node's /v1/execute request is closed.
	// The handler returns when the server cancels the runner's HTTP context.
	nodeRequestClosed := waitFor(2*time.Second, func() bool {
		select {
		case <-handlerReturned:
			return true
		default:
			return false
		}
	})
	if !nodeRequestClosed {
		t.Fatal("node httptest did not see its /v1/execute request closed within 2s")
	}
}

// (2) TestSilentNodeLeavesOtherNodesAndUnattributedTasksIntact:
// Un barrido solo afecta a las tareas running del nodo offline; tareas de otro nodo
// o tareas sin worker_id permanecen intactas.
func TestSilentNodeLeavesOtherNodesAndUnattributedTasksIntact(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	reg := registry.NewRegistry(time.Hour)
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:    t.TempDir(),
		Registry:    reg,
		TaskManager: mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Tarea de otro nodo
	t1, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "other-node"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = t1.TransitionTo(protocol.TaskStatusRunning)
	t1.AttributeNode("node-healthy")

	// Tarea sin worker_id
	t2, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "unattributed"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = t2.TransitionTo(protocol.TaskStatusRunning)

	// Tarea del nodo offline
	t3, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "silent"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = t3.TransitionTo(protocol.TaskStatusRunning)
	t3.AttributeNode("node-silent")

	// Registrar nodo offline
	_, _ = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID: "node-silent", Endpoint: "http://127.0.0.1:9", MaxConcurrency: 1,
	})
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: "node-silent", Timestamp: time.Now().Add(-10 * time.Hour).Unix(),
	})
	reg.PruneStaleNodes()

	// Registrar nodo sano
	_, _ = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID: "node-healthy", Endpoint: "http://127.0.0.1:9", MaxConcurrency: 1,
	})

	finalized := srv.FailTasksOfOfflineNodes()
	if finalized != 1 {
		t.Fatalf("expected 1 finalized task, got %d", finalized)
	}

	if t1.CurrentStatus() != protocol.TaskStatusRunning {
		t.Errorf("t1 (healthy node) should stay running, got %s", t1.CurrentStatus())
	}
	if t2.CurrentStatus() != protocol.TaskStatusRunning {
		t.Errorf("t2 (unattributed) should stay running, got %s", t2.CurrentStatus())
	}
	if t3.CurrentStatus() != protocol.TaskStatusFailed {
		t.Errorf("t3 (silent node) should fail, got %s", t3.CurrentStatus())
	}
	snap3 := t3.Snapshot()
	if snap3.Error == nil || snap3.Error.Code != "NODE_LOST" {
		t.Errorf("t3 error should be NODE_LOST, got %+v", snap3.Error)
	}
}

// (3) TestSilentNodeLeavesTerminalTasksUnchanged:
// Una tarea ya completada, cancelada o fallada previamente no es modificada ni sobreescrita.
func TestSilentNodeLeavesTerminalTasksUnchanged(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	reg := registry.NewRegistry(time.Hour)
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:    t.TempDir(),
		Registry:    reg,
		TaskManager: mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Tarea completada con su resultado
	t1, _ := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "completed"})
	_ = t1.TransitionTo(protocol.TaskStatusRunning)
	t1.AttributeNode("node-silent")
	_, _ = t1.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "original-result"})

	// Tarea fallada previamente con su causa original
	t2, _ := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "failed"})
	_ = t2.TransitionTo(protocol.TaskStatusRunning)
	t2.AttributeNode("node-silent")
	_ = mgr.FailTask(t2.TaskID, "USER_ABORT", "aborted by user")

	_, _ = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID: "node-silent", Endpoint: "http://127.0.0.1:9", MaxConcurrency: 1,
	})
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: "node-silent", Timestamp: time.Now().Add(-10 * time.Hour).Unix(),
	})
	reg.PruneStaleNodes()

	finalized := srv.FailTasksOfOfflineNodes()
	if finalized != 0 {
		t.Fatalf("expected 0 finalized tasks for terminal tasks, got %d", finalized)
	}

	snap1 := t1.Snapshot()
	if snap1.Status != protocol.TaskStatusCompleted || snap1.Completion == nil || snap1.Completion.Result != "original-result" {
		t.Errorf("t1 should keep original completion, got %+v", snap1)
	}

	snap2 := t2.Snapshot()
	if snap2.Status != protocol.TaskStatusFailed || snap2.Error == nil || snap2.Error.Code != "USER_ABORT" {
		t.Errorf("t2 should keep original error USER_ABORT, got %+v", snap2.Error)
	}
}

// (4) TestSilentNodeConcurrentEOFAndSweepSingleTransitionAndEvent:
// Concurrencia entre el sweep de offline y un cierre/fallo simultáneo: exactamente
// una transición terminal y un único error registrado sin data race ni duplicación.
func TestSilentNodeConcurrentEOFAndSweepSingleTransitionAndEvent(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "race"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = mt.TransitionTo(protocol.TaskStatusRunning)
	mt.AttributeNode("node-race")

	sub, unsub, err := mgr.Subscribe(mt.TaskID, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsub()

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Goroutine 1: EOF desde el runner
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = mgr.FailTask(mt.TaskID, "NODE_LOST", "stream ended")
	}()

	// Goroutine 2: barrido de nodos offline
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = mgr.FailTasksOfNode("node-race", "NODE_LOST", "node went offline")
	}()

	close(start)
	wg.Wait()

	if mt.CurrentStatus() != protocol.TaskStatusFailed {
		t.Fatalf("expected failed status, got %s", mt.CurrentStatus())
	}

	// Contar eventos terminales emitidos por el suscriptor
	var terminalCount int
	timeout := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case evt, ok := <-sub:
			if !ok {
				break drain
			}
			if evt.Type == protocol.EventError {
				terminalCount++
			}
		case <-timeout:
			break drain
		}
	}

	if terminalCount != 1 {
		t.Errorf("expected exactly 1 EventError event, got %d", terminalCount)
	}
}

// (6) TestSilentNodeNoRetryWithMaxRetries:
// Una tarea fallada por NODE_LOST no debe ser reintentada aunque tenga max_retries > 0.
func TestSilentNodeNoRetryWithMaxRetries(t *testing.T) {
	const heartbeatTimeout = 200 * time.Millisecond
	const nodeID = "silent-retry"

	fallbackExit := make(chan struct{})
	nodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		evt, _ := protocol.NewEvent(1, req.IdempotencyKey, protocol.EventStatus,
			protocol.StatusPayload{Status: "running"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evt.FormatSSE())
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-fallbackExit:
		}
	}))
	t.Cleanup(func() { close(fallbackExit); nodeTS.Close() })

	reg := registry.NewRegistry(heartbeatTimeout)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	srvHelper, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: heartbeatTimeout,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srvHelper.Handler())
	defer ts.Close()

	_, _ = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         nodeID,
		Endpoint:       nodeTS.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})

	stopHB := make(chan struct{})
	var closeOnce sync.Once
	stopHeartbeats := func() { closeOnce.Do(func() { close(stopHB) }) }
	t.Cleanup(stopHeartbeats)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ticker.C:
				_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
					NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
				})
			}
		}
	}()

	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
	})

	resp, err := http.Post(ts.URL+"/v1/tasks", "application/json",
		strings.NewReader(`{"agent":"worker","task":"retry-test","git_repo":"toy/repo","git_branch":"29r","max_retries":3,"retry_delay_seconds":1}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var tr struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tr)

	ok := waitFor(2*time.Second, func() func() bool {
		return func() bool {
			_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
				NodeID: nodeID, ActiveTasks: 1, Timestamp: time.Now().Add(time.Second).Unix(),
			})
			s := getLFBTask(ts, tr.TaskID)
			st, _ := s["status"].(string)
			wid, _ := s["worker_id"].(string)
			return st == "running" && wid == nodeID
		}
	}())
	if !ok {
		t.Fatalf("task never reached running with worker_id=%s within 2s", nodeID)
	}

	stopHeartbeats()

	// Poner nodo offline y barrer
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: nodeID, ActiveTasks: 1, Timestamp: time.Now().Add(-10 * time.Second).Unix(),
	})
	reg.PruneStaleNodes()

	srvHelper.FailTasksOfOfflineNodes()

	// La tarea debe pasar inmediatamente a failed NODE_LOST
	var lastStatus, lastCode string
	ok = waitFor(2*time.Second, func() bool {
		s := getLFBTask(ts, tr.TaskID)
		lastStatus, _ = s["status"].(string)
		if lastStatus == "failed" {
			errMap, _ := s["error"].(map[string]any)
			lastCode, _ = errMap["code"].(string)
			return lastCode == "NODE_LOST"
		}
		return false
	})
	if !ok {
		t.Fatalf("expected status=failed code=NODE_LOST immediately without retry, got status=%s code=%s", lastStatus, lastCode)
	}

	// Esperar 1.2s para asegurar que ningún reintento la resucita
	time.Sleep(1200 * time.Millisecond)
	s2 := getLFBTask(ts, tr.TaskID)
	st2, _ := s2["status"].(string)
	if st2 != "failed" {
		t.Fatalf("task was resurrected by a retry, got %s", st2)
	}
}

// (7) TestSilentNodeRealHeartbeatExpiryCadena:
// Cadena real sin atajos de timestamp: latidos periódicos cada 50ms con timeout 200ms,
// se detienen los latidos (sin enviar timestamps pasados artificiales), se sondea con
// PruneStaleNodes() hasta que el paso natural del tiempo (>= 3 * 200ms = 600ms) marque
// el nodo como offline, se invoca el barrido y se verifica que la tarea pasa a failed NODE_LOST
// y la conexión HTTP del nodo se cierra.
func TestSilentNodeRealHeartbeatExpiryCadena(t *testing.T) {
	const heartbeatTimeout = 200 * time.Millisecond
	const nodeID = "silent-cadena"

	handlerReturned := make(chan struct{})
	fallbackExit := make(chan struct{})
	nodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &req)
		evt, _ := protocol.NewEvent(1, req.IdempotencyKey, protocol.EventStatus,
			protocol.StatusPayload{Status: "running"})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evt.FormatSSE())
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-fallbackExit:
		}
	}))
	t.Cleanup(func() { close(fallbackExit); nodeTS.Close() })

	reg := registry.NewRegistry(heartbeatTimeout)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	srvHelper, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: heartbeatTimeout,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srvHelper.Handler())
	defer ts.Close()

	if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         nodeID,
		Endpoint:       nodeTS.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	stopHB := make(chan struct{})
	var closeOnce sync.Once
	stopHeartbeats := func() { closeOnce.Do(func() { close(stopHB) }) }
	t.Cleanup(stopHeartbeats)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ticker.C:
				_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
					NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
				})
			}
		}
	}()

	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID: nodeID, ActiveTasks: 0, Timestamp: time.Now().Add(time.Second).Unix(),
	})

	resp, err := http.Post(ts.URL+"/v1/tasks", "application/json",
		strings.NewReader(`{"agent":"worker","task":"cadena","git_repo":"toy/repo","git_branch":"29c"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var tr struct {
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	if tr.TaskID == "" {
		t.Fatal("POST returned no task_id")
	}

	startWait := time.Now()
	reachedRunning := waitFor(2*time.Second, func() bool {
		_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
			NodeID: nodeID, ActiveTasks: 1, Timestamp: time.Now().Add(time.Second).Unix(),
		})
		s := getLFBTask(ts, tr.TaskID)
		st, _ := s["status"].(string)
		wid, _ := s["worker_id"].(string)
		return st == "running" && wid == nodeID
	})
	if !reachedRunning {
		t.Fatalf("task never reached running with worker_id=%s within 2s", nodeID)
	}

	// Detener los latidos por completo (sin enviar ningún heartbeat artificial)
	stopHeartbeats()
	silentAt := time.Now()

	// Sondear hasta que PruneStaleNodes marque el nodo como offline por expiración real
	offlineFound := waitFor(5*time.Second, func() bool {
		reg.PruneStaleNodes()
		for _, n := range reg.ListNodes() {
			if n.NodeID == nodeID && n.Status == protocol.NodeStatusOffline {
				return true
			}
		}
		return false
	})
	offlineDuration := time.Since(silentAt)
	if !offlineFound {
		t.Fatalf("node %s never went offline within 5s", nodeID)
	}
	if offlineDuration < 600*time.Millisecond {
		t.Fatalf("node went offline in %v, expected at least 600ms (3 * 200ms)", offlineDuration)
	}

	// Invocar el barrido de nodos offline
	srvHelper.FailTasksOfOfflineNodes()

	// Sondear hasta que la tarea pase a failed NODE_LOST
	var lastStatus, lastCode string
	failObserved := waitFor(2*time.Second, func() bool {
		s := getLFBTask(ts, tr.TaskID)
		lastStatus, _ = s["status"].(string)
		if lastStatus == "failed" {
			e, _ := s["error"].(map[string]any)
			lastCode, _ = e["code"].(string)
			return lastCode == "NODE_LOST"
		}
		return false
	})
	if !failObserved {
		t.Fatalf("expected status=failed code=NODE_LOST, got status=%s code=%s (finding 29 not fixed)", lastStatus, lastCode)
	}

	// Sondear hasta que la petición del nodo se cierre (handler returned)
	nodeRequestClosed := waitFor(2*time.Second, func() bool {
		select {
		case <-handlerReturned:
			return true
		default:
			return false
		}
	})
	if !nodeRequestClosed {
		t.Fatal("node httptest did not see its /v1/execute request closed within 2s")
	}
	t.Logf("cadena real completada con exito: expiracion offline en %v (total %v)", offlineDuration, time.Since(startWait))
}
