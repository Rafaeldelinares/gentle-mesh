package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

func TestTaskStateMachine_ValidTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "test"})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	if task.Status != protocol.TaskStatusQueued {
		t.Fatalf("expected queued, got %s", task.Status)
	}

	// queued -> preparing
	if err := task.TransitionTo(protocol.TaskStatusPreparing); err != nil {
		t.Fatalf("transition to preparing failed: %v", err)
	}
	if task.CurrentStatus() != protocol.TaskStatusPreparing {
		t.Fatalf("expected preparing, got %s", task.CurrentStatus())
	}

	// preparing -> running
	if err := task.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}
	if task.CurrentStatus() != protocol.TaskStatusRunning {
		t.Fatalf("expected running, got %s", task.CurrentStatus())
	}
	if task.StartedAt == 0 {
		t.Fatal("expected StartedAt to be populated")
	}

	// running -> completed
	if err := task.TransitionTo(protocol.TaskStatusCompleted); err != nil {
		t.Fatalf("transition to completed failed: %v", err)
	}
	if task.CurrentStatus() != protocol.TaskStatusCompleted {
		t.Fatalf("expected completed, got %s", task.CurrentStatus())
	}
	if task.FinishedAt == 0 {
		t.Fatal("expected FinishedAt to be populated")
	}

	// Verify terminal state immutability
	if err := task.TransitionTo(protocol.TaskStatusRunning); err == nil {
		t.Fatal("expected error transitioning from terminal state, got nil")
	} else if !errors.Is(err, ErrInvalidStateTransition) || !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("expected ErrInvalidStateTransition and ErrTaskAlreadyFinished, got: %v", err)
	}
}

func TestTaskStateMachine_InvalidTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	// Test invalid: queued -> completed directly
	task1, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "test-invalid-1"})
	if err != nil {
		t.Fatalf("create task failed: %v", err)
	}
	if err := task1.TransitionTo(protocol.TaskStatusCompleted); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition for queued -> completed, got %v", err)
	}

	// Move task to running
	if err := task1.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}

	// Test invalid: running -> queued (backwards)
	if err := task1.TransitionTo(protocol.TaskStatusQueued); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition for running -> queued, got %v", err)
	}

	// Test invalid: running -> preparing (backwards)
	if err := task1.TransitionTo(protocol.TaskStatusPreparing); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition for running -> preparing, got %v", err)
	}

	// Test invalid: same state transition (running -> running)
	if err := task1.TransitionTo(protocol.TaskStatusRunning); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition for running -> running, got %v", err)
	}

	// Finish task
	if err := task1.TransitionTo(protocol.TaskStatusFailed); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}

	// Terminal state cannot transition to anything
	if err := task1.TransitionTo(protocol.TaskStatusRunning); !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("expected ErrTaskAlreadyFinished on terminal task, got %v", err)
	}
	if err := task1.TransitionTo(protocol.TaskStatusCanceled); !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("expected ErrTaskAlreadyFinished on terminal task, got %v", err)
	}
}

func TestTaskStateMachine_EmitEventsTerminal(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("manager init failed: %v", err)
	}
	defer mgr.Close()

	// Test completion event
	task1, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "comp-test"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	_ = task1.TransitionTo(protocol.TaskStatusRunning)

	compPayload := protocol.CompletionPayload{
		Result:       "all tests passed",
		CommitHash:   "abc1234",
		DurationMs:   250,
		FilesChanged: []string{"main.go"},
	}
	if _, err := task1.EmitEvent(protocol.EventCompletion, compPayload); err != nil {
		t.Fatalf("emit completion failed: %v", err)
	}

	if task1.CurrentStatus() != protocol.TaskStatusCompleted {
		t.Fatalf("expected status completed, got %s", task1.CurrentStatus())
	}
	if task1.FinishedAt == 0 {
		t.Fatal("expected FinishedAt to be set")
	}

	snap1 := task1.Snapshot()
	if snap1.Completion == nil || snap1.Completion.Result != "all tests passed" {
		t.Fatalf("expected completion payload in snapshot, got %+v", snap1.Completion)
	}

	// Further emit should fail
	if _, err := task1.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "after"}); !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("expected ErrTaskAlreadyFinished, got %v", err)
	}

	// Test error event
	task2, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "err-test"})
	if err != nil {
		t.Fatalf("create task 2: %v", err)
	}
	_ = task2.TransitionTo(protocol.TaskStatusRunning)

	errPayload := protocol.ErrorPayload{
		Code:    "compilation_failed",
		Message: "syntax error on line 42",
		Fatal:   true,
	}
	if _, err := task2.EmitEvent(protocol.EventError, errPayload); err != nil {
		t.Fatalf("emit error failed: %v", err)
	}

	if task2.CurrentStatus() != protocol.TaskStatusFailed {
		t.Fatalf("expected status failed, got %s", task2.CurrentStatus())
	}
	if task2.FinishedAt == 0 {
		t.Fatal("expected FinishedAt to be set")
	}

	snap2 := task2.Snapshot()
	if snap2.Error == nil || snap2.Error.Code != "compilation_failed" {
		t.Fatalf("expected error payload in snapshot, got %+v", snap2.Error)
	}
}

func TestTask_Queries(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "task-queries.jsonl")
	logger, _ := NewJSONLLogger(logPath)
	defer logger.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := NewManagedTask("task-q", protocol.TaskRequest{}, ctx, cancel, logger)

	// RegisterQuery & ResolveQuery sync delivery
	queryChan := task.RegisterQuery("q-1")
	if queryChan == nil {
		t.Fatal("expected queryChan to not be nil")
	}

	// Duplicate register should return existing channel
	dupChan := task.RegisterQuery("q-1")
	if dupChan != queryChan {
		t.Fatal("expected RegisterQuery to return existing channel for duplicate queryID")
	}

	// Resolve in background
	go func() {
		time.Sleep(10 * time.Millisecond)
		if err := task.ResolveQuery("q-1", "yes, proceed"); err != nil {
			t.Errorf("resolve query failed: %v", err)
		}
	}()

	select {
	case ans := <-queryChan:
		if ans != "yes, proceed" {
			t.Fatalf("expected 'yes, proceed', got %q", ans)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for query answer")
	}

	// Duplicate resolve on already answered query should fail
	if err := task.ResolveQuery("q-1", "second answer"); !errors.Is(err, ErrQueryNotFound) {
		t.Fatalf("expected ErrQueryNotFound on duplicate resolve, got %v", err)
	}

	// Resolve unknown query
	if err := task.ResolveQuery("q-unknown", "foo"); !errors.Is(err, ErrQueryNotFound) {
		t.Fatalf("expected ErrQueryNotFound for unknown query, got %v", err)
	}

	// Test WaitForQuery with timeout
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer timeoutCancel()

	ans, err := task.WaitForQuery(timeoutCtx, "q-timeout")
	if !errors.Is(err, ErrQueryTimedOut) {
		t.Fatalf("expected ErrQueryTimedOut, got ans=%q err=%v", ans, err)
	}

	// Test WaitForQuery success
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = task.ResolveQuery("q-success", "ok")
	}()

	ans, err = task.WaitForQuery(context.Background(), "q-success")
	if err != nil || ans != "ok" {
		t.Fatalf("expected 'ok' without error, got ans=%q err=%v", ans, err)
	}
}

func TestTaskManager_CreateGetList(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	req1 := protocol.TaskRequest{Agent: "scout", Task: "task-one"}
	t1, err := mgr.CreateTask(req1)
	if err != nil {
		t.Fatalf("CreateTask 1 failed: %v", err)
	}
	if t1.TaskID == "" {
		t.Fatal("expected non-empty task ID")
	}

	req2 := protocol.TaskRequest{Agent: "worker", Task: "task-two"}
	t2, err := mgr.CreateTask(req2)
	if err != nil {
		t.Fatalf("CreateTask 2 failed: %v", err)
	}
	if t2.TaskID == "" {
		t.Fatal("expected non-empty task ID for t2")
	}

	// GetTask
	gotT1, exists := mgr.GetTask(t1.TaskID)
	if !exists || gotT1.TaskID != t1.TaskID {
		t.Fatalf("GetTask failed for t1")
	}

	_, exists = mgr.GetTask("nonexistent")
	if exists {
		t.Fatal("expected nonexistent task not to exist")
	}

	// ListTasks
	tasks := mgr.ListTasks()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks in list, got %d", len(tasks))
	}
}

func TestTaskManager_CancelTask(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "cancel-me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Cancel unknown task
	if err := mgr.CancelTask("unknown", "none"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}

	// Cancel active task
	if err := mgr.CancelTask(task.TaskID, "user requested stop"); err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}

	// Status should be canceled
	if task.CurrentStatus() != protocol.TaskStatusCanceled {
		t.Fatalf("expected canceled status, got %s", task.CurrentStatus())
	}

	// Context should be canceled
	select {
	case <-task.Context().Done():
		// Expected
	default:
		t.Fatal("expected task context to be canceled")
	}

	// Subsequent CancelTask should return ErrTaskAlreadyFinished
	if err := mgr.CancelTask(task.TaskID, "again"); !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("expected ErrTaskAlreadyFinished, got %v", err)
	}
}

func TestTaskManager_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{
		Agent:          "worker",
		Task:           "timeout-test",
		TimeoutSeconds: 1, // 1 second timeout
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	select {
	case <-task.Context().Done():
		if !errors.Is(task.Context().Err(), context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded, got %v", task.Context().Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("task context did not time out as expected")
	}
}

func TestTaskManager_SubmitReply(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "reply-test"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	queryChan := task.RegisterQuery("confirm-deploy")

	// SubmitReply on nonexistent task
	if err := mgr.SubmitReply("fake-task", protocol.TaskReplyRequest{QueryID: "confirm-deploy", Answer: "approved"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}

	// SubmitReply on valid task
	if err := mgr.SubmitReply(task.TaskID, protocol.TaskReplyRequest{QueryID: "confirm-deploy", Answer: "approved"}); err != nil {
		t.Fatalf("SubmitReply failed: %v", err)
	}

	select {
	case ans := <-queryChan:
		if ans != "approved" {
			t.Fatalf("expected 'approved', got %q", ans)
		}
	default:
		t.Fatal("expected queryChan to have received answer")
	}

	// Second SubmitReply should return ErrQueryNotFound
	if err := mgr.SubmitReply(task.TaskID, protocol.TaskReplyRequest{QueryID: "confirm-deploy", Answer: "again"}); !errors.Is(err, ErrQueryNotFound) {
		t.Fatalf("expected ErrQueryNotFound on duplicate SubmitReply, got %v", err)
	}
}

func TestTaskManager_Subscribe_LiveAndHistory(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "stream-test"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Initial event (ID 1: status queued) is already emitted by CreateTask.
	// Emit event 2 (status preparing)
	_ = task.TransitionTo(protocol.TaskStatusPreparing)

	// Subscribe with sinceID = 0 (should get events 1 and 2 from history, then live events)
	eventsChan, unsub, err := mgr.Subscribe(task.TaskID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	// Emit live event 3 (status running)
	_ = task.TransitionTo(protocol.TaskStatusRunning)

	// Emit live event 4 (thought)
	_, _ = task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "doing work"})

	// Emit live event 5 (completion)
	_, _ = task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "success"})

	var received []protocol.Event
	for evt := range eventsChan {
		received = append(received, evt)
	}

	if len(received) != 5 {
		t.Fatalf("expected 5 events, got %d", len(received))
	}

	for i, evt := range received {
		expectedID := int64(i + 1)
		if evt.ID != expectedID {
			t.Errorf("event %d: expected ID %d, got %d", i, expectedID, evt.ID)
		}
	}

	if received[0].Type != protocol.EventStatus || received[4].Type != protocol.EventCompletion {
		t.Errorf("unexpected event types: first=%s, last=%s", received[0].Type, received[4].Type)
	}
}

func TestTaskManager_Subscribe_SinceID(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "since-test"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	_ = task.TransitionTo(protocol.TaskStatusPreparing) // ID 2
	_ = task.TransitionTo(protocol.TaskStatusRunning)   // ID 3
	_, _ = task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "working"}) // ID 4
	_, _ = task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"}) // ID 5

	// Subscribe with sinceID = 3
	eventsChan, unsub, err := mgr.Subscribe(task.TaskID, 3)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	var received []protocol.Event
	for evt := range eventsChan {
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events (ID 4 and 5), got %d", len(received))
	}
	if received[0].ID != 4 || received[1].ID != 5 {
		t.Fatalf("expected IDs 4 and 5, got %d and %d", received[0].ID, received[1].ID)
	}
}

func TestTaskManager_Subscribe_TerminalTask(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "term-sub"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = task.TransitionTo(protocol.TaskStatusRunning)
	_, _ = task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})

	// Subscribing to an already terminal task should replay history and immediately close
	eventsChan, unsub, err := mgr.Subscribe(task.TaskID, 0)
	if err != nil {
		t.Fatalf("Subscribe to terminal task failed: %v", err)
	}
	defer unsub()

	var count int
	for range eventsChan {
		count++
	}

	if count != 3 { // queued, running, completion
		t.Fatalf("expected 3 replayed events, got %d", count)
	}
}

func TestTaskManager_Subscribe_UnsubscribeCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "unsub-test"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	eventsChan, unsub, err := mgr.Subscribe(task.TaskID, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Consume initial queued event
	select {
	case evt := <-eventsChan:
		if evt.ID != 1 {
			t.Fatalf("expected event 1, got %d", evt.ID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for event 1")
	}

	// Unsubscribe
	unsub()

	// Double unsubscribe should be safe
	unsub()

	// Emit further events
	_ = task.TransitionTo(protocol.TaskStatusRunning)
	_, _ = task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "after unsub"})

	// Verify eventsChan is closed and drained
	select {
	case _, ok := <-eventsChan:
		if ok {
			t.Fatal("expected channel to be closed/empty after unsub, but got value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for closed channel")
	}
}

func TestTaskManager_FanOut(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "fanout"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	const numSubscribers = 5
	var subscriberChans []<-chan protocol.Event
	for i := 0; i < numSubscribers; i++ {
		ch, unsub, err := mgr.Subscribe(task.TaskID, 0)
		if err != nil {
			t.Fatalf("subscriber %d failed: %v", i, err)
		}
		defer unsub()
		subscriberChans = append(subscriberChans, ch)
	}

	// Emit events
	_ = task.TransitionTo(protocol.TaskStatusRunning)
	for i := 1; i <= 5; i++ {
		_, _ = task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: fmt.Sprintf("thought %d", i)})
	}
	_, _ = task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})

	// Total events: 1 (queued) + 1 (running) + 5 (thought) + 1 (completion) = 8
	const expectedCount = 8

	var wg sync.WaitGroup
	for idx, ch := range subscriberChans {
		wg.Add(1)
		go func(subIdx int, eventCh <-chan protocol.Event) {
			defer wg.Done()
			var count int
			for range eventCh {
				count++
			}
			if count != expectedCount {
				t.Errorf("subscriber %d received %d events, expected %d", subIdx, count, expectedCount)
			}
		}(idx, ch)
	}

	wg.Wait()
}

func TestTaskManager_CleanupExpired(t *testing.T) {
	tmpDir := t.TempDir()
	// TTL of 50ms
	mgr, err := NewTaskManager(tmpDir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	t1, _ := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "expired"})
	_ = t1.TransitionTo(protocol.TaskStatusRunning)
	_, _ = t1.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})

	t2, _ := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "active"})
	_ = t2.TransitionTo(protocol.TaskStatusRunning)

	// Before TTL: t1 and t2 exist
	mgr.CleanupExpired()
	if _, exists := mgr.GetTask(t1.TaskID); !exists {
		t.Fatal("expected t1 to exist before TTL elapsed")
	}

	// Wait for TTL to expire
	time.Sleep(80 * time.Millisecond)

	mgr.CleanupExpired()

	// t1 should be purged
	if _, exists := mgr.GetTask(t1.TaskID); exists {
		t.Fatal("expected t1 to be purged after TTL")
	}

	// t2 (active) should still exist
	if _, exists := mgr.GetTask(t2.TaskID); !exists {
		t.Fatal("expected active t2 to still exist")
	}
}

func TestTaskManager_Close(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}

	task, _ := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "close-test"})

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Task context should be canceled
	select {
	case <-task.Context().Done():
		// Expected
	default:
		t.Fatal("expected task context to be canceled on manager close")
	}

	// Further CreateTask should fail
	if _, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker"}); err == nil {
		t.Fatal("expected error creating task on closed manager")
	}
}

func TestTaskManager_ConcurrencyRace(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	const numTasks = 5
	const numOpsPerTask = 20
	var wg sync.WaitGroup

	for i := 0; i < numTasks; i++ {
		wg.Add(1)
		go func(taskIdx int) {
			defer wg.Done()

			task, err := mgr.CreateTask(protocol.TaskRequest{
				Agent: "worker",
				Task:  fmt.Sprintf("race-task-%d", taskIdx),
			})
			if err != nil {
				t.Errorf("failed to create task %d: %v", taskIdx, err)
				return
			}

			// Background subscriber
			subCh, unsub, err := mgr.Subscribe(task.TaskID, 0)
			if err == nil {
				go func() {
					for range subCh {
					}
				}()
				defer unsub()
			}

			_ = task.TransitionTo(protocol.TaskStatusPreparing)
			_ = task.TransitionTo(protocol.TaskStatusRunning)

			for op := 0; op < numOpsPerTask; op++ {
				_, _ = task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{
					Text: fmt.Sprintf("thought %d-%d", taskIdx, op),
				})
				_ = task.Snapshot()
				_ = mgr.ListTasks()
			}

			// Interactive query in concurrency
			qID := fmt.Sprintf("q-%d", taskIdx)
			_ = task.RegisterQuery(qID)
			_ = mgr.SubmitReply(task.TaskID, protocol.TaskReplyRequest{QueryID: qID, Answer: "ok"})

			_, _ = task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})
		}(i)
	}

	wg.Wait()
}

func TestTaskManager_AutomaticRetentionAndDiskCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	tm, err := NewTaskManager(tmpDir, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer tm.Close()

	task, err := tm.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "cleanup-retention-test"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	_ = task.TransitionTo(protocol.TaskStatusRunning)
	if _, err := task.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"}); err != nil {
		t.Fatalf("EmitEvent completion failed: %v", err)
	}

	if task.FinishedAt == 0 {
		t.Fatal("expected FinishedAt to be set")
	}

	logFile := filepath.Join(tmpDir, task.TaskID+".jsonl")
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("expected log file to exist on disk: %v", err)
	}

	time.Sleep(250 * time.Millisecond)

	if _, exists := tm.GetTask(task.TaskID); exists {
		t.Fatal("expected task to be purged from GetTask after TTL")
	}

	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("expected log file to no longer exist on disk, got err: %v", err)
	}

	if err := tm.Close(); err != nil {
		t.Fatalf("tm.Close() failed: %v", err)
	}
}

func TestTaskManager_TrackRunnerGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "track-runner-test"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	done := mgr.TrackRunner()
	runnerCompleted := make(chan struct{})

	go func() {
		defer done()
		<-task.Context().Done()
		// Emit event to verify logger is still open and writable before runner finishes
		_, emitErr := task.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: "finishing runner"})
		if emitErr != nil {
			t.Errorf("expected EmitEvent to succeed before logger closed, got: %v", emitErr)
		}
		close(runnerCompleted)
	}()

	// Close() should cancel all task contexts, wait for runners to finish, and then close loggers
	if err := mgr.Close(); err != nil {
		t.Fatalf("mgr.Close() failed: %v", err)
	}

	select {
	case <-runnerCompleted:
		// Succeeded: runner completed
	default:
		t.Fatal("expected runner to be completed when Close() finishes")
	}
}

func TestTaskManager_WithStore_SaveAndUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	dbPath := filepath.Join(tmpDir, "tasks.db")

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	mgr, err := NewTaskManager(tasksDir, 0, WithStore(s))
	if err != nil {
		t.Fatalf("failed to create manager with store: %v", err)
	}
	defer mgr.Close()

	if mgr.Store() != s {
		t.Fatalf("expected mgr.Store() to return configured store")
	}

	ctx := context.Background()

	// 1. Task Creation -> queued in store
	req1 := protocol.TaskRequest{
		Agent:        "worker",
		Task:         "run unit tests",
		GitRepo:      "gentle-mesh",
		GitBranch:    "feat/store",
		Domain:       "testing",
		EditSurfaces: []string{"pkg/server/task/task.go"},
	}
	task1, err := mgr.CreateTask(req1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	rec1, err := s.GetTask(ctx, task1.TaskID)
	if err != nil {
		t.Fatalf("GetTask failed on queued task: %v", err)
	}
	if rec1.Status != protocol.TaskStatusQueued {
		t.Errorf("expected store status queued, got %s", rec1.Status)
	}
	if rec1.Domain != "testing" {
		t.Errorf("expected domain 'testing', got %q", rec1.Domain)
	}
	if rec1.Branch != "feat/store" {
		t.Errorf("expected branch 'feat/store', got %q", rec1.Branch)
	}

	// 2. Lifecycle Event: Transition to running
	if err := task1.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("TransitionTo running failed: %v", err)
	}

	rec1, err = s.GetTask(ctx, task1.TaskID)
	if err != nil {
		t.Fatalf("GetTask failed after running: %v", err)
	}
	if rec1.Status != protocol.TaskStatusRunning {
		t.Errorf("expected store status running, got %s", rec1.Status)
	}
	if rec1.StartedAt == 0 {
		t.Errorf("expected StartedAt to be populated in store")
	}

	// 3. Lifecycle Event: Complete with result
	compPayload := protocol.CompletionPayload{
		Result:       "all 42 tests passed",
		FilesChanged: []string{"pkg/server/task/task.go", "pkg/server/task/manager.go"},
	}
	if _, err := task1.EmitEvent(protocol.EventCompletion, compPayload); err != nil {
		t.Fatalf("EmitEvent completion failed: %v", err)
	}

	rec1, err = s.GetTask(ctx, task1.TaskID)
	if err != nil {
		t.Fatalf("GetTask failed after completion: %v", err)
	}
	if rec1.Status != protocol.TaskStatusCompleted {
		t.Errorf("expected store status completed, got %s", rec1.Status)
	}
	if rec1.ResultSummary != "all 42 tests passed" {
		t.Errorf("expected result summary 'all 42 tests passed', got %q", rec1.ResultSummary)
	}
	if rec1.FinishedAt == 0 {
		t.Errorf("expected FinishedAt to be populated in store")
	}

	// 4. Lifecycle Event: Task 2 with error
	req2 := protocol.TaskRequest{
		Agent:     "worker",
		Task:      "build project",
		GitRepo:   "gentle-mesh",
		GitBranch: "feat/store",
		Domain:    "build",
	}
	task2, err := mgr.CreateTask(req2)
	if err != nil {
		t.Fatalf("CreateTask 2 failed: %v", err)
	}

	if err := task2.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("TransitionTo running on task 2 failed: %v", err)
	}

	errPayload := protocol.ErrorPayload{
		Code:    "build_failure",
		Message: "syntax error in main.go",
	}
	if _, err := task2.EmitEvent(protocol.EventError, errPayload); err != nil {
		t.Fatalf("EmitEvent error failed: %v", err)
	}

	rec2, err := s.GetTask(ctx, task2.TaskID)
	if err != nil {
		t.Fatalf("GetTask 2 failed after error: %v", err)
	}
	if rec2.Status != protocol.TaskStatusFailed {
		t.Errorf("expected store status failed, got %s", rec2.Status)
	}
	if rec2.ErrorMessage != "syntax error in main.go" {
		t.Errorf("expected error message 'syntax error in main.go', got %q", rec2.ErrorMessage)
	}
	if rec2.FinishedAt == 0 {
		t.Errorf("expected FinishedAt to be populated in store for task 2")
	}
}

func TestTaskManager_CrashRecovery_Rehydration(t *testing.T) {
	// 1. Create TaskManager with a store.SQLiteStore in a temp dir.
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	dbPath := filepath.Join(tmpDir, "mesh-recovery.db")

	store1, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store 1: %v", err)
	}

	mgr1, err := NewTaskManager(tasksDir, 0, WithStore(store1))
	if err != nil {
		t.Fatalf("failed to create mgr1: %v", err)
	}

	// 2. Create 2 tasks (run task 1 to completion with a result, run task 2 to failed with error).
	task1, err := mgr1.CreateTask(protocol.TaskRequest{
		Agent:        "worker",
		Task:         "run security audit",
		GitRepo:      "gentle-mesh",
		GitBranch:    "main",
		Domain:       "security",
		EditSurfaces: []string{"pkg/auth/token.go"},
	})
	if err != nil {
		t.Fatalf("CreateTask 1 failed: %v", err)
	}
	if err := task1.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("task1 TransitionTo running failed: %v", err)
	}
	compPayload := protocol.CompletionPayload{
		Result:       "audit clean: 0 vulnerabilities found",
		FilesChanged: []string{"pkg/auth/token.go"},
	}
	if _, err := task1.EmitEvent(protocol.EventCompletion, compPayload); err != nil {
		t.Fatalf("task1 EmitEvent completion failed: %v", err)
	}

	task2, err := mgr1.CreateTask(protocol.TaskRequest{
		Agent:        "worker",
		Task:         "execute integration suite",
		GitRepo:      "gentle-mesh",
		GitBranch:    "main",
		Domain:       "integration",
		EditSurfaces: []string{"pkg/runner/run.go"},
	})
	if err != nil {
		t.Fatalf("CreateTask 2 failed: %v", err)
	}
	if err := task2.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("task2 TransitionTo running failed: %v", err)
	}
	errPayload := protocol.ErrorPayload{
		Code:    "integration_timeout",
		Message: "service worker unreachable after 30s",
	}
	if _, err := task2.EmitEvent(protocol.EventError, errPayload); err != nil {
		t.Fatalf("task2 EmitEvent error failed: %v", err)
	}

	// 3. Close the TaskManager (simulating server shutdown/crash).
	if err := mgr1.Close(); err != nil {
		t.Fatalf("mgr1.Close() failed: %v", err)
	}

	// 4. Create a BRAND NEW TaskManager with a NEW store.SQLiteStore pointing to the SAME sqlite file.
	store2, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store 2: %v", err)
	}

	mgr2, err := NewTaskManager(tasksDir, 0, WithStore(store2))
	if err != nil {
		t.Fatalf("failed to create mgr2 with rehydration: %v", err)
	}
	defer mgr2.Close()

	// 5. Verify both tasks are recovered in mgr2.ListTasks(), can be retrieved via mgr2.GetTask(task1.TaskID),
	// and maintain exact status, error, and completion data.
	allTasks := mgr2.ListTasks()
	if len(allTasks) != 2 {
		t.Fatalf("expected 2 tasks in ListTasks(), got %d", len(allTasks))
	}

	// Verify task 1 recovery
	recTask1, exists1 := mgr2.GetTask(task1.TaskID)
	if !exists1 {
		t.Fatalf("task 1 (%s) not found in mgr2", task1.TaskID)
	}
	if recTask1.CurrentStatus() != protocol.TaskStatusCompleted {
		t.Errorf("task 1 expected status completed, got %s", recTask1.CurrentStatus())
	}
	snap1 := recTask1.Snapshot()
	if snap1.Status != protocol.TaskStatusCompleted {
		t.Errorf("snap1 status mismatch: expected completed, got %s", snap1.Status)
	}
	if snap1.Completion == nil {
		t.Fatalf("snap1 expected non-nil Completion")
	}
	if snap1.Completion.Result != "audit clean: 0 vulnerabilities found" {
		t.Errorf("snap1 completion result mismatch: %q", snap1.Completion.Result)
	}
	if snap1.FinishedAt == 0 {
		t.Errorf("snap1 expected FinishedAt > 0")
	}
	if recTask1.Domain != "security" {
		t.Errorf("task 1 domain mismatch: %q", recTask1.Domain)
	}

	// Verify task 2 recovery
	recTask2, exists2 := mgr2.GetTask(task2.TaskID)
	if !exists2 {
		t.Fatalf("task 2 (%s) not found in mgr2", task2.TaskID)
	}
	if recTask2.CurrentStatus() != protocol.TaskStatusFailed {
		t.Errorf("task 2 expected status failed, got %s", recTask2.CurrentStatus())
	}
	snap2 := recTask2.Snapshot()
	if snap2.Status != protocol.TaskStatusFailed {
		t.Errorf("snap2 status mismatch: expected failed, got %s", snap2.Status)
	}
	if snap2.Error == nil {
		t.Fatalf("snap2 expected non-nil Error")
	}
	if snap2.Error.Message != "service worker unreachable after 30s" {
		t.Errorf("snap2 error message mismatch: %q", snap2.Error.Message)
	}
	if snap2.FinishedAt == 0 {
		t.Errorf("snap2 expected FinishedAt > 0")
	}
	if recTask2.Domain != "integration" {
		t.Errorf("task 2 domain mismatch: %q", recTask2.Domain)
	}

	// Verify historical event replay from JSONL on recovered task
	subCh, unsub, err := recTask1.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe on recovered task failed: %v", err)
	}
	defer unsub()

	var events []protocol.Event
	for evt := range subCh {
		events = append(events, evt)
	}
	if len(events) == 0 {
		t.Errorf("expected historical events to be replayed from JSONL for recovered task 1")
	}
}

func TestManagedTask_NotifyUpdate_SafetyAndHelpers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := NewManagedTask("task-safety-test", protocol.TaskRequest{Agent: "worker"}, ctx, cancel, nil)

	// Verify notifyUpdate handles nil onUpdate without panic
	task.notifyUpdate()

	// Verify notifyUpdate handles panicking onUpdate without panic
	task.SetOnUpdate(func(t *ManagedTask) {
		panic("simulated callback panic")
	})
	task.notifyUpdate() // should absorb panic safely

	// Verify SetStatus and UpdateActivity invoke callback
	var updates int
	var lastStatus protocol.TaskStatus
	var lastAction string

	task.SetOnUpdate(func(t *ManagedTask) {
		updates++
		lastStatus = t.CurrentStatus()
		t.mu.RLock()
		lastAction = t.CurrentAction
		t.mu.RUnlock()
	})

	if err := task.SetStatus(protocol.TaskStatusPreparing); err != nil {
		t.Fatalf("SetStatus preparing failed: %v", err)
	}
	if updates != 1 || lastStatus != protocol.TaskStatusPreparing {
		t.Errorf("expected 1 update with status preparing, got %d and %s", updates, lastStatus)
	}

	// Same status should be a no-op
	if err := task.SetStatus(protocol.TaskStatusPreparing); err != nil {
		t.Fatalf("SetStatus same status failed: %v", err)
	}
	if updates != 1 {
		t.Errorf("expected no additional update for same status, got %d", updates)
	}

	// UpdateActivity
	task.UpdateActivity("executing step 1")
	if updates != 2 || lastAction != "executing step 1" {
		t.Errorf("expected 2 updates and action 'executing step 1', got %d and %q", updates, lastAction)
	}

	// UpdateActivity with phase
	task.UpdateActivity("verify", "running assertions")
	if updates != 3 || lastAction != "running assertions" {
		t.Errorf("expected 3 updates and action 'running assertions', got %d and %q", updates, lastAction)
	}
	if task.Phase != protocol.AgentPhaseVerify {
		t.Errorf("expected phase verify, got %s", task.Phase)
	}
}

func TestTaskManager_WithStore_CancelTask(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")

	memStore := store.NewMemoryStore()
	mgr, err := NewTaskManager(tasksDir, 0, WithStore(memStore))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	task, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "cancel test"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	if err := mgr.CancelTask(task.TaskID, "user requested stop"); err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}

	rec, err := memStore.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if rec.Status != protocol.TaskStatusCanceled {
		t.Errorf("expected store status canceled, got %s", rec.Status)
	}
}

func TestTaskManager_RunningTerritories(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewTaskManager(tmpDir, 0)
	if err != nil {
		t.Fatalf("NewTaskManager: %v", err)
	}
	defer mgr.Close()

	req := func(name, branch string) protocol.TaskRequest {
		return protocol.TaskRequest{
			Agent:     "worker",
			Task:      name,
			GitRepo:   "github.com/org/repo",
			GitBranch: branch,
		}
	}

	queued, err := mgr.CreateTask(req("queued-task", "feature/queued"))
	if err != nil {
		t.Fatalf("CreateTask queued: %v", err)
	}
	preparing, err := mgr.CreateTask(req("preparing-task", "feature/preparing"))
	if err != nil {
		t.Fatalf("CreateTask preparing: %v", err)
	}
	running, err := mgr.CreateTask(req("running-task", "feature/running"))
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}

	// Initially every task is queued: RunningTerritories must be empty while
	// ActiveTerritories still reports all three.
	if got := mgr.RunningTerritories(); len(got) != 0 {
		t.Fatalf("expected 0 running territories for all-queued tasks, got %d: %+v", len(got), got)
	}
	if got := mgr.ActiveTerritories(); len(got) != 3 {
		t.Fatalf("expected ActiveTerritories to still report 3 queued tasks, got %d", len(got))
	}

	if err := preparing.TransitionTo(protocol.TaskStatusPreparing); err != nil {
		t.Fatalf("transition to preparing failed: %v", err)
	}
	if err := running.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}

	got := mgr.RunningTerritories()
	if len(got) != 2 {
		t.Fatalf("expected 2 running territories, got %d: %+v", len(got), got)
	}
	ids := make(map[string]bool, len(got))
	for _, terr := range got {
		ids[terr.TaskID] = true
		if terr.Repo != "github.com/org/repo" || terr.Branch == "" {
			t.Errorf("unexpected territory contents for %s: %+v", terr.TaskID, terr)
		}
	}
	if ids[queued.TaskID] {
		t.Errorf("queued task %s must be excluded from RunningTerritories", queued.TaskID)
	}
	if !ids[preparing.TaskID] {
		t.Errorf("preparing task %s must be included in RunningTerritories", preparing.TaskID)
	}
	if !ids[running.TaskID] {
		t.Errorf("running task %s must be included in RunningTerritories", running.TaskID)
	}

	// Terminal states are excluded: complete the running task.
	if err := running.TransitionTo(protocol.TaskStatusCompleted); err != nil {
		t.Fatalf("transition to completed failed: %v", err)
	}
	got = mgr.RunningTerritories()
	if len(got) != 1 || got[0].TaskID != preparing.TaskID {
		t.Fatalf("expected only preparing task after completion, got %+v", got)
	}

	// Canceling the queued task keeps it excluded.
	if err := mgr.CancelTask(queued.TaskID, "no longer needed"); err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}
	got = mgr.RunningTerritories()
	if len(got) != 1 || got[0].TaskID != preparing.TaskID {
		t.Fatalf("expected only preparing task after canceling queued task, got %+v", got)
	}
}
