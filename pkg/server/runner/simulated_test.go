package runner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// mockSink implements runner.EventSink for focused in-memory unit tests.
type mockSink struct {
	ctx        context.Context
	mu         sync.Mutex
	events     []protocol.Event
	queries    map[string]chan string
	eventIDSeq int64
	onEvent    func(evt protocol.Event)
}

func newMockSink(ctx context.Context) *mockSink {
	if ctx == nil {
		ctx = context.Background()
	}
	return &mockSink{
		ctx:     ctx,
		queries: make(map[string]chan string),
	}
}

func (s *mockSink) Context() context.Context {
	return s.ctx
}

func (s *mockSink) RegisterQuery(queryID string) <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ch, exists := s.queries[queryID]; exists {
		return ch
	}
	ch := make(chan string, 1)
	s.queries[queryID] = ch
	return ch
}

func (s *mockSink) ResolveQuery(queryID string, answer string) {
	s.mu.Lock()
	ch, exists := s.queries[queryID]
	if exists {
		delete(s.queries, queryID)
	}
	s.mu.Unlock()

	if exists {
		ch <- answer
		close(ch)
	}
}

func (s *mockSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.eventIDSeq++
	evt, err := protocol.NewEvent(s.eventIDSeq, "mock-task-1", eventType, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	s.events = append(s.events, *evt)
	if s.onEvent != nil {
		s.onEvent(*evt)
	}
	return *evt, nil
}

func (s *mockSink) Events() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]protocol.Event, len(s.events))
	copy(res, s.events)
	return res
}

func TestSimulatedRunner_HappyPath(t *testing.T) {
	t.Run("default tokens and completion", func(t *testing.T) {
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{})
		sink := newMockSink(context.Background())

		req := protocol.TaskRequest{
			Agent: "worker",
			Task:  "Analyze codebase",
		}

		err := r.Run(context.Background(), req, sink)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}

		events := sink.Events()
		// 1 status (running) + 7 default tokens + 1 completion = 9 events
		expectedCount := 1 + len(runner.DefaultTokens) + 1
		if len(events) != expectedCount {
			t.Fatalf("expected %d events, got %d", expectedCount, len(events))
		}

		// 1. First event must be StatusRunning
		if events[0].Type != protocol.EventStatus {
			t.Errorf("expected first event type %q, got %q", protocol.EventStatus, events[0].Type)
		}
		var statusPayload protocol.StatusPayload
		if err := events[0].UnmarshalPayload(&statusPayload); err != nil {
			t.Fatalf("failed to unmarshal status payload: %v", err)
		}
		if statusPayload.Status != protocol.TaskStatusRunning {
			t.Errorf("expected status %q, got %q", protocol.TaskStatusRunning, statusPayload.Status)
		}

		// 2. Middle events must be DefaultTokens
		for i, expectedToken := range runner.DefaultTokens {
			evt := events[i+1]
			if evt.Type != runner.EventToken {
				t.Errorf("event %d: expected type %q, got %q", i+1, runner.EventToken, evt.Type)
			}
			var thought protocol.ThoughtPayload
			if err := evt.UnmarshalPayload(&thought); err != nil {
				t.Fatalf("event %d: failed to unmarshal thought payload: %v", i+1, err)
			}
			if thought.Text != expectedToken {
				t.Errorf("event %d: expected token text %q, got %q", i+1, expectedToken, thought.Text)
			}
		}

		// 3. Last event must be Completion
		lastEvt := events[len(events)-1]
		if lastEvt.Type != protocol.EventCompletion {
			t.Errorf("expected last event type %q, got %q", protocol.EventCompletion, lastEvt.Type)
		}
		var completionPayload protocol.CompletionPayload
		if err := lastEvt.UnmarshalPayload(&completionPayload); err != nil {
			t.Fatalf("failed to unmarshal completion payload: %v", err)
		}
		if completionPayload.Result != "Task completed successfully" {
			t.Errorf("expected completion result %q, got %q", "Task completed successfully", completionPayload.Result)
		}
		if len(completionPayload.FilesChanged) != 0 {
			t.Errorf("expected 0 files changed, got %d", len(completionPayload.FilesChanged))
		}
	})

	t.Run("custom tokens, files changed, and completion result", func(t *testing.T) {
		customTokens := []string{"Planning", " -> ", "Executing", " -> ", "All green"}
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens:           customTokens,
			CompletionResult: "Refactoring completed with 100% tests passing",
			FilesChanged:     []string{"pkg/server/runner/runner.go", "pkg/server/runner/simulated.go"},
		})
		sink := newMockSink(context.Background())

		err := r.Run(context.Background(), protocol.TaskRequest{}, sink)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}

		events := sink.Events()
		expectedCount := 1 + len(customTokens) + 1
		if len(events) != expectedCount {
			t.Fatalf("expected %d events, got %d", expectedCount, len(events))
		}

		for i, tok := range customTokens {
			var tp protocol.ThoughtPayload
			if err := events[i+1].UnmarshalPayload(&tp); err != nil {
				t.Fatalf("failed to unmarshal thought payload: %v", err)
			}
			if tp.Text != tok {
				t.Errorf("expected token %q, got %q", tok, tp.Text)
			}
		}

		var comp protocol.CompletionPayload
		if err := events[len(events)-1].UnmarshalPayload(&comp); err != nil {
			t.Fatalf("failed to unmarshal completion payload: %v", err)
		}
		if comp.Result != "Refactoring completed with 100% tests passing" {
			t.Errorf("unexpected completion result: %s", comp.Result)
		}
		if len(comp.FilesChanged) != 2 || comp.FilesChanged[0] != "pkg/server/runner/runner.go" {
			t.Errorf("unexpected files changed: %+v", comp.FilesChanged)
		}
	})

	t.Run("nil sink returns error", func(t *testing.T) {
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{})
		err := r.Run(context.Background(), protocol.TaskRequest{}, nil)
		if err == nil || !strings.Contains(err.Error(), "sink is nil") {
			t.Fatalf("expected nil sink error, got: %v", err)
		}
	})

	t.Run("options getter returns configured options", func(t *testing.T) {
		opts := runner.SimulatedOptions{
			CompletionResult: "Custom",
			TokenDelay:       10 * time.Millisecond,
		}
		r := runner.NewSimulatedRunner(opts)
		if r.Options().CompletionResult != "Custom" {
			t.Errorf("expected Custom, got %s", r.Options().CompletionResult)
		}
		if r.Options().TokenDelay != 10*time.Millisecond {
			t.Errorf("expected 10ms delay, got %v", r.Options().TokenDelay)
		}
	})
}

func TestSimulatedRunner_ToolCalls(t *testing.T) {
	toolCalls := []runner.SimulatedToolCall{
		{
			ToolName: "read_file",
			Input:    map[string]any{"path": "pkg/server/main.go"},
			Result:   "package main\n\nfunc main() {}",
			Delay:    0,
		},
		{
			ToolName: "bash",
			Input:    map[string]any{"command": "go test ./..."},
			Result:   map[string]any{"exit_code": float64(0), "stdout": "PASS"},
			Delay:    5 * time.Millisecond,
		},
		{
			ToolName: "git_diff",
			Input:    nil, // tests nil input handling
			Result:   nil, // tests nil result handling
			Delay:    0,
		},
	}

	r := runner.NewSimulatedRunner(runner.SimulatedOptions{
		ToolCalls:        toolCalls,
		Tokens:           []string{"Done testing"},
		CompletionResult: "All tools executed",
	})
	sink := newMockSink(context.Background())

	err := r.Run(context.Background(), protocol.TaskRequest{}, sink)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	events := sink.Events()

	// Sequence:
	// 0: status (running)
	// 1: tool_call (read_file)
	// 2: tool_result (read_file)
	// 3: tool_call (bash)
	// 4: tool_result (bash)
	// 5: tool_call (git_diff)
	// 6: tool_result (git_diff)
	// 7: token (Done testing)
	// 8: completion
	expectedCount := 1 + (len(toolCalls) * 2) + 1 + 1
	if len(events) != expectedCount {
		t.Fatalf("expected %d events, got %d", expectedCount, len(events))
	}

	// Verify Tool 1: read_file
	if events[1].Type != protocol.EventToolCall {
		t.Errorf("expected event 1 type tool_call, got %s", events[1].Type)
	}
	var tc1 protocol.ToolCallPayload
	if err := events[1].UnmarshalPayload(&tc1); err != nil {
		t.Fatalf("failed to unmarshal tc1: %v", err)
	}
	if tc1.CallID != "call_1" || tc1.Tool != "read_file" || tc1.Args["path"] != "pkg/server/main.go" {
		t.Errorf("tc1 mismatch: %+v", tc1)
	}

	if events[2].Type != protocol.EventToolResult {
		t.Errorf("expected event 2 type tool_result, got %s", events[2].Type)
	}
	var tr1 protocol.ToolResultPayload
	if err := events[2].UnmarshalPayload(&tr1); err != nil {
		t.Fatalf("failed to unmarshal tr1: %v", err)
	}
	if tr1.CallID != "call_1" || tr1.Output != "package main\n\nfunc main() {}" {
		t.Errorf("tr1 mismatch: %+v", tr1)
	}

	// Verify Tool 2: bash
	if events[3].Type != protocol.EventToolCall {
		t.Errorf("expected event 3 type tool_call, got %s", events[3].Type)
	}
	var tc2 protocol.ToolCallPayload
	if err := events[3].UnmarshalPayload(&tc2); err != nil {
		t.Fatalf("failed to unmarshal tc2: %v", err)
	}
	if tc2.CallID != "call_2" || tc2.Tool != "bash" || tc2.Args["command"] != "go test ./..." {
		t.Errorf("tc2 mismatch: %+v", tc2)
	}

	if events[4].Type != protocol.EventToolResult {
		t.Errorf("expected event 4 type tool_result, got %s", events[4].Type)
	}
	var tr2 protocol.ToolResultPayload
	if err := events[4].UnmarshalPayload(&tr2); err != nil {
		t.Fatalf("failed to unmarshal tr2: %v", err)
	}
	if tr2.CallID != "call_2" || !strings.Contains(tr2.Output, `"stdout":"PASS"`) {
		t.Errorf("tr2 mismatch: %+v", tr2)
	}

	// Verify Tool 3: git_diff with nil args and result
	var tc3 protocol.ToolCallPayload
	if err := events[5].UnmarshalPayload(&tc3); err != nil {
		t.Fatalf("failed to unmarshal tc3: %v", err)
	}
	if tc3.CallID != "call_3" || tc3.Tool != "git_diff" || tc3.Args == nil {
		t.Errorf("tc3 mismatch: %+v", tc3)
	}

	var tr3 protocol.ToolResultPayload
	if err := events[6].UnmarshalPayload(&tr3); err != nil {
		t.Fatalf("failed to unmarshal tr3: %v", err)
	}
	if tr3.CallID != "call_3" || tr3.Output != "" {
		t.Errorf("tr3 mismatch: %+v", tr3)
	}
}

func TestSimulatedRunner_InteractiveQuery(t *testing.T) {
	t.Run("successful interactive query and resolution", func(t *testing.T) {
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "query_mesh_1",
				Question: "Are you ready to proceed with the release?",
				Timeout:  3 * time.Second,
			},
			Tokens:           []string{"Proceeding", " with release"},
			CompletionResult: "Release successfully deployed",
		})

		sink := newMockSink(context.Background())

		// Hook into sink to answer query when query event is received
		sink.onEvent = func(evt protocol.Event) {
			if evt.Type == protocol.EventQuery {
				var qp protocol.QueryPayload
				if err := evt.UnmarshalPayload(&qp); err == nil {
					go func() {
						time.Sleep(10 * time.Millisecond)
						sink.ResolveQuery(qp.QueryID, "yes, proceed")
					}()
				}
			}
		}

		err := r.Run(context.Background(), protocol.TaskRequest{}, sink)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}

		events := sink.Events()
		// status + query + 2 tokens + completion = 5 events
		if len(events) != 5 {
			t.Fatalf("expected 5 events, got %d", len(events))
		}

		if events[1].Type != protocol.EventQuery {
			t.Fatalf("expected event 1 type query, got %s", events[1].Type)
		}
		var qp protocol.QueryPayload
		if err := events[1].UnmarshalPayload(&qp); err != nil {
			t.Fatalf("failed to unmarshal query payload: %v", err)
		}
		if qp.QueryID != "query_mesh_1" || qp.Prompt != "Are you ready to proceed with the release?" {
			t.Errorf("unexpected query payload: %+v", qp)
		}

		var comp protocol.CompletionPayload
		if err := events[4].UnmarshalPayload(&comp); err != nil {
			t.Fatalf("failed to unmarshal completion: %v", err)
		}
		if comp.Result != "Release successfully deployed" {
			t.Errorf("unexpected completion: %s", comp.Result)
		}
	})

	t.Run("query times out when unanswered", func(t *testing.T) {
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "query_timeout_test",
				Question: "Will anyone answer?",
				Timeout:  20 * time.Millisecond,
			},
			Tokens: []string{"Never reached"},
		})

		sink := newMockSink(context.Background())
		err := r.Run(context.Background(), protocol.TaskRequest{}, sink)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("expected error containing 'timed out', got: %v", err)
		}

		events := sink.Events()
		// Must not have reached completion
		for _, evt := range events {
			if evt.Type == protocol.EventCompletion {
				t.Error("completion event should not have been emitted after timeout")
			}
		}
	})
}

func TestSimulatedRunner_ContextCancellation(t *testing.T) {
	t.Run("pre-canceled context aborts immediately", func(t *testing.T) {
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens: []string{"token1", "token2"},
		})
		sink := newMockSink(context.Background())

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := r.Run(ctx, protocol.TaskRequest{}, sink)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}

		if len(sink.Events()) != 0 {
			t.Errorf("expected 0 events on pre-canceled context, got %d", len(sink.Events()))
		}
	})

	t.Run("cancellation during token delay aborts mid-flight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens:     []string{"t1", "t2", "t3", "t4", "t5"},
			TokenDelay: 50 * time.Millisecond,
		})

		sink := newMockSink(ctx)
		sink.onEvent = func(evt protocol.Event) {
			if evt.Type == runner.EventToken {
				// Cancel as soon as first token is emitted
				cancel()
			}
		}

		err := r.Run(ctx, protocol.TaskRequest{}, sink)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}

		events := sink.Events()
		for _, evt := range events {
			if evt.Type == protocol.EventCompletion {
				t.Fatal("completion event should not be emitted after cancellation")
			}
		}
	})

	t.Run("cancellation during tool delay aborts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			ToolCalls: []runner.SimulatedToolCall{
				{
					ToolName: "long_tool",
					Delay:    500 * time.Millisecond,
				},
			},
		})

		sink := newMockSink(ctx)
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		err := r.Run(ctx, protocol.TaskRequest{}, sink)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}

		for _, evt := range sink.Events() {
			if evt.Type == protocol.EventCompletion {
				t.Fatal("completion event must not be emitted")
			}
		}
	})

	t.Run("cancellation during query wait aborts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "q_cancel",
				Question: "Waiting...",
				Timeout:  5 * time.Second,
			},
		})

		sink := newMockSink(ctx)
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		err := r.Run(ctx, protocol.TaskRequest{}, sink)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}

		for _, evt := range sink.Events() {
			if evt.Type == protocol.EventCompletion {
				t.Fatal("completion event must not be emitted")
			}
		}
	})
}

func TestSimulatedRunner_SimulatedError(t *testing.T) {
	expectedErr := errors.New("simulated fatal pipeline failure")
	r := runner.NewSimulatedRunner(runner.SimulatedOptions{
		Tokens:        []string{"Starting up...", "Something broke"},
		FailWithError: expectedErr,
	})

	sink := newMockSink(context.Background())
	err := r.Run(context.Background(), protocol.TaskRequest{}, sink)

	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got: %v", expectedErr, err)
	}

	events := sink.Events()
	// Sequence: status + 2 tokens + error = 4 events
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	lastEvt := events[len(events)-1]
	if lastEvt.Type != protocol.EventError {
		t.Fatalf("expected last event type error, got %s", lastEvt.Type)
	}

	var errorPayload protocol.ErrorPayload
	if err := lastEvt.UnmarshalPayload(&errorPayload); err != nil {
		t.Fatalf("failed to unmarshal error payload: %v", err)
	}

	if errorPayload.Code != "SIMULATED_FAILURE" {
		t.Errorf("expected code SIMULATED_FAILURE, got %s", errorPayload.Code)
	}
	if errorPayload.Message != expectedErr.Error() {
		t.Errorf("expected message %q, got %q", expectedErr.Error(), errorPayload.Message)
	}
	if !errorPayload.Fatal {
		t.Error("expected fatal error flag to be true")
	}

	for _, evt := range events {
		if evt.Type == protocol.EventCompletion {
			t.Fatal("completion event must not be emitted on error")
		}
	}
}

func TestSimulatedRunner_IntegrationWithManagedTask(t *testing.T) {
	t.Run("seamless integration with ManagedTask and TaskManager", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := task.NewTaskManager(tmpDir, 1*time.Hour)
		if err != nil {
			t.Fatalf("failed to create task manager: %v", err)
		}
		defer mgr.Close()

		req := protocol.TaskRequest{
			Agent:          "worker",
			Task:           "Implement runner abstraction",
			TimeoutSeconds: 30,
		}

		managedTask, err := mgr.CreateTask(req)
		if err != nil {
			t.Fatalf("failed to create managed task: %v", err)
		}

		// Subscribe to live events and history
		subCh, unsub, err := managedTask.Subscribe(0)
		if err != nil {
			t.Fatalf("failed to subscribe to task: %v", err)
		}
		defer unsub()

		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Tokens:           []string{"Step 1", "Step 2"},
			CompletionResult: "All integration tests passing",
			FilesChanged:     []string{"pkg/server/runner/runner.go"},
		})

		// Run runner using managedTask as EventSink
		err = r.Run(managedTask.Context(), managedTask.Request, managedTask)
		if err != nil {
			t.Fatalf("runner failed: %v", err)
		}

		// Verify task status transitioned to completed
		if managedTask.CurrentStatus() != protocol.TaskStatusCompleted {
			t.Errorf("expected task status completed, got %s", managedTask.CurrentStatus())
		}

		snapshot := managedTask.Snapshot()
		if snapshot.Status != protocol.TaskStatusCompleted {
			t.Errorf("snapshot status mismatch: %s", snapshot.Status)
		}
		if snapshot.Completion == nil {
			t.Fatal("expected non-nil completion in snapshot")
		}
		if snapshot.Completion.Result != "All integration tests passing" {
			t.Errorf("completion result mismatch: %s", snapshot.Completion.Result)
		}
		if len(snapshot.Completion.FilesChanged) != 1 || snapshot.Completion.FilesChanged[0] != "pkg/server/runner/runner.go" {
			t.Errorf("files changed mismatch: %+v", snapshot.Completion.FilesChanged)
		}
		if snapshot.StartedAt == 0 || snapshot.FinishedAt == 0 {
			t.Errorf("expected non-zero timestamps: started=%d finished=%d", snapshot.StartedAt, snapshot.FinishedAt)
		}

		// Drain subscriber channel
		var collected []protocol.Event
		for evt := range subCh {
			collected = append(collected, evt)
		}

		// Events: initial queued (from manager) + running (from runner) + 2 tokens + completion = 5
		if len(collected) != 5 {
			t.Fatalf("expected 5 collected events, got %d", len(collected))
		}
		if collected[0].Type != protocol.EventStatus {
			t.Errorf("first event must be status (queued), got %s", collected[0].Type)
		}
		if collected[1].Type != protocol.EventStatus {
			t.Errorf("second event must be status (running), got %s", collected[1].Type)
		}
		if collected[2].Type != runner.EventToken || collected[3].Type != runner.EventToken {
			t.Errorf("events 2 and 3 must be tokens")
		}
		if collected[4].Type != protocol.EventCompletion {
			t.Errorf("event 4 must be completion, got %s", collected[4].Type)
		}
	})

	t.Run("interactive query resolved via TaskManager.SubmitReply", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := task.NewTaskManager(tmpDir, 1*time.Hour)
		if err != nil {
			t.Fatalf("failed to create task manager: %v", err)
		}
		defer mgr.Close()

		req := protocol.TaskRequest{
			Agent:          "worker",
			Task:           "Interactive workflow test",
			TimeoutSeconds: 10,
		}

		managedTask, err := mgr.CreateTask(req)
		if err != nil {
			t.Fatalf("failed to create task: %v", err)
		}

		subCh, unsub, err := managedTask.Subscribe(0)
		if err != nil {
			t.Fatalf("failed to subscribe: %v", err)
		}
		defer unsub()

		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{
				QueryID:  "q_integ_1",
				Question: "Please confirm migration",
				Timeout:  3 * time.Second,
			},
			Tokens:           []string{"Applying", " migration..."},
			CompletionResult: "Migration applied successfully",
		})

		runErrCh := make(chan error, 1)
		go func() {
			runErrCh <- r.Run(managedTask.Context(), managedTask.Request, managedTask)
		}()

		// Wait for EventQuery on subscriber channel
		queryFound := false
		for evt := range subCh {
			if evt.Type == protocol.EventQuery {
				queryFound = true
				var qp protocol.QueryPayload
				if err := evt.UnmarshalPayload(&qp); err != nil {
					t.Fatalf("failed to unmarshal query payload: %v", err)
				}
				// Resolve query via TaskManager.SubmitReply
				err := mgr.SubmitReply(managedTask.TaskID, protocol.TaskReplyRequest{
					QueryID: qp.QueryID,
					Answer:  "confirmed by orchestrator",
				})
				if err != nil {
					t.Fatalf("failed to submit reply: %v", err)
				}
			}
			if evt.Type == protocol.EventCompletion {
				break
			}
		}

		if !queryFound {
			t.Fatal("expected EventQuery to be encountered")
		}

		select {
		case err := <-runErrCh:
			if err != nil {
				t.Fatalf("runner returned error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for runner to finish")
		}

		if managedTask.CurrentStatus() != protocol.TaskStatusCompleted {
			t.Errorf("expected task status completed, got %s", managedTask.CurrentStatus())
		}
	})

	t.Run("runner failure records error in ManagedTask state", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := task.NewTaskManager(tmpDir, 1*time.Hour)
		if err != nil {
			t.Fatalf("failed to create task manager: %v", err)
		}
		defer mgr.Close()

		managedTask, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "Failing task"})
		if err != nil {
			t.Fatalf("failed to create task: %v", err)
		}

		simErr := errors.New("out of disk space on remote node")
		r := runner.NewSimulatedRunner(runner.SimulatedOptions{
			FailWithError: simErr,
		})

		err = r.Run(managedTask.Context(), managedTask.Request, managedTask)
		if !errors.Is(err, simErr) {
			t.Fatalf("expected simErr, got: %v", err)
		}

		if managedTask.CurrentStatus() != protocol.TaskStatusFailed {
			t.Errorf("expected task status failed, got %s", managedTask.CurrentStatus())
		}

		snap := managedTask.Snapshot()
		if snap.Error == nil {
			t.Fatal("expected error in snapshot")
		}
		if snap.Error.Code != "SIMULATED_FAILURE" || snap.Error.Message != simErr.Error() {
			t.Errorf("unexpected error payload in snapshot: %+v", snap.Error)
		}
	})
}
