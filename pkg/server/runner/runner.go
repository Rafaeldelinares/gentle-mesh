package runner

import (
	"context"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// EventToken represents the event type for streaming subagent thought/tokens.
// Maps to the canonical protocol.EventThought ("thought").
const EventToken = protocol.EventThought

// EventSink decouples the runner from concrete task storage and lifecycle coordination.
type EventSink interface {
	EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error)
	Context() context.Context
	RegisterQuery(queryID string) <-chan string
}

// Runner defines the execution interface for remote subagent task runners.
type Runner interface {
	Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error
}
