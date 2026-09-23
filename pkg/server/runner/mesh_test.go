package runner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

type mockSelector struct {
	selectedNode *protocol.NodeInfo
	err          error
	calledAgent  string
	calledTags   []string
}

func (s *mockSelector) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	s.calledAgent = agent
	s.calledTags = requiredTags
	return s.selectedNode, s.err
}

type mockRunner struct {
	calledReq protocol.TaskRequest
	called    bool
	err       error
}

func (m *mockRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	m.called = true
	m.calledReq = req
	return m.err
}

func TestMeshRunner_SelectNodeSuccess(t *testing.T) {
	completionEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "worker completed task",
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(completionEvt.FormatSSE())
	}))
	defer ts.Close()

	selector := &mockSelector{
		selectedNode: &protocol.NodeInfo{
			NodeID:   "node-gpu-1",
			Endpoint: ts.URL,
		},
	}

	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       selector,
		FallbackRunner: fallback,
		Client:         ts.Client(),
		Token:          "mesh-tok",
	})

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{
		Agent: "gpu-worker",
		Task:  "Run embeddings",
		Tags:  []string{"gpu", "cuda"},
	}

	err := mesh.Run(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if selector.calledAgent != "gpu-worker" {
		t.Errorf("expected calledAgent 'gpu-worker', got %q", selector.calledAgent)
	}
	if len(selector.calledTags) != 2 || selector.calledTags[0] != "gpu" || selector.calledTags[1] != "cuda" {
		t.Errorf("expected calledTags ['gpu', 'cuda'], got %v", selector.calledTags)
	}
	if fallback.called {
		t.Error("expected fallback runner NOT to be called when remote node is selected")
	}

	events := sink.Events()
	if len(events) != 1 || events[0].Type != protocol.EventCompletion {
		t.Errorf("expected completion event, got %+v", events)
	}
}

func TestMeshRunner_FallbackWhenNoNodeAvailable(t *testing.T) {
	errNoNode := errors.New("no nodes available with matching tags")
	selector := &mockSelector{
		err: errNoNode,
	}

	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       selector,
		FallbackRunner: fallback,
	})

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{
		Agent: "heavy-worker",
		Task:  "Compile binary",
		Tags:  []string{"arm64"},
	}

	err := mesh.Run(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fallback.called {
		t.Error("expected fallback runner to be called")
	}
	if fallback.calledReq.Agent != req.Agent {
		t.Errorf("expected fallback called with agent %q, got %q", req.Agent, fallback.calledReq.Agent)
	}
}

func TestMeshRunner_ErrorWithoutFallback(t *testing.T) {
	errNoNode := errors.New("err_no_node_available")
	selector := &mockSelector{
		err: errNoNode,
	}

	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       selector,
		FallbackRunner: nil,
	})

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{
		Agent: "vision-worker",
		Task:  "Process images",
		Tags:  []string{"vram-16g"},
	}

	err := mesh.Run(context.Background(), req, sink)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	expectedPrefix := `no mesh worker available for task (agent: "vision-worker", tags: [vram-16g]):`
	if !strings.HasPrefix(err.Error(), expectedPrefix) {
		t.Errorf("expected error starting with %q, got %q", expectedPrefix, err.Error())
	}
	if !errors.Is(err, errNoNode) {
		t.Errorf("expected wrapped error to be errNoNode (%v), got: %v", errNoNode, err)
	}
}

func TestMeshRunner_Triangulation(t *testing.T) {
	t.Run("nil selector falls back to fallback runner", func(t *testing.T) {
		fallback := &mockRunner{}
		mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       nil,
			FallbackRunner: fallback,
		})

		sink := newMockSink(context.Background())
		req := protocol.TaskRequest{Agent: "worker"}
		err := mesh.Run(context.Background(), req, sink)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fallback.called {
			t.Error("expected fallback runner to be called")
		}
	})

	t.Run("nil selector without fallback returns error", func(t *testing.T) {
		mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       nil,
			FallbackRunner: nil,
		})

		sink := newMockSink(context.Background())
		req := protocol.TaskRequest{Agent: "worker"}
		err := mesh.Run(context.Background(), req, sink)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("selector returns nil node and nil error falls back to fallback", func(t *testing.T) {
		selector := &mockSelector{
			selectedNode: nil,
			err:          nil,
		}
		fallback := &mockRunner{}
		mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       selector,
			FallbackRunner: fallback,
		})

		sink := newMockSink(context.Background())
		req := protocol.TaskRequest{Agent: "worker"}
		err := mesh.Run(context.Background(), req, sink)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fallback.called {
			t.Error("expected fallback runner to be called")
		}
	})
}
