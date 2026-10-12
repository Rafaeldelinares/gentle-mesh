package runner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// fallbackSelector implements runner.NodeSelector and returns either a configured error or
// the next node in a queue. It is deterministic: no clock, no sleeps.
type fallbackSelector struct {
	err   error
	nodes []*protocol.NodeInfo
}

func (s *fallbackSelector) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.nodes) == 0 {
		return nil, errors.New("no nodes")
	}
	return s.nodes[0], nil
}
func (s *fallbackSelector) MarkNodeUnreachable(nodeID string) error { return nil }

// callRecorder counts Run invocations on a runner.
type callRecorder struct{ calls atomic.Int32 }

func (r *callRecorder) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.calls.Add(1)
	return nil
}

func lfbCompletionNode(t *testing.T) *httptest.Server {
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

// TestMeshRunnerNoFallbackAndNoEligibleNodeReturnsError covers the flag-off case: no eligible
// node and no fallback wired -> MeshRunner returns the existing error without calling any
// runner, so the error propagates to the scheduler (which classifies it as DISPATCH_FAILED).
func TestMeshRunnerNoFallbackAndNoEligibleNodeReturnsError(t *testing.T) {
	fb := &callRecorder{}
	mr := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       &fallbackSelector{err: registry.ErrAgentNotSupported},
		FallbackRunner: nil, // flag off -> no fallback
		Token:          "x",
	})
	err := mr.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background()))
	if err == nil {
		t.Fatal("expected an error when no node is eligible and no fallback is wired")
	}
	if !strings.Contains(err.Error(), "no mesh worker available") {
		t.Errorf("error %q does not contain 'no mesh worker available'", err)
	}
	if got := fb.calls.Load(); got != 0 {
		t.Errorf("fallback runner must NOT be called when no node is eligible, got %d", got)
	}
}

// TestMeshRunnerWithFallbackCompletesLocally covers the default (flag on) case: no eligible
// node, fallback present -> the fallback runs once and Run returns nil.
func TestMeshRunnerWithFallbackCompletesLocally(t *testing.T) {
	fb := &callRecorder{}
	mr := runner.NewMeshRunner(runner.MeshRunnerOptions{
		Selector:       &fallbackSelector{err: registry.ErrAgentNotSupported},
		FallbackRunner: fb,
		Token:          "x",
	})
	if err := mr.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fb.calls.Load(); got != 1 {
		t.Errorf("fallback runner must be called once, got %d", got)
	}
}

// TestMeshRunnerEligibleNodeDispatchesRegardlessOfFallback: with a node eligible, the task goes
// to the mesh (not the fallback), regardless of whether a fallback is wired. This is the
// "no diferencia" case.
func TestMeshRunnerEligibleNodeDispatchesRegardlessOfFallback(t *testing.T) {
	ts := lfbCompletionNode(t)
	fb := &callRecorder{}
	selector := &fallbackSelector{nodes: []*protocol.NodeInfo{
		{NodeID: "ok", Endpoint: ts.URL, MaxConcurrency: 2, ActiveTasks: 0, Agents: []protocol.AgentProfile{{Name: "worker"}}},
	}}
	mr := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fb, Client: ts.Client(), Token: "x"})
	if err := mr.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fb.calls.Load(); got != 0 {
		t.Errorf("fallback must NOT be called when a node is eligible, got %d", got)
	}
}

// TestMeshRunnerAllFullSoftCapDispatchesWithoutFallback: with #94, a full node is still
// dispatched to (soft cap), and the fallback is NOT used regardless of the flag.
func TestMeshRunnerAllFullSoftCapDispatchesWithoutFallback(t *testing.T) {
	ts := lfbCompletionNode(t)
	fb := &callRecorder{}
	selector := &fallbackSelector{nodes: []*protocol.NodeInfo{
		{NodeID: "full", Endpoint: ts.URL, MaxConcurrency: 1, ActiveTasks: 1, Agents: []protocol.AgentProfile{{Name: "worker"}}},
	}}
	mr := runner.NewMeshRunner(runner.MeshRunnerOptions{Selector: selector, FallbackRunner: fb, Client: ts.Client(), Token: "x"})
	if err := mr.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "t"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fb.calls.Load(); got != 0 {
		t.Errorf("fallback must NOT be called under the soft cap, got %d", got)
	}
}
