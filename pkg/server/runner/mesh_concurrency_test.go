package runner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
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
