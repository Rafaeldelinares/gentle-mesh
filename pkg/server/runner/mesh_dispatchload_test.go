package runner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/registry"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/runner"
)

// reservingSelector implements runner.NodeReserver. It hands out the queued nodes in order and
// records every reservation, release and unreachable mark as an ordered event list, so the
// reservation lifecycle can be asserted without timing anything.
type reservingSelector struct {
	mu     sync.Mutex
	nodes  []*protocol.NodeInfo
	err    error
	events []string
}

func (s *reservingSelector) SelectAndReserve(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	if len(s.nodes) == 0 {
		return nil, errors.New("no nodes queued")
	}
	node := s.nodes[0]
	s.nodes = s.nodes[1:]
	s.events = append(s.events, "reserve:"+node.NodeID)
	return node, nil
}

// SelectNodeForTask satisfies runner.NodeSelector. The MeshRunner prefers SelectAndReserve on
// this selector, so a recorded "select:" entry means reservation was skipped.
func (s *reservingSelector) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	if len(s.nodes) == 0 {
		return nil, errors.New("no nodes queued")
	}
	node := s.nodes[0]
	s.nodes = s.nodes[1:]
	s.events = append(s.events, "select:"+node.NodeID)
	return node, nil
}

func (s *reservingSelector) Release(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, "release:"+nodeID)
}

func (s *reservingSelector) MarkNodeUnreachable(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, "mark-unreachable:"+nodeID)
	return nil
}

func (s *reservingSelector) record() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.events))
	copy(out, s.events)
	return out
}

// completionNode serves one completion event over SSE, the minimum a dispatch needs to succeed.
func completionNode(t *testing.T) *httptest.Server {
	t.Helper()

	completion, _ := protocol.NewEvent(1, "task-1", protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(completion.FormatSSE())
	}))
	t.Cleanup(ts.Close)
	return ts
}

func loadNode(id, endpoint string) *protocol.NodeInfo {
	return &protocol.NodeInfo{
		NodeID:         id,
		Endpoint:       endpoint,
		MaxConcurrency: 1,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
	}
}

// panickingSink fails the way a sink bug would: in the middle of a dispatch. It embeds a
// working sink so it inherits the rest of the EventSink contract and only overrides emission.
type panickingSink struct {
	runner.EventSink
}

func (panickingSink) EmitEvent(protocol.EventType, any) (protocol.Event, error) {
	panic("sink boom")
}

func TestMeshRunnerReleasesReservationOnSuccess(t *testing.T) {
	ts := completionNode(t)
	selector := &reservingSelector{nodes: []*protocol.NodeInfo{loadNode("node-a", ts.URL)}}
	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fallback, Client: ts.Client()})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"reserve:node-a", "release:node-a"}
	if got := selector.record(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("expected %v, got %v", want, got)
	}
	if fallback.called {
		t.Error("fallback must not run when a node accepted the task")
	}
}

func TestMeshRunnerReleasesReservationOnDispatchError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	selector := &reservingSelector{nodes: []*protocol.NodeInfo{loadNode("node-a", ts.URL)}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: &mockRunner{}, Client: ts.Client()})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err == nil {
		t.Fatal("expected the dispatch error to be reported")
	}

	want := []string{"reserve:node-a", "release:node-a"}
	if got := selector.record(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestMeshRunnerReleasesReservationOnCancellation(t *testing.T) {
	started := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	selector := &reservingSelector{nodes: []*protocol.NodeInfo{loadNode("node-a", ts.URL)}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: &mockRunner{}, Client: ts.Client()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- mesh.Run(ctx, protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background()))
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected a cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	want := []string{"reserve:node-a", "release:node-a"}
	if got := selector.record(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("expected %v after cancellation, got %v", want, got)
	}
}

func TestMeshRunnerReleasesReservationOnSinkPanic(t *testing.T) {
	ts := completionNode(t)
	selector := &reservingSelector{nodes: []*protocol.NodeInfo{loadNode("node-a", ts.URL)}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: &mockRunner{}, Client: ts.Client()})

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Error("expected the sink panic to propagate")
		}
		want := []string{"reserve:node-a", "release:node-a"}
		if got := selector.record(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("expected %v after a panic, got %v", want, got)
		}
	}()

	_ = mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, panickingSink{EventSink: newMockSink(context.Background())})
}

// TestMeshRunnerReleasesFailedNodeBeforeRetrying covers the transport retry: the node that did
// not accept the task gives its slot back before the next node is reserved.
func TestMeshRunnerReleasesFailedNodeBeforeRetrying(t *testing.T) {
	healthy := completionNode(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	selector := &reservingSelector{nodes: []*protocol.NodeInfo{
		loadNode("node-dead", deadURL),
		loadNode("node-alive", healthy.URL),
	}}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: &mockRunner{}, Client: healthy.Client()})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"reserve:node-dead",
		"release:node-dead",
		"mark-unreachable:node-dead",
		"reserve:node-alive",
		"release:node-alive",
	}
	got := selector.record()
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// TestMeshRunnerWithoutReservationSupportStillWorks keeps selectors that only want to select
// working exactly as before: no reservation, no release, one dispatch.
func TestMeshRunnerWithoutReservationSupportStillWorks(t *testing.T) {
	ts := completionNode(t)
	selector := &mockSelector{selectedNode: loadNode("node-plain", ts.URL)}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: &mockRunner{}, Client: ts.Client()})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selector.selectCalls != 1 {
		t.Errorf("expected exactly one selection, got %d", selector.selectCalls)
	}
}

// TestMeshRunnerKeepsWorkingWhenEveryNodeIsAtCapacity is the behaviour change: a mesh whose nodes
// are all at capacity is not "nothing available", so the task still runs on a node instead of
// quietly succeeding in the coordinator's local simulated runner.
func TestMeshRunnerKeepsWorkingWhenEveryNodeIsAtCapacity(t *testing.T) {
	ts := completionNode(t)
	reg := registry.NewRegistry(30 * time.Second)
	if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "node-1",
		Endpoint:       ts.URL,
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 1,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("occupy the only slot: %v", err)
	}

	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: reg, FallbackRunner: fallback, Client: ts.Client()})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.called {
		t.Error("a full mesh must not fall back to the local simulated runner")
	}
}

// TestMeshRunnerFallsBackWhenNoNodeSupportsTheAgent keeps finding 9b intact: the local fallback
// still answers when there is no eligible node at all.
func TestMeshRunnerFallsBackWhenNoNodeSupportsTheAgent(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)
	if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
		NodeID:         "node-1",
		Endpoint:       "http://10.0.0.1:8080",
		Agents:         []protocol.AgentProfile{{Name: "worker"}},
		MaxConcurrency: 2,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	fallback := &mockRunner{}
	mesh := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: reg, FallbackRunner: fallback})

	if err := mesh.Run(context.Background(), protocol.TaskRequest{Agent: "designer", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fallback.called {
		t.Error("expected the local fallback when no node supports the agent")
	}
}
