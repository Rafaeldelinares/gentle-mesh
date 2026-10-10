package runner_test

import (
	"context"
	"encoding/json"
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

// completionRunner emits one completion event with the payload it was given,
// mimicking what a local fallback runner does.
type completionRunner struct {
	payload protocol.CompletionPayload
}

func (r *completionRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	_, err := sink.EmitEvent(protocol.EventCompletion, r.payload)
	return err
}

// recordingSink keeps every event the runner emits.
type recordingSink struct {
	events []protocol.Event
}

func (s *recordingSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	evt, err := protocol.NewEvent(int64(len(s.events)+1), "task-local", eventType, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	s.events = append(s.events, *evt)
	return *evt, nil
}

func (s *recordingSink) Context() context.Context { return context.Background() }

func (s *recordingSink) RegisterQuery(queryID string) <-chan string { return make(chan string) }

// A run performed by the coordinator's local fallback must declare it.
func TestMeshRunner_LocalFallbackMarksCompletion(t *testing.T) {
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       &mockSelector{err: errors.New("no registered node supports the requested agent")},
		FallbackRunner: &completionRunner{payload: protocol.CompletionPayload{Result: "ran locally"}},
	})
	sink := &recordingSink{}

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "local"}, sink); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Type != protocol.EventCompletion {
		t.Fatalf("expected one completion event, got %+v", sink.events)
	}

	var cp protocol.CompletionPayload
	if err := sink.events[0].UnmarshalPayload(&cp); err != nil {
		t.Fatalf("failed to decode completion payload: %v", err)
	}
	if !cp.ExecutedLocally {
		t.Error("a local fallback execution must set executed_locally")
	}
	if cp.Result != "ran locally" {
		t.Errorf("existing fields must survive, got result %q", cp.Result)
	}

	var raw map[string]any
	if err := json.Unmarshal(sink.events[0].Payload, &raw); err != nil {
		t.Fatalf("failed to decode payload as JSON: %v", err)
	}
	if raw["executed_locally"] != true {
		t.Errorf("the serialized payload must carry executed_locally=true, got %v", raw)
	}
}

// A run performed on a mesh worker must keep the completion JSON exactly as it
// was before the marker existed.
func TestMeshRunner_RemoteExecutionKeepsCompletionJSONUnchanged(t *testing.T) {
	const wantPayload = `{"result":"remote done","text":"remote done"}`

	completionEvt, err := protocol.NewEvent(1, "task-remote", protocol.EventCompletion,
		protocol.CompletionPayload{Result: "remote done"})
	if err != nil {
		t.Fatalf("failed to build the completion event: %v", err)
	}
	if got := string(completionEvt.Payload); got != wantPayload {
		t.Fatalf("fixture payload changed: got %s, want %s", got, wantPayload)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(completionEvt.FormatSSE())
	}))
	defer ts.Close()

	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       &mockSelector{selectedNode: &protocol.NodeInfo{NodeID: "node-1", Endpoint: ts.URL}},
		FallbackRunner: &completionRunner{payload: protocol.CompletionPayload{Result: "should not run locally"}},
	})
	sink := &recordingSink{}

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "remote"}, sink); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Type != protocol.EventCompletion {
		t.Fatalf("expected one completion event, got %+v", sink.events)
	}
	if got := string(sink.events[0].Payload); got != wantPayload {
		t.Errorf("a remote execution must keep the payload byte-identical:\n got: %s\nwant: %s", got, wantPayload)
	}
	if strings.Contains(string(sink.events[0].Payload), "executed_locally") {
		t.Error("a remote execution must not be marked as local")
	}

	var cp protocol.CompletionPayload
	if err := sink.events[0].UnmarshalPayload(&cp); err != nil {
		t.Fatalf("failed to decode completion payload: %v", err)
	}
	if cp.ExecutedLocally {
		t.Error("ExecutedLocally must stay false for a remote execution")
	}
}
