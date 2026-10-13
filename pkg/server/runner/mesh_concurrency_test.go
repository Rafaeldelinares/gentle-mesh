package runner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// (f) MeshRunner receives 429 from first node, executes on second node (single execution),
// and does not mark the 429 node as unreachable.
func TestMeshRunner_RetryOn429_SingleExecution(t *testing.T) {
	var hitsNode1 int32
	var hitsNode2 int32

	node1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsNode1, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer node1.Close()

	completionEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "done by node 2",
	})
	node2 := streamingWorker(t, *completionEvt, &hitsNode2)
	defer node2.Close()

	selector := &mockSelector{
		nodes: []*protocol.NodeInfo{
			{NodeID: "node-1", Endpoint: node1.URL},
			{NodeID: "node-2", Endpoint: node2.URL},
		},
	}

	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector: selector,
	})

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{Agent: "worker", Task: "work"}

	err := mesh.Run(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("expected MeshRunner to retry on node2 after 429 from node1, got err: %v", err)
	}

	if atomic.LoadInt32(&hitsNode1) != 1 {
		t.Errorf("expected node1 to be called once, got %d", atomic.LoadInt32(&hitsNode1))
	}
	if atomic.LoadInt32(&hitsNode2) != 1 {
		t.Errorf("expected node2 to be executed exactly once, got %d", atomic.LoadInt32(&hitsNode2))
	}
	if len(selector.markedIDs) != 0 {
		t.Errorf("expected node1 NOT to be marked unreachable on 429, marked: %v", selector.markedIDs)
	}
}

// (g) All nodes return 429: fails without local fallback when FallbackRunner is nil,
// and falls back to local runner when FallbackRunner is configured.
func TestMeshRunner_All429_DispatchFailedAndFallback(t *testing.T) {
	node1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	defer node1.Close()

	node2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	defer node2.Close()

	t.Run("without local fallback (-local-fallback=false)", func(t *testing.T) {
		selector := &mockSelector{
			nodes: []*protocol.NodeInfo{
				{NodeID: "node-1", Endpoint: node1.URL},
				{NodeID: "node-2", Endpoint: node2.URL},
			},
		}

		mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       selector,
			FallbackRunner: nil, // local fallback disabled
		})

		sink := newMockSink(context.Background())
		req := protocol.TaskRequest{Agent: "worker", Task: "work"}

		err := mesh.Run(context.Background(), req, sink)
		if err == nil {
			t.Fatal("expected error when all nodes return 429 and fallback is disabled")
		}
		if !strings.Contains(err.Error(), "no mesh worker available") && !strings.Contains(err.Error(), "status 429") {
			t.Errorf("expected dispatch failure error, got: %v", err)
		}
		if len(selector.markedIDs) != 0 {
			t.Errorf("expected no nodes to be marked unreachable on 429, marked: %v", selector.markedIDs)
		}
	})

	t.Run("with local fallback (-local-fallback=true)", func(t *testing.T) {
		selector := &mockSelector{
			nodes: []*protocol.NodeInfo{
				{NodeID: "node-1", Endpoint: node1.URL},
				{NodeID: "node-2", Endpoint: node2.URL},
			},
		}

		fallback := &mockRunner{}
		mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       selector,
			FallbackRunner: fallback,
		})

		sink := newMockSink(context.Background())
		req := protocol.TaskRequest{Agent: "worker", Task: "work"}

		err := mesh.Run(context.Background(), req, sink)
		if err != nil {
			t.Fatalf("unexpected error with fallback runner: %v", err)
		}
		if !fallback.called {
			t.Error("expected fallback runner to be called when all nodes are busy (429)")
		}
	})
}

// (h) MeshRunner releases the inFlight reservation of a node returning 429
// immediately before the next attempt reserves another node.
type recordingReserver struct {
	mu     sync.Mutex
	nodes  []*protocol.NodeInfo
	events []string
}

func (r *recordingReserver) SelectAndReserve(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.nodes) == 0 {
		return nil, errors.New("no nodes available")
	}
	n := r.nodes[0]
	r.nodes = r.nodes[1:]
	r.events = append(r.events, "reserve:"+n.NodeID)
	return n, nil
}

func (r *recordingReserver) Release(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "release:"+nodeID)
}

func (r *recordingReserver) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	return r.SelectAndReserve(agent, requiredTags...)
}

func (r *recordingReserver) MarkNodeUnreachable(nodeID string) error {
	return nil
}

func TestMeshRunner_429ReleasesReservationBeforeNextAttempt(t *testing.T) {
	node1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	defer node1.Close()

	var hitsNode2 int32
	completionEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "done by node 2",
	})
	node2 := streamingWorker(t, *completionEvt, &hitsNode2)
	defer node2.Close()

	reserver := &recordingReserver{
		nodes: []*protocol.NodeInfo{
			{NodeID: "node-1", Endpoint: node1.URL},
			{NodeID: "node-2", Endpoint: node2.URL},
		},
	}

	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector: reserver,
	})

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{Agent: "worker", Task: "work"}

	err := mesh.Run(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reserver.mu.Lock()
	events := append([]string(nil), reserver.events...)
	reserver.mu.Unlock()

	// Verify exact sequence: reserve:node-1, release:node-1 (must happen before reserve:node-2!), reserve:node-2, release:node-2
	want := []string{"reserve:node-1", "release:node-1", "reserve:node-2", "release:node-2"}
	if len(events) != len(want) {
		t.Fatalf("expected events %v, got %v", want, events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("at index %d: want %q, got %q (all events: %v)", i, want[i], events[i], events)
		}
	}
}

// (i) Receiving a queued status event when the coordinator task is already running
// fails with ErrInvalidStateTransition because running -> queued is an illegal transition.
func TestTaskStateMachine_QueuedEventWhileRunningReturnsInvalidTransition(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("new task manager: %v", err)
	}
	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "test"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := mt.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("transition to running: %v", err)
	}

	// Emitting queued status event while already running
	_, err = mt.EmitEvent(protocol.EventStatus, protocol.StatusPayload{
		Status:  protocol.TaskStatusQueued,
		Message: "task queued waiting for execution slot",
	})
	if !errors.Is(err, task.ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition, got: %v", err)
	}
}
