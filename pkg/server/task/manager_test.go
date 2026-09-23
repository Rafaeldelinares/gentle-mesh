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
