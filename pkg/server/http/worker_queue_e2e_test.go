package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/worker"
)

type controlledRunner struct {
	mu        sync.Mutex
	blockTask string
	unblockCh chan struct{}
	calls     map[string]int
}

func (r *controlledRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	taskKey := req.Task
	r.calls[taskKey]++
	shouldBlock := (taskKey == r.blockTask)
	unblock := r.unblockCh
	r.mu.Unlock()

	_, _ = sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning})

	if shouldBlock && unblock != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-unblock:
		}
	}

	_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "success: " + req.Task})
	return nil
}

func postTask(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, raw)
	}
	var res protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res.TaskID
}

func getTask(t *testing.T, ts *httptest.Server, id string) protocol.TaskState {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + id)
	if err != nil {
		t.Fatalf("GET /v1/tasks/%s: %v", id, err)
	}
	defer resp.Body.Close()
	var st protocol.TaskState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return st
}

// TestWorkerQueueE2E_QueuedTaskAttributionAndCompletion verifies end-to-end:
// Real meshhttp.NewServer + real worker.NewServer with Concurrency=1.
// Task 1 occupies the single execution slot.
// Task 2 arrives and enters worker's queueSem.
// Task 2 immediately has worker_id visible while waiting in queue.
// GET /v1/tasks/{id} returns Status == running (never regressed to queued).
// When task 1 finishes, task 2 acquires execution slot and completes successfully.
func TestWorkerQueueE2E_QueuedTaskAttributionAndCompletion(t *testing.T) {
	unblock1 := make(chan struct{})
	cr := &controlledRunner{
		blockTask: "task-first",
		unblockCh: unblock1,
	}

	workerSrv := worker.NewServer(worker.ServerConfig{
		Concurrency:  1,
		QueueSize:    2,
		QueueTimeout: 10 * time.Second,
		Runner:       cr,
	})

	workerTS := httptest.NewServer(workerSrv.Handler())
	defer workerTS.Close()

	reg := registry.NewRegistry(5 * time.Second)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}

	coordSrv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	coordTS := httptest.NewServer(coordSrv.Handler())
	defer coordTS.Close()

	const nodeID = "worker-node-1"
	_, err = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         nodeID,
		Endpoint:       workerTS.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}

	// 1. Dispatch Task 1 (which blocks on unblock1)
	id1 := postTask(t, coordTS, `{"agent":"worker","task":"task-first","git_repo":"repo","git_branch":"b1"}`)

	// Wait until task 1 is running and attributed
	var st1 protocol.TaskState
	waitRetryFor(t, 5*time.Second, "task 1 running", func() bool {
		st1 = getTask(t, coordTS, id1)
		return st1.Status == protocol.TaskStatusRunning && st1.WorkerID == nodeID
	})

	// 2. Dispatch Task 2 while Task 1 is still blocking the worker's sole execution slot
	id2 := postTask(t, coordTS, `{"agent":"worker","task":"task-second","git_repo":"repo","git_branch":"b2"}`)

	// Task 2 is accepted into worker queue. It must immediately have worker_id visible,
	// and its status must be running on coordinator (never queued after running).
	var st2 protocol.TaskState
	waitRetryFor(t, 5*time.Second, "task 2 queued on worker and attributed", func() bool {
		st2 = getTask(t, coordTS, id2)
		return st2.WorkerID == nodeID && st2.Status == protocol.TaskStatusRunning
	})

	if st2.WorkerID != nodeID {
		t.Fatalf("expected task 2 to have worker_id %q while queued, got %q", nodeID, st2.WorkerID)
	}
	if st2.Status != protocol.TaskStatusRunning {
		t.Fatalf("expected task 2 status to be running, got %q", st2.Status)
	}

	// 3. Unblock Task 1
	close(unblock1)

	// Task 1 must complete
	waitRetryFor(t, 5*time.Second, "task 1 completed", func() bool {
		s := getTask(t, coordTS, id1)
		return s.Status == protocol.TaskStatusCompleted
	})

	// Task 2 must now acquire the execution slot and complete
	waitRetryFor(t, 5*time.Second, "task 2 completed", func() bool {
		s := getTask(t, coordTS, id2)
		return s.Status == protocol.TaskStatusCompleted
	})

	finalSt2 := getTask(t, coordTS, id2)
	if finalSt2.Status != protocol.TaskStatusCompleted {
		t.Fatalf("expected task 2 status completed, got %s", finalSt2.Status)
	}
	if finalSt2.WorkerID != nodeID {
		t.Fatalf("expected task 2 worker_id %q, got %q", nodeID, finalSt2.WorkerID)
	}
}

type timeoutRunner struct {
	callsTask1 *atomic.Int32
	callsTask2 *atomic.Int32
	unblock    chan struct{}
}

func (r *timeoutRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	if req.Task == "blocker" {
		r.callsTask1.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.unblock:
			_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "blocker done"})
			return nil
		}
	}
	r.callsTask2.Add(1)
	_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})
	return nil
}

// TestWorkerQueueE2E_QueueTimeoutNotRetriedWithMaxRetries verifies:
// When a task times out waiting in the worker queue, the worker emits QUEUE_TIMEOUT with Fatal: true.
// The coordinator marks the task as failed with Code: QUEUE_TIMEOUT.
// Even with max_retries=1, the task is NOT retried because terminal causes are never retried.
func TestWorkerQueueE2E_QueueTimeoutNotRetriedWithMaxRetries(t *testing.T) {
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	cleanupUnblock := func() { unblockOnce.Do(func() { close(unblock) }) }
	defer cleanupUnblock()

	var callsTask1, callsTask2 atomic.Int32

	workerSrv := worker.NewServer(worker.ServerConfig{
		Concurrency:  1,
		QueueSize:    2,
		QueueTimeout: 150 * time.Millisecond, // Fast queue timeout
		Runner: &timeoutRunner{
			callsTask1: &callsTask1,
			callsTask2: &callsTask2,
			unblock:    unblock,
		},
	})

	workerTS := httptest.NewServer(workerSrv.Handler())
	defer workerTS.Close()

	reg := registry.NewRegistry(5 * time.Second)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}

	coordSrv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		Registry:         reg,
		TaskManager:      mgr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	coordTS := httptest.NewServer(coordSrv.Handler())
	defer coordTS.Close()

	const nodeID = "worker-timeout-node"
	_, err = reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         nodeID,
		Endpoint:       workerTS.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}

	// 1. Start Blocker Task
	idBlocker := postTask(t, coordTS, `{"agent":"worker","task":"blocker","git_repo":"r","git_branch":"b1"}`)
	waitRetryFor(t, 5*time.Second, "blocker running", func() bool {
		st := getTask(t, coordTS, idBlocker)
		return st.Status == protocol.TaskStatusRunning && st.WorkerID == nodeID && callsTask1.Load() == 1
	})

	// 2. Start Task with max_retries=1 that will sit in queue and timeout
	idTimeout := postTask(t, coordTS, `{"agent":"worker","task":"timeout-task","git_repo":"r","git_branch":"b2","max_retries":1,"retry_delay_seconds":1}`)

	// Wait for task to fail with QUEUE_TIMEOUT
	var stTimeout protocol.TaskState
	waitRetryFor(t, 5*time.Second, "task failed with QUEUE_TIMEOUT", func() bool {
		stTimeout = getTask(t, coordTS, idTimeout)
		return stTimeout.Status == protocol.TaskStatusFailed
	})

	if stTimeout.Error == nil || stTimeout.Error.Code != "QUEUE_TIMEOUT" {
		t.Fatalf("expected error code QUEUE_TIMEOUT, got %+v", stTimeout.Error)
	}

	// Wait 1.5s to verify no retry is scheduled (max_retries=1 must not resurrect a QUEUE_TIMEOUT)
	time.Sleep(1500 * time.Millisecond)

	afterState := getTask(t, coordTS, idTimeout)
	if afterState.Status != protocol.TaskStatusFailed {
		t.Fatalf("task was resurrected by retry, status is %s", afterState.Status)
	}
	if calls := callsTask2.Load(); calls != 0 {
		t.Fatalf("task was executed %d times, expected 0 (timed out in queue)", calls)
	}

	cleanupUnblock()
}
