package http

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/federation"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/registry"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/runner"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/task"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/webhook"
)

// errNilManagedTask is returned when a nil task is handed to the scheduler.
var errNilManagedTask = errors.New("scheduler: nil managed task")

// SchedulerConfig defines the dependencies and initial policy for a TerritoryScheduler.
type SchedulerConfig struct {
	Mode              protocol.TerritoryMode
	TaskManager       *task.TaskManager
	TerritoryManager  *federation.TerritoryManager
	Registry          *registry.Registry
	Runner            runner.Runner
	WebhookDispatcher *webhook.Dispatcher
}

// TerritoryScheduler is the coordinator's territory semaphore. Depending on the
// configured protocol.TerritoryMode it queues, warns about, rejects, or ignores
// territory conflicts detected before a task starts running. It also owns the
// exclusive branch lock lifecycle for dispatched tasks and drains the FIFO queue
// automatically as running tasks finish.
type TerritoryScheduler struct {
	mu      sync.Mutex
	mode    protocol.TerritoryMode
	queue   []*task.ManagedTask
	running map[string]*task.ManagedTask

	taskManager       *task.TaskManager
	territoryManager  *federation.TerritoryManager
	registry          *registry.Registry
	runner            runner.Runner
	webhookDispatcher *webhook.Dispatcher
}

// NewTerritoryScheduler constructs a TerritoryScheduler, defaulting the mode to
// protocol.TerritoryModeQueue when it is empty or invalid. Missing dependencies
// are replaced with safe zero-value defaults so the scheduler is always usable.
func NewTerritoryScheduler(cfg SchedulerConfig) *TerritoryScheduler {
	mode := cfg.Mode
	if !mode.Valid() {
		mode = protocol.TerritoryModeQueue
	}

	s := &TerritoryScheduler{
		mode:              mode,
		queue:             make([]*task.ManagedTask, 0),
		running:           make(map[string]*task.ManagedTask),
		taskManager:       cfg.TaskManager,
		territoryManager:  cfg.TerritoryManager,
		registry:          cfg.Registry,
		runner:            cfg.Runner,
		webhookDispatcher: cfg.WebhookDispatcher,
	}

	if s.territoryManager == nil {
		s.territoryManager = federation.NewTerritoryManager(federation.ManagerConfig{})
	}
	if s.registry == nil {
		s.registry = registry.NewRegistry(0)
	}

	return s
}

// Mode returns the current territory mode.
func (s *TerritoryScheduler) Mode() protocol.TerritoryMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetMode updates the territory mode when m is a recognized value. Invalid modes are ignored.
func (s *TerritoryScheduler) SetMode(m protocol.TerritoryMode) {
	if !m.Valid() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
}

// QueueLen returns the number of tasks currently waiting in the FIFO queue.
func (s *TerritoryScheduler) QueueLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// RunningLen returns the number of tasks currently tracked as running.
func (s *TerritoryScheduler) RunningLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// QueuedTasks returns the task IDs of queued tasks in FIFO order.
func (s *TerritoryScheduler) QueuedTasks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0, len(s.queue))
	for _, t := range s.queue {
		ids = append(ids, t.TaskID)
	}
	return ids
}

// RunningTasks returns the task IDs of running tasks in deterministic (sorted) order.
func (s *TerritoryScheduler) RunningTasks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Cancel removes taskID from the queue, if present, and cancels the underlying task.
// It reports whether the task was found and removed from the queue.
func (s *TerritoryScheduler) Cancel(taskID string, reason string) bool {
	s.mu.Lock()
	idx := -1
	for i, t := range s.queue {
		if t.TaskID == taskID {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.mu.Unlock()
		return false
	}

	s.queue = append(s.queue[:idx], s.queue[idx+1:]...)
	s.mu.Unlock()

	if s.taskManager != nil {
		_ = s.taskManager.CancelTask(taskID, reason)
	}
	return true
}

// Schedule decides how to handle a newly created task according to the active mode:
//   - disabled: dispatch immediately, ignoring conflicts;
//   - strict: reject with an error when a running conflict exists;
//   - warn: emit a non-fatal warning and dispatch anyway;
//   - queue (default): enqueue on conflict, otherwise dispatch immediately.
//
// It returns queued=true only when the task was appended to the FIFO queue.
func (s *TerritoryScheduler) Schedule(mt *task.ManagedTask) (bool, error) {
	if mt == nil {
		return false, errNilManagedTask
	}

	switch s.Mode() {
	case protocol.TerritoryModeDisabled:
		return false, s.Dispatch(mt)

	case protocol.TerritoryModeStrict:
		s.mu.Lock()
		if conflict := s.findConflictLocked(mt); conflict != nil {
			s.mu.Unlock()
			return false, fmt.Errorf("territory conflict: %s", conflict.Message)
		}
		if err := s.dispatchUnderLock(mt); err != nil {
			s.mu.Unlock()
			return false, err
		}
		s.mu.Unlock()
		s.startRunner(mt)
		return false, nil

	case protocol.TerritoryModeWarn:
		if conflict := s.findConflict(mt); conflict != nil {
			s.emitConflictWarning(mt, conflict)
		}
		return false, s.Dispatch(mt)

	default:
		s.mu.Lock()
		if s.findConflictLocked(mt) != nil {
			s.enqueueByPriorityLocked(mt)
			s.mu.Unlock()
			return true, nil
		}
		err := s.dispatchUnderLock(mt)
		if err != nil {
			// A task that just finished has already published a terminal status but
			// may not have released its branch lock yet. Its completion drain has not
			// run at this point, so enqueuing the task here guarantees it is picked up
			// instead of being stranded in the queued state.
			if errors.Is(err, registry.ErrBranchLocked) {
				s.enqueueByPriorityLocked(mt)
				s.mu.Unlock()
				return true, nil
			}
			s.mu.Unlock()
			return false, err
		}
		s.mu.Unlock()
		s.startRunner(mt)
		return false, nil
	}
}

// Dispatch claims the branch lock, transitions the task to running, tracks it, and
// launches its runner asynchronously. It returns an error when the branch is already
// locked by another task or when the task cannot transition into the running state.
func (s *TerritoryScheduler) Dispatch(mt *task.ManagedTask) error {
	if mt == nil {
		return errNilManagedTask
	}

	s.mu.Lock()
	if _, exists := s.running[mt.TaskID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: task %q is already running", mt.TaskID)
	}
	if err := s.dispatchUnderLock(mt); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	s.startRunner(mt)
	return nil
}

// Drain scans the FIFO queue and dispatches every candidate that has no running
// territory conflict and whose branch lock can be claimed. It returns the number of
// tasks dispatched in this cycle.
func (s *TerritoryScheduler) Drain() int {
	s.mu.Lock()
	toStart := s.drainUnderLock()
	s.mu.Unlock()

	for _, mt := range toStart {
		s.startRunner(mt)
	}
	return len(toStart)
}

// dispatchUnderLock claims the branch lock, transitions the task into running, and
// records it in the running set. The caller must hold s.mu.
func (s *TerritoryScheduler) dispatchUnderLock(mt *task.ManagedTask) error {
	if err := s.claimBranchLock(mt); err != nil {
		return err
	}
	if err := mt.TransitionTo(protocol.TaskStatusRunning); err != nil {
		s.releaseBranchLock(mt)
		return err
	}
	s.running[mt.TaskID] = mt
	return nil
}

// drainUnderLock performs the queue scan described by Drain without starting runners,
// returning the tasks that were transitioned into running. The caller must hold s.mu.
func (s *TerritoryScheduler) drainUnderLock() []*task.ManagedTask {
	var toStart []*task.ManagedTask
	kept := make([]*task.ManagedTask, 0, len(s.queue))

	for _, t := range s.queue {
		if s.findConflictLocked(t) != nil {
			kept = append(kept, t)
			continue
		}
		if err := s.dispatchUnderLock(t); err != nil {
			kept = append(kept, t)
			continue
		}
		toStart = append(toStart, t)
	}

	s.queue = kept
	return toStart
}

// finishTask runs the cleanup every ending shares: webhook notification and complete(), which
// releases the branch lock and the territory. Calling it twice is harmless: the lock release is
// checked against the task that owns it and the queue drain is idempotent.
func (s *TerritoryScheduler) finishTask(mt *task.ManagedTask) {
	if s.webhookDispatcher != nil {
		s.notifyWebhooks(mt)
	}
	s.complete(mt)
}

// startRunner launches the configured runner asynchronously, guarding it with a panic
// recovery that emits a RUNNER_PANIC error and always releasing scheduler resources.
func (s *TerritoryScheduler) startRunner(mt *task.ManagedTask) {
	done := func() {}
	if s.taskManager != nil {
		done = s.taskManager.TrackRunner()
	}

	r := s.runner

	go func() {
		// One slot for the whole life of the task, retries included: the WaitGroup never goes back
		// to zero between attempts, so a shutdown always waits for the pending retry.
		defer done()
		// And one cleanup for the real end: webhooks + complete (which releases the branch lock
		// and the territory), whether the task completes, fails, times out or panics.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("scheduler runner panic on task %s: %v\n%s", mt.TaskID, rec, debug.Stack())
				_, _ = mt.EmitEvent(protocol.EventError, protocol.ErrorPayload{
					Code:    "RUNNER_PANIC",
					Message: fmt.Sprintf("runner panicked: %v", rec),
					Fatal:   true,
				})
				// A panicking runner leaves the task running forever otherwise.
				s.failTask(mt, "RUNNER_PANIC", fmt.Sprintf("runner panicked: %v", rec))
			}
			s.finishTask(mt)
		}()

		for {
			if r == nil {
				return
			}
			err := r.Run(mt.Context(), mt.Request, mt)
			if err == nil {
				return
			}
			// A runner may have finalized the task itself (for example by emitting an error
			// event): a terminal task must not be retried, and the recorded cause wins.
			switch mt.CurrentStatus() {
			case protocol.TaskStatusCompleted, protocol.TaskStatusFailed, protocol.TaskStatusCanceled:
				return
			}
			delay, retry := s.handleRetry(mt, err)
			if !retry {
				// Not retryable, or the retry budget is exhausted: fail the task with the cause of
				// the last attempt, so the state, the SSE event and the store all carry it.
				code, message := failureCause(err)
				s.failTask(mt, code, message)
				return
			}
			// The retry happens here, in this same goroutine: the task keeps running and keeps its
			// branch lock and its territory until it really finishes.
			timer := time.NewTimer(delay)
			select {
			case <-mt.Context().Done():
				timer.Stop()
				// Cancelled, shutting down, or out of time while waiting: never call the runner
				// again. A timeout is finalized with its own cause and the same cleanup.
				if errors.Is(mt.Context().Err(), context.DeadlineExceeded) {
					s.failTask(mt, "TASK_TIMEOUT", "task timeout")
				}
				return
			case <-timer.C:
			}
		}
	}()
}

// failTask finalizes a task through the manager so the cause is recorded in the state, the
// SSE stream and the store. It falls back to the plain status transition when no manager is
// configured or the task is already terminal.
func (s *TerritoryScheduler) failTask(mt *task.ManagedTask, code, message string) {
	if mt == nil {
		return
	}
	if s.taskManager != nil {
		if err := s.taskManager.FailTask(mt.TaskID, code, message); err == nil {
			return
		}
	}
	_ = mt.SetStatus(protocol.TaskStatusFailed)
}

// failureCause classifies a runner error into the stable cause the coordinator reports.
// The codes match the ones the pi runner emits, so webhook classification (task.timeout)
// keeps working through this path too.
func failureCause(err error) (string, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "TASK_TIMEOUT", "task timeout"
	case errors.Is(err, context.Canceled):
		return "TASK_CANCELED", "task canceled"
	case errors.Is(err, runner.ErrNodeLost):
		// The node accepted the task and its stream ended without a result: the node is lost.
		// The task is never retried (at-most-once), so the client decides whether to resend it.
		return "NODE_LOST", "node lost: stream ended without a result"
	default:
		return "DISPATCH_FAILED", fmt.Sprintf("dispatch failed: %v", err)
	}
}

// retryableCause reports whether a runner error is worth retrying. Only DISPATCH_FAILED (an
// unclassified runner or dispatch failure) is retried: TASK_TIMEOUT, TASK_CANCELED and NODE_LOST
// are terminal, and RUNNER_PANIC never reaches here (it is handled where the panic is recovered).
func retryableCause(err error) bool {
	code, _ := failureCause(err)
	return code == "DISPATCH_FAILED"
}

// handleRetry schedules a retry when the cause is retryable and retries remain, and reports whether
// the task will be retried. The task keeps running (no state transition), keeps its branch lock and
// its territory, and the runner is called again after the delay; the lock and the territory are
// released once, when the task really finishes.
func (s *TerritoryScheduler) handleRetry(mt *task.ManagedTask, lastErr error) (time.Duration, bool) {
	currentRetry := mt.RetryAttempts()
	if !retryableCause(lastErr) || currentRetry >= mt.Request.MaxRetries {
		if mt.Request.MaxRetries > 0 {
			log.Printf("scheduler: task %s finished after %d/%d retries: %v", mt.TaskID, currentRetry, mt.Request.MaxRetries, lastErr)
		}
		return 0, false
	}

	retryDelay := mt.Request.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 30 // default 30 seconds
	}

	nextRetry := mt.IncRetryAttempts()
	log.Printf("scheduler: task %s will retry %d/%d in %ds: %v", mt.TaskID, nextRetry, mt.Request.MaxRetries, retryDelay, lastErr)

	_, _ = mt.EmitEvent(protocol.EventRetry, protocol.RetryPayload{
		RetryNumber:   nextRetry,
		MaxRetries:    mt.Request.MaxRetries,
		RetryAfterSec: retryDelay,
		LastError:     lastErr.Error(),
	})

	return time.Duration(retryDelay) * time.Second, true
}

// notifyWebhooks sends webhook notifications based on task final status.
func (s *TerritoryScheduler) notifyWebhooks(mt *task.ManagedTask) {
	taskState := mt.Snapshot()

	switch mt.Status {
	case protocol.TaskStatusCompleted:
		s.webhookDispatcher.NotifyTaskCompleted(&taskState, "")
	case protocol.TaskStatusFailed:
		msg := "task failed"
		if mt.Error != nil {
			msg = mt.Error.Message
		}
		// Determine event type based on error code
		event := "task.failed"
		if mt.Error != nil {
			switch mt.Error.Code {
			case "TASK_TIMEOUT", "TASK_INACTIVITY_TIMEOUT":
				event = "task.timeout"
			}
		}
		s.webhookDispatcher.Notify(event, mt.TaskID, "failed", &taskState, nil, &msg)
	}
}

// complete releases the branch lock, removes the task from the running set, and drains
// the queue. Removal and drain happen under s.mu so a concurrent Schedule cannot lose
// its wake-up between observing a conflict and appending to the queue.
func (s *TerritoryScheduler) complete(mt *task.ManagedTask) {
	s.releaseBranchLock(mt)

	s.mu.Lock()
	delete(s.running, mt.TaskID)
	toStart := s.drainUnderLock()
	s.mu.Unlock()

	for _, next := range toStart {
		s.startRunner(next)
	}
}

// findConflict reports a running territory conflict for mt, if any.
func (s *TerritoryScheduler) findConflict(mt *task.ManagedTask) *protocol.TerritoryConflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findConflictLocked(mt)
}

// findConflictLocked reports a running territory conflict for mt. The caller must hold s.mu.
func (s *TerritoryScheduler) findConflictLocked(mt *task.ManagedTask) *protocol.TerritoryConflict {
	if s.territoryManager == nil {
		return nil
	}
	return s.territoryManager.FindRunningConflict(mt.Territory())
}

// ClampPriority bounds raw priority to [-100, 100] with explicit range checking
// before integer conversion, preventing integer overflow (CWE-190 / G115).
func ClampPriority(p int) int32 {
	if p < -100 {
		return -100
	}
	if p > 100 {
		return 100
	}
	return int32(p)
}

// enqueueByPriorityLocked inserts mt into the queue based on its priority.
// Higher priority tasks are placed near the front. The caller must hold s.mu.
func (s *TerritoryScheduler) enqueueByPriorityLocked(mt *task.ManagedTask) {
	priority := ClampPriority(mt.Request.Priority)

	// Find position to insert (higher priority first)
	insertPos := len(s.queue)
	for i := 0; i < len(s.queue); i++ {
		qPriority := ClampPriority(s.queue[i].Request.Priority)
		if priority > qPriority {
			insertPos = i
			break
		}
	}

	if insertPos == len(s.queue) {
		s.queue = append(s.queue, mt)
	} else {
		s.queue = append(s.queue[:insertPos], append([]*task.ManagedTask{mt}, s.queue[insertPos:]...)...)
	}
}

// emitConflictWarning records a non-fatal territory conflict warning on the task.
// It uses the thought event because it is the only non-terminal, text-carrying event
// type in the protocol; an error event would incorrectly fail the task.
func (s *TerritoryScheduler) emitConflictWarning(mt *task.ManagedTask, conflict *protocol.TerritoryConflict) {
	if conflict == nil {
		return
	}
	_, _ = mt.EmitEvent(protocol.EventThought, protocol.ThoughtPayload{
		Text: "territory conflict warning: " + conflict.Message,
	})
}

// claimBranchLock claims the exclusive repo/branch lock for mt when applicable.
func (s *TerritoryScheduler) claimBranchLock(mt *task.ManagedTask) error {
	if s.registry == nil || mt.Request.GitRepo == "" || mt.Request.GitBranch == "" {
		return nil
	}
	return s.registry.Locks().ClaimLock(mt.Request.GitRepo, mt.Request.GitBranch, mt.TaskID)
}

// releaseBranchLock releases the repo/branch lock held by mt when applicable.
func (s *TerritoryScheduler) releaseBranchLock(mt *task.ManagedTask) {
	if s.registry == nil || mt.Request.GitRepo == "" || mt.Request.GitBranch == "" {
		return
	}
	_ = s.registry.Locks().ReleaseLock(mt.Request.GitRepo, mt.Request.GitBranch, mt.TaskID)
}
