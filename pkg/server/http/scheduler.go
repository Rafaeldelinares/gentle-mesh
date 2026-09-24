package http

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// errNilManagedTask is returned when a nil task is handed to the scheduler.
var errNilManagedTask = errors.New("scheduler: nil managed task")

// SchedulerConfig defines the dependencies and initial policy for a TerritoryScheduler.
type SchedulerConfig struct {
	Mode             protocol.TerritoryMode
	TaskManager      *task.TaskManager
	TerritoryManager *federation.TerritoryManager
	Registry         *registry.Registry
	Runner           runner.Runner
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

	taskManager      *task.TaskManager
	territoryManager *federation.TerritoryManager
	registry         *registry.Registry
	runner           runner.Runner
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
		mode:             mode,
		queue:            make([]*task.ManagedTask, 0),
		running:          make(map[string]*task.ManagedTask),
		taskManager:      cfg.TaskManager,
		territoryManager: cfg.TerritoryManager,
		registry:         cfg.Registry,
		runner:           cfg.Runner,
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
			s.queue = append(s.queue, mt)
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
				s.queue = append(s.queue, mt)
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

// startRunner launches the configured runner asynchronously, guarding it with a panic
// recovery that emits a RUNNER_PANIC error and always releasing scheduler resources.
func (s *TerritoryScheduler) startRunner(mt *task.ManagedTask) {
	done := func() {}
	if s.taskManager != nil {
		done = s.taskManager.TrackRunner()
	}

	r := s.runner
	go func() {
		defer done()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("scheduler runner panic on task %s: %v\n%s", mt.TaskID, rec, debug.Stack())
				_, _ = mt.EmitEvent(protocol.EventError, protocol.ErrorPayload{
					Code:    "RUNNER_PANIC",
					Message: fmt.Sprintf("runner panicked: %v", rec),
					Fatal:   true,
				})
			}
			s.complete(mt)
		}()

		if r == nil {
			return
		}
		_ = r.Run(mt.Context(), mt.Request, mt)
	}()
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
