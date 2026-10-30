package runner

import (
	"context"
	"testing"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
)

// attributingSink records attribution calls and forwarded events.
type attributingSink struct {
	attributed []string
	events     []protocol.EventType
}

func (s *attributingSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	s.events = append(s.events, eventType)
	return protocol.Event{Type: eventType}, nil
}
func (s *attributingSink) Context() context.Context           { return context.Background() }
func (s *attributingSink) RegisterQuery(string) <-chan string { return make(chan string) }
func (s *attributingSink) AttributeNode(nodeID string)        { s.attributed = append(s.attributed, nodeID) }

// plainForwardingSink implements only EventSink: no attribution support.
type plainForwardingSink struct{ events []protocol.EventType }

func (s *plainForwardingSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	s.events = append(s.events, eventType)
	return protocol.Event{Type: eventType}, nil
}
func (s *plainForwardingSink) Context() context.Context           { return context.Background() }
func (s *plainForwardingSink) RegisterQuery(string) <-chan string { return make(chan string) }

func TestNodeAttributionSinkRecordsTheNodeOnTheFirstEvent(t *testing.T) {
	inner := &attributingSink{}
	sink := &nodeAttributionSink{EventSink: inner, nodeID: "node-alpha"}

	// A dispatch attempt that never produced an event must not claim the task.
	if len(inner.attributed) != 0 {
		t.Fatalf("no attribution expected before any event, got %v", inner.attributed)
	}

	if _, err := sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning}); err != nil {
		t.Fatalf("first event failed: %v", err)
	}
	if len(inner.attributed) != 1 || inner.attributed[0] != "node-alpha" {
		t.Fatalf("expected the node to be attributed on the first event, got %v", inner.attributed)
	}

	// Further events keep forwarding but do not attribute again.
	if _, err := sink.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "hi"}); err != nil {
		t.Fatalf("second event failed: %v", err)
	}
	if len(inner.attributed) != 1 {
		t.Fatalf("the node must be attributed exactly once, got %v", inner.attributed)
	}
}

func TestNodeAttributionSinkWithoutAttributorIsUntouched(t *testing.T) {
	inner := &plainForwardingSink{}
	sink := &nodeAttributionSink{EventSink: inner, nodeID: "node-alpha"}

	// The wrapped sink does not support attribution: this must be a no-op, never a failure.
	if _, err := sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning}); err != nil {
		t.Fatalf("forwarding failed: %v", err)
	}
	if len(inner.events) != 1 || inner.events[0] != protocol.EventStatus {
		t.Fatalf("event was not forwarded untouched: %v", inner.events)
	}
}

func TestNodeAttributionSinkIgnoresEmptyNode(t *testing.T) {
	inner := &attributingSink{}
	sink := &nodeAttributionSink{EventSink: inner}

	if _, err := sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning}); err != nil {
		t.Fatalf("forwarding failed: %v", err)
	}
	if len(inner.attributed) != 0 {
		t.Fatalf("an empty node must not be attributed, got %v", inner.attributed)
	}
}

func TestNodeAttributionSinkForwardsEventsUntouched(t *testing.T) {
	inner := &attributingSink{}
	sink := &nodeAttributionSink{EventSink: inner, nodeID: "node-alpha"}

	if _, err := sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning}); err != nil {
		t.Fatalf("forwarding failed: %v", err)
	}
	if len(inner.events) != 1 || inner.events[0] != protocol.EventStatus {
		t.Fatalf("event was not forwarded untouched: %v", inner.events)
	}
}
