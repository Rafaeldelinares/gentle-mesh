package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ErrNodeLost means the node accepted the task and then vanished. It must never be treated as a
// pre-acceptance transport error: the task was already accepted, so retrying it on another node
// could run it twice, and the local fallback must not answer it either.
func TestNodeLostIsNotAPreAcceptanceTransportError(t *testing.T) {
	if isPreAcceptanceTransportError(ErrNodeLost) {
		t.Fatal("ErrNodeLost must not be retried: the node already accepted the task")
	}
	if isPreAcceptanceTransportError(fmt.Errorf("remote node failed: %w", ErrNodeLost)) {
		t.Fatal("a wrapped ErrNodeLost must not be retried either")
	}
	if !errors.Is(fmt.Errorf("remote node failed: %w", ErrNodeLost), ErrNodeLost) {
		t.Fatal("ErrNodeLost must survive wrapping, so the scheduler maps it to NODE_LOST")
	}
}

// TestRunReturnsNodeLostWhenTheStreamEndsWithoutTerminalEvent pins the measured bug: a node that
// accepts the task and then closes the stream without a completion or an error is a lost node, not
// a successful run.
func TestRunReturnsNodeLostWhenTheStreamEndsWithoutTerminalEvent(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: status\ndata: {\"id\":1,\"task_id\":\"t\",\"timestamp\":0,\"type\":\"status\",\"payload\":{\"status\":\"running\"}}\n\n")
	}))
	defer node.Close()

	sink := &capturingSink{}
	err := NewRemoteRunner(RemoteRunnerConfig{Endpoint: node.URL, Client: node.Client()}).Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "x"}, sink)

	if !errors.Is(err, ErrNodeLost) {
		t.Fatalf("Run must return ErrNodeLost when the stream ends without a terminal event, got %v", err)
	}
	if len(sink.events) != 1 || sink.events[0] != protocol.EventStatus {
		t.Fatalf("the non-terminal event must still be forwarded, got %v", sink.events)
	}
}

// capturingSink records the forwarded events.
type capturingSink struct{ events []protocol.EventType }

func (s *capturingSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	s.events = append(s.events, eventType)
	return protocol.Event{Type: eventType}, nil
}
func (s *capturingSink) Context() context.Context           { return context.Background() }
func (s *capturingSink) RegisterQuery(string) <-chan string { return make(chan string) }
