package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/registry"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/runner"
)

type mockSelector struct {
	selectedNode *protocol.NodeInfo
	err          error
	calledAgent  string
	calledTags   []string

	// nodes, when non-empty, is consumed in order before falling back to
	// selectedNode: it models "the first pick is dead, the next one is alive".
	nodes       []*protocol.NodeInfo
	selectCalls int
	markedIDs   []string
}

func (s *mockSelector) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	s.calledAgent = agent
	s.calledTags = requiredTags
	s.selectCalls++
	if len(s.nodes) > 0 {
		node := s.nodes[0]
		s.nodes = s.nodes[1:]
		return node, nil
	}
	return s.selectedNode, s.err
}

// MarkNodeUnreachable records the nodes the router took out of rotation.
func (s *mockSelector) MarkNodeUnreachable(nodeID string) error {
	s.markedIDs = append(s.markedIDs, nodeID)
	return nil
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

// closedEndpoint reserves a port and closes it, so the very first connection fails with
// "connection refused": the node never accepted the task.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

// streamingWorker answers every request with one SSE event and counts the hits.
func streamingWorker(t *testing.T, evt protocol.Event, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evt.FormatSSE())
	}))
}

// A worker that never accepted the task (transport failure before acceptance) is retired
// and the task is offered to the next node, which runs it exactly once.
func TestMeshRunner_RetriesAnotherNodeWhenTheWorkerNeverAccepted(t *testing.T) {
	var hits int32
	completion, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "ran on node-b",
	})
	alive := streamingWorker(t, *completion, &hits)
	defer alive.Close()

	selector := &mockSelector{nodes: []*protocol.NodeInfo{
		{NodeID: "node-a", Endpoint: closedEndpoint(t)},
		{NodeID: "node-b", Endpoint: alive.URL},
	}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector})
	sink := newMockSink(context.Background())

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, sink); err != nil {
		t.Fatalf("the task should have run on the second node, got: %v", err)
	}
	if selector.selectCalls != 2 {
		t.Errorf("expected 2 selections (dead node, then alive node), got %d", selector.selectCalls)
	}
	if len(selector.markedIDs) != 1 || selector.markedIDs[0] != "node-a" {
		t.Errorf("expected node-a to be marked unreachable, got %v", selector.markedIDs)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected the alive node to run the task exactly once, got %d", got)
	}
}

// A worker that answered (here with a 500) was reached: the task may have started, so it
// is never retried on another node.
func TestMeshRunner_DoesNotRetryAfterTheWorkerAnswered(t *testing.T) {
	var hits int32
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer refusing.Close()

	selector := &mockSelector{nodes: []*protocol.NodeInfo{
		{NodeID: "node-a", Endpoint: refusing.URL},
		{NodeID: "node-b", Endpoint: refusing.URL},
	}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector})
	sink := newMockSink(context.Background())

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, sink); err == nil {
		t.Fatal("expected the worker error to be reported")
	}
	if selector.selectCalls != 1 {
		t.Errorf("a worker that answered must not be retried: selections=%d", selector.selectCalls)
	}
	if len(selector.markedIDs) != 0 {
		t.Errorf("an answered worker must not be marked unreachable, got %v", selector.markedIDs)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected a single execution attempt, got %d", got)
	}
}

// A task the worker answered as failed is not retried either: the answer is the outcome.
func TestMeshRunner_DoesNotRetryWhenTheWorkerReportsFailure(t *testing.T) {
	var hits int32
	failure, _ := protocol.NewEvent(1, "task-1", protocol.EventError, protocol.ErrorPayload{
		Code:    "task_failed",
		Message: "the worker failed the task",
	})
	worker := streamingWorker(t, *failure, &hits)
	defer worker.Close()

	selector := &mockSelector{nodes: []*protocol.NodeInfo{
		{NodeID: "node-a", Endpoint: worker.URL},
		{NodeID: "node-b", Endpoint: worker.URL},
	}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector})
	sink := newMockSink(context.Background())

	_ = mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, sink)

	if selector.selectCalls != 1 {
		t.Errorf("a task the worker reported as failed must not be retried: selections=%d", selector.selectCalls)
	}
	if len(selector.markedIDs) != 0 {
		t.Errorf("a worker that answered must not be marked unreachable, got %v", selector.markedIDs)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected a single execution attempt, got %d", got)
	}
}

// The retry budget is capped at three attempts per task, and a transport failure never
// falls back to the local runner.
func TestMeshRunner_StopsAtThreeDispatchAttempts(t *testing.T) {
	dead := closedEndpoint(t)
	selector := &mockSelector{selectedNode: &protocol.NodeInfo{NodeID: "node-a", Endpoint: dead}}
	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fallback})

	err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background()))
	if err == nil {
		t.Fatal("expected an error when every node refuses the task")
	}
	if selector.selectCalls != 3 {
		t.Errorf("expected exactly 3 dispatch attempts, got %d", selector.selectCalls)
	}
	if len(selector.markedIDs) != 3 {
		t.Errorf("expected the node to be marked on each attempt, got %v", selector.markedIDs)
	}
	if fallback.called {
		t.Error("a dispatch failure must not fall back to the local runner")
	}
}

// A task whose dispatch failed must not end up in the local fallback, not even when there is
// a single dead node: the dispatch error is what the caller sees, and the fallback is never
// invoked (otherwise the client would read "completed" without any worker running it).
func TestMeshRunner_SingleDeadNodeDoesNotFallBackToLocalRunner(t *testing.T) {
	selector := &mockSelector{selectedNode: &protocol.NodeInfo{NodeID: "node-a", Endpoint: closedEndpoint(t)}}
	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fallback})

	err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background()))
	if err == nil {
		t.Fatal("expected the dispatch error, got nil (the task must not be reported as completed)")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected the transport error to be reported, got: %v", err)
	}
	if fallback.called {
		t.Error("a dispatch failure must never fall back to the local runner")
	}
	if selector.selectCalls != 3 {
		t.Errorf("expected the 3 dispatch attempts, got %d", selector.selectCalls)
	}
}

// Same when the retry finds no node left: the selection error must not replace the dispatch
// error, because that would let the local fallback mask a task that a dead node swallowed.
func TestMeshRunner_SelectionFailureAfterADispatchKeepsTheDispatchError(t *testing.T) {
	selector := &mockSelector{
		nodes: []*protocol.NodeInfo{{NodeID: "node-a", Endpoint: closedEndpoint(t)}},
		err:   errors.New("no healthy node available with required capacity"),
	}
	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fallback})

	err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background()))
	if err == nil {
		t.Fatal("expected the dispatch error, got nil")
	}
	if strings.Contains(err.Error(), "no healthy node available") {
		t.Errorf("the selector error must not replace a dispatch failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected the transport error, got: %v", err)
	}
	if fallback.called {
		t.Error("the fallback must not run after a dispatch failure")
	}
	if selector.selectCalls != 2 {
		t.Errorf("expected 2 selections (the dead node, then none), got %d", selector.selectCalls)
	}
}

// The other side of the distinction: when the very first selection finds nothing, nothing was
// dispatched and the fallback works exactly as before.
func TestMeshRunner_FirstSelectionFailureStillFallsBack(t *testing.T) {
	selector := &mockSelector{err: errors.New("no healthy node available with required capacity")}
	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fallback})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("the fallback should have run the task, got: %v", err)
	}
	if !fallback.called {
		t.Error("the fallback must be used when no node could ever be selected")
	}
	if selector.selectCalls != 1 {
		t.Errorf("expected a single selection, got %d", selector.selectCalls)
	}
}

// End to end with the REAL registry and the REAL RemoteRunner (no selector mock): the node
// with the closed port is picked first, its transport error is recognized, and the task ends
// on the registered live node.
func TestMeshRunner_RealRemoteRunnerRetriesOnAnotherRegisteredNode(t *testing.T) {
	var hits int32
	completion, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{Result: "ran alive"})
	alive := streamingWorker(t, *completion, &hits)
	defer alive.Close()

	reg := registry.NewRegistry(30 * time.Second)
	if _, err := reg.RegisterNode(nodeJoin("node-a-dead", closedEndpoint(t))); err != nil {
		t.Fatalf("failed to register the dead node: %v", err)
	}
	if _, err := reg.RegisterNode(nodeJoin("node-b-alive", alive.URL)); err != nil {
		t.Fatalf("failed to register the live node: %v", err)
	}

	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: reg, FallbackRunner: &mockRunner{}})
	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("the task should have ended on the live node, got: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected the live node to run the task exactly once, got %d", got)
	}

	dead, ok := reg.GetNode("node-a-dead")
	if !ok {
		t.Fatal("the dead node should still be registered")
	}
	if dead.Status != protocol.NodeStatusDegraded {
		t.Errorf("the node that refused the task should be degraded, got %s", dead.Status)
	}
	live, _ := reg.GetNode("node-b-alive")
	if live.Status != protocol.NodeStatusOnline {
		t.Errorf("the live node should stay online, got %s", live.Status)
	}
}

// The real dial error must stay unwrapped enough for the classifier: this prints its type and
// message and checks the exact shape isPreAcceptanceTransportError looks for.
func TestMeshRunner_RealDialErrorKeepsItsGoType(t *testing.T) {
	raw := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
		Endpoint: closedEndpoint(t),
		Client:   &http.Client{Timeout: 5 * time.Second},
	})
	runErr := raw.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, newMockSink(context.Background()))
	if runErr == nil {
		t.Fatal("expected a transport error from a closed endpoint")
	}
	t.Logf("error real de RemoteRunner.Run: tipo=%T mensaje=%q", runErr, runErr.Error())

	var urlErr *url.Error
	if !errors.As(runErr, &urlErr) {
		t.Fatalf("the error must stay a *url.Error for the classifier to inspect it, got %T", runErr)
	}
	var netErr *net.OpError
	if !errors.As(runErr, &netErr) || netErr.Op != "dial" {
		t.Fatalf("expected a *net.OpError with Op == dial inside the chain, got %T", runErr)
	}
	t.Logf("cadena: *url.Error(%s) -> *net.OpError(Op=%q) -> %v", urlErr.Op, netErr.Op, netErr.Err)
}

func nodeJoin(id, endpoint string) protocol.NodeJoinRequest {
	return protocol.NodeJoinRequest{
		NodeID:   id,
		Endpoint: endpoint,
		Agents:   []protocol.AgentProfile{{Name: "worker", Tags: []string{"git"}}},
		Hardware: protocol.NodeHardware{CPUs: 8, RAMGB: 16, OS: "linux/amd64"},
	}
}
