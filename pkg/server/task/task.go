package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var (
	// ErrInvalidStateTransition is returned when an illegal lifecycle transition is attempted.
	ErrInvalidStateTransition = errors.New("invalid task state transition")
	// ErrTaskAlreadyFinished is returned when attempting an operation on a terminal task.
	ErrTaskAlreadyFinished = errors.New("task has already finished")
	// ErrQueryNotFound is returned when an interactive query cannot be found or was already resolved.
	ErrQueryNotFound = errors.New("query not found or already answered")
	// ErrQueryTimedOut is returned when waiting for an interactive query answer times out.
	ErrQueryTimedOut = errors.New("query wait timed out")
	// ErrSubscriberClosed is returned when an operation is performed on a closed subscriber.
	ErrSubscriberClosed = errors.New("subscriber channel closed")
)

// ManagedTask coordinates the in-memory execution state, event emission, event logging,
// interactive queries, and live event subscriber fan-out for a single remote subagent task.
type ManagedTask struct {
	TaskID         string
	Request        protocol.TaskRequest
	mu             sync.RWMutex
	Status         protocol.TaskStatus
	CreatedAt      int64
	StartedAt      int64
	FinishedAt     int64
	Completion     *protocol.CompletionPayload
	Error          *protocol.ErrorPayload
	ctx            context.Context
	cancel         context.CancelFunc
	eventSeq       atomic.Int64
	logger         *JSONLLogger
	subscribers    map[chan protocol.Event]struct{}
	pendingQueries map[string]chan string
	finishedTime   time.Time
}

// NewManagedTask constructs a new ManagedTask instance.
func NewManagedTask(
	taskID string,
	req protocol.TaskRequest,
	ctx context.Context,
	cancel context.CancelFunc,
	logger *JSONLLogger,
) *ManagedTask {
	return &ManagedTask{
		TaskID:         taskID,
		Request:        req,
		Status:         protocol.TaskStatusQueued,
		CreatedAt:      time.Now().Unix(),
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		subscribers:    make(map[chan protocol.Event]struct{}),
		pendingQueries: make(map[string]chan string),
	}
}

// Context returns the task execution and cancellation context.
func (t *ManagedTask) Context() context.Context {
	return t.ctx
}

// isTerminalLocked returns true if the task has reached a final immutable state.
func (t *ManagedTask) isTerminalLocked() bool {
	return t.Status == protocol.TaskStatusCompleted ||
		t.Status == protocol.TaskStatusFailed ||
		t.Status == protocol.TaskStatusCanceled
}

func isValidTransition(from, to protocol.TaskStatus) bool {
	if from == to {
		return false
	}
	switch from {
	case protocol.TaskStatusQueued:
		return to == protocol.TaskStatusPreparing ||
			to == protocol.TaskStatusRunning ||
			to == protocol.TaskStatusCanceled ||
			to == protocol.TaskStatusFailed
	case protocol.TaskStatusPreparing:
		return to == protocol.TaskStatusRunning ||
			to == protocol.TaskStatusCanceled ||
			to == protocol.TaskStatusFailed
	case protocol.TaskStatusRunning:
		return to == protocol.TaskStatusCompleted ||
			to == protocol.TaskStatusCanceled ||
			to == protocol.TaskStatusFailed
	default:
		// Terminal states cannot transition further
		return false
	}
}

// TransitionTo validates and performs a lifecycle state transition.
// It emits protocol.EventTypeStatus on success and closes subscribers if the state is terminal.
func (t *ManagedTask) TransitionTo(status protocol.TaskStatus) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isTerminalLocked() {
		return fmt.Errorf("%w: %w", ErrInvalidStateTransition, ErrTaskAlreadyFinished)
	}

	if !isValidTransition(t.Status, status) {
		return ErrInvalidStateTransition
	}

	t.Status = status
	now := time.Now()
	if t.Status == protocol.TaskStatusRunning && t.StartedAt == 0 {
		t.StartedAt = now.Unix()
	}
	if (t.Status == protocol.TaskStatusCompleted || t.Status == protocol.TaskStatusFailed || t.Status == protocol.TaskStatusCanceled) && t.FinishedAt == 0 {
		t.FinishedAt = now.Unix()
		t.finishedTime = now
		t.cancel()
	}

	id := t.eventSeq.Add(1)
	evt, err := protocol.NewEvent(id, t.TaskID, protocol.EventStatus, protocol.StatusPayload{Status: status})
	if err != nil {
		return err
	}

	if t.logger != nil {
		if err := t.logger.WriteEvent(*evt); err != nil {
			return fmt.Errorf("failed to write status event: %w", err)
		}
	}

	for sub := range t.subscribers {
		select {
		case sub <- *evt:
		default:
		}
	}

	if t.isTerminalLocked() {
		for sub := range t.subscribers {
			close(sub)
		}
		t.subscribers = make(map[chan protocol.Event]struct{})
	}

	return nil
}

// EmitEvent creates a new sequential event, logs it to the append-only JSONL log,
// broadcasts it non-blockingly to all subscribers, and updates internal status if applicable.
func (t *ManagedTask) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isTerminalLocked() {
		return protocol.Event{}, fmt.Errorf("%w: %w", ErrInvalidStateTransition, ErrTaskAlreadyFinished)
	}

	id := t.eventSeq.Add(1)
	evt, err := protocol.NewEvent(id, t.TaskID, eventType, payload)
	if err != nil {
		return protocol.Event{}, err
	}

	now := time.Now()
	switch eventType {
	case protocol.EventStatus:
		var sp protocol.StatusPayload
		if err := evt.UnmarshalPayload(&sp); err == nil && sp.Status != "" {
			if sp.Status != t.Status {
				if !isValidTransition(t.Status, sp.Status) {
					return protocol.Event{}, ErrInvalidStateTransition
				}
				t.Status = sp.Status
				if t.Status == protocol.TaskStatusRunning && t.StartedAt == 0 {
					t.StartedAt = now.Unix()
				}
				if (t.Status == protocol.TaskStatusCompleted || t.Status == protocol.TaskStatusFailed || t.Status == protocol.TaskStatusCanceled) && t.FinishedAt == 0 {
					t.FinishedAt = now.Unix()
					t.finishedTime = now
					t.cancel()
				}
			}
		}
	case protocol.EventCompletion:
		var cp protocol.CompletionPayload
		if err := evt.UnmarshalPayload(&cp); err == nil {
			t.Completion = &cp
		}
		t.Status = protocol.TaskStatusCompleted
		if t.FinishedAt == 0 {
			t.FinishedAt = now.Unix()
			t.finishedTime = now
		}
		t.cancel()
	case protocol.EventError:
		var ep protocol.ErrorPayload
		if err := evt.UnmarshalPayload(&ep); err == nil {
			t.Error = &ep
		}
		t.Status = protocol.TaskStatusFailed
		if t.FinishedAt == 0 {
			t.FinishedAt = now.Unix()
			t.finishedTime = now
		}
		t.cancel()
	}

	if t.logger != nil {
		if err := t.logger.WriteEvent(*evt); err != nil {
			return protocol.Event{}, fmt.Errorf("failed to write event to logger: %w", err)
		}
	}

	for sub := range t.subscribers {
		select {
		case sub <- *evt:
		default:
		}
	}

	if t.isTerminalLocked() {
		for sub := range t.subscribers {
			close(sub)
		}
		t.subscribers = make(map[chan protocol.Event]struct{})
	}

	return *evt, nil
}

// RegisterQuery registers a channel to receive interactive query replies for queryID.
// If queryID is already registered, the existing channel is returned.
func (t *ManagedTask) RegisterQuery(queryID string) <-chan string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pendingQueries == nil {
		t.pendingQueries = make(map[string]chan string)
	}

	if ch, exists := t.pendingQueries[queryID]; exists {
		return ch
	}

	ch := make(chan string, 1)
	t.pendingQueries[queryID] = ch
	return ch
}

// ResolveQuery delivers answer to the waiting query channel and cleans up registration.
// Returns ErrQueryNotFound if the query is not registered or was already answered.
func (t *ManagedTask) ResolveQuery(queryID string, answer string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pendingQueries == nil {
		return ErrQueryNotFound
	}

	ch, exists := t.pendingQueries[queryID]
	if !exists {
		return ErrQueryNotFound
	}

	delete(t.pendingQueries, queryID)
	ch <- answer
	close(ch)
	return nil
}

// WaitForQuery registers queryID and blocks until an answer is received, ctx is done, or timeout occurs.
func (t *ManagedTask) WaitForQuery(ctx context.Context, queryID string) (string, error) {
	ch := t.RegisterQuery(queryID)
	select {
	case ans, ok := <-ch:
		if !ok {
			return "", ErrQueryNotFound
		}
		return ans, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pendingQueries, queryID)
		t.mu.Unlock()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ErrQueryTimedOut
		}
		return "", ctx.Err()
	}
}

// Snapshot returns a point-in-time snapshot of the task state.
func (t *ManagedTask) Snapshot() protocol.TaskState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var comp *protocol.CompletionPayload
	if t.Completion != nil {
		c := *t.Completion
		comp = &c
	}

	var errPayload *protocol.ErrorPayload
	if t.Error != nil {
		e := *t.Error
		errPayload = &e
	}

	return protocol.TaskState{
		TaskID:     t.TaskID,
		Request:    t.Request,
		Status:     t.Status,
		CreatedAt:  t.CreatedAt,
		StartedAt:  t.StartedAt,
		FinishedAt: t.FinishedAt,
		Completion: comp,
		Error:      errPayload,
	}
}

// CurrentStatus returns the current task status safely under a read lock.
func (t *ManagedTask) CurrentStatus() protocol.TaskStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// Subscribe returns an event channel delivering historical events with ID > sinceID
// followed by live broadcast events. If the task is already terminal, the channel is closed
// immediately after historical playback. The returned unsubscribe function removes the subscriber
// and safely drains any buffered events.
func (t *ManagedTask) Subscribe(sinceID int64) (<-chan protocol.Event, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var history []protocol.Event
	if t.logger != nil {
		var err error
		history, err = t.logger.ReadEvents(sinceID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read task history: %w", err)
		}
	}

	if t.isTerminalLocked() {
		ch := make(chan protocol.Event, len(history))
		for _, evt := range history {
			ch <- evt
		}
		close(ch)
		return ch, func() {}, nil
	}

	bufSize := 128
	if len(history)+64 > bufSize {
		bufSize = len(history) + 64
	}
	ch := make(chan protocol.Event, bufSize)
	for _, evt := range history {
		ch <- evt
	}

	if t.subscribers == nil {
		t.subscribers = make(map[chan protocol.Event]struct{})
	}
	t.subscribers[ch] = struct{}{}

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			t.mu.Lock()
			_, wasActive := t.subscribers[ch]
			delete(t.subscribers, ch)
			t.mu.Unlock()

			if wasActive {
				for {
					select {
					case _, ok := <-ch:
						if !ok {
							return
						}
					default:
						goto drained
					}
				}
			drained:
				close(ch)
			}
		})
	}

	return ch, unsub, nil
}
