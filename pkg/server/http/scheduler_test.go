package http_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// schedulerGateRunner blocks each task on a per-label gate until the test releases it,
// then emits a completion event. It lets tests drive territory release deterministically.
type schedulerGateRunner struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func newSchedulerGateRunner() *schedulerGateRunner {
	return &schedulerGateRunner{gates: make(map[string]chan struct{})}
}

func (r *schedulerGateRunner) gateFor(key string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.gates[key]
	if !ok {
		ch = make(chan struct{}, 1)
		r.gates[key] = ch
	}
	return ch
}

func (r *schedulerGateRunner) release(key string) {
	g := r.gateFor(key)
	select {
	case g <- struct{}{}:
	default:
	}
}

func (r *schedulerGateRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	select {
	case <-r.gateFor(req.Task):
		_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "completed"})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// schedulerImmediateRunner completes every task synchronously.
type schedulerImmediateRunner struct{}

func (schedulerImmediateRunner) Run(_ context.Context, _ protocol.TaskRequest, sink runner.EventSink) error {
	_, err := sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"})
	return err
}

type schedulerHarness struct {
	tm      *task.TaskManager
	terrMgr *federation.TerritoryManager
	reg     *registry.Registry
	sched   *meshhttp.TerritoryScheduler
}

func newSchedulerHarness(t *testing.T, mode protocol.TerritoryMode, r runner.Runner) *schedulerHarness {
	t.Helper()

	tm, err := task.NewTaskManager(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("failed to create task manager: %v", err)
	}
	t.Cleanup(func() { _ = tm.Close() })

	reg := registry.NewRegistry(time.Minute)
	terrMgr := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:        "scheduler-test-coordinator",
		ClusterName:   "scheduler-test",
		LocalSource:   func() []protocol.ActiveTerritory { return tm.ActiveTerritories() },
		RunningSource: func() []protocol.ActiveTerritory { return tm.RunningTerritories() },
	})
	sched := meshhttp.NewTerritoryScheduler(meshhttp.SchedulerConfig{
		Mode:             mode,
		TaskManager:      tm,
		TerritoryManager: terrMgr,
		Registry:         reg,
		Runner:           r,
	})

	return &schedulerHarness{tm: tm, terrMgr: terrMgr, reg: reg, sched: sched}
}

func (h *schedulerHarness) createTask(t *testing.T, label, repo, branch string, surfaces ...string) *task.ManagedTask {
	t.Helper()

	mt, err := h.tm.CreateTask(protocol.TaskRequest{
		Agent:        "worker",
		Task:         label,
		GitRepo:      repo,
		GitBranch:    branch,
		EditSurfaces: surfaces,
	})
	if err != nil {
		t.Fatalf("failed to create task %q: %v", label, err)
	}
	return mt
}

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied within timeout")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func waitForEventContaining(t *testing.T, ch <-chan protocol.Event, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return false
			}
			if strings.Contains(string(evt.Payload), substr) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestTerritoryScheduler_NewDefaultsToQueueMode(t *testing.T) {
	h := newSchedulerHarness(t, "", schedulerImmediateRunner{})

	if got := h.sched.Mode(); got != protocol.TerritoryModeQueue {
		t.Fatalf("expected default mode %q, got %q", protocol.TerritoryModeQueue, got)
	}

	h.sched.SetMode(protocol.TerritoryModeStrict)
	if got := h.sched.Mode(); got != protocol.TerritoryModeStrict {
		t.Fatalf("expected mode %q after SetMode, got %q", protocol.TerritoryModeStrict, got)
	}

	h.sched.SetMode(protocol.TerritoryMode("bogus"))
	if got := h.sched.Mode(); got != protocol.TerritoryModeStrict {
		t.Fatalf("invalid mode must be ignored, got %q", got)
	}
}

func TestTerritoryScheduler_QueueModeFIFOAndDrain(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeQueue, r)

	a := h.createTask(t, "task-A", repo, "main")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}
	if got := h.sched.RunningLen(); got != 1 {
		t.Fatalf("expected 1 running task after A, got %d", got)
	}

	b := h.createTask(t, "task-B", repo, "main")
	if queued, err := h.sched.Schedule(b); err != nil || !queued {
		t.Fatalf("expected task B to be queued, got queued=%v err=%v", queued, err)
	}
	c := h.createTask(t, "task-C", repo, "main")
	if queued, err := h.sched.Schedule(c); err != nil || !queued {
		t.Fatalf("expected task C to be queued, got queued=%v err=%v", queued, err)
	}

	if got := h.sched.QueueLen(); got != 2 {
		t.Fatalf("expected queue length 2, got %d", got)
	}
	if got := h.sched.QueuedTasks(); !equalStrings(got, []string{b.TaskID, c.TaskID}) {
		t.Fatalf("expected FIFO queue [%s %s], got %v", b.TaskID, c.TaskID, got)
	}
	if got := h.sched.RunningTasks(); !equalStrings(got, []string{a.TaskID}) {
		t.Fatalf("expected running tasks [%s], got %v", a.TaskID, got)
	}
	if got := b.CurrentStatus(); got != protocol.TaskStatusQueued {
		t.Fatalf("expected queued task B to stay queued, got %s", got)
	}

	r.release("task-A")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 1 && h.sched.QueueLen() == 1 })
	if got := h.sched.RunningTasks(); !equalStrings(got, []string{b.TaskID}) {
		t.Fatalf("expected task B to be running after A completed, got %v", got)
	}
	if got := h.sched.QueuedTasks(); !equalStrings(got, []string{c.TaskID}) {
		t.Fatalf("expected task C to remain queued, got %v", got)
	}

	r.release("task-B")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 1 && h.sched.QueueLen() == 0 })
	if got := h.sched.RunningTasks(); !equalStrings(got, []string{c.TaskID}) {
		t.Fatalf("expected task C to be running after B completed, got %v", got)
	}

	r.release("task-C")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 && h.sched.QueueLen() == 0 })
	if got := c.CurrentStatus(); got != protocol.TaskStatusCompleted {
		t.Fatalf("expected task C to complete, got %s", got)
	}
}

func TestTerritoryScheduler_WarnModeDispatchesAndWarns(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeWarn, r)

	a := h.createTask(t, "warn-A", repo, "main", "pkg/auth")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}

	b := h.createTask(t, "warn-B", repo, "feature", "pkg/auth")
	events, unsub, err := h.tm.Subscribe(b.TaskID, 0)
	if err != nil {
		t.Fatalf("failed to subscribe to task B: %v", err)
	}
	defer unsub()

	queued, err := h.sched.Schedule(b)
	if err != nil {
		t.Fatalf("warn mode must not reject a conflicting task: %v", err)
	}
	if queued {
		t.Fatal("warn mode must dispatch immediately instead of queueing")
	}
	if got := h.sched.RunningLen(); got != 2 {
		t.Fatalf("expected both tasks running in warn mode, got %d", got)
	}
	if !waitForEventContaining(t, events, "territory conflict warning", 2*time.Second) {
		t.Fatal("expected a territory conflict warning event on the dispatched task")
	}

	r.release("warn-A")
	r.release("warn-B")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 })
}

func TestTerritoryScheduler_DisabledModeIgnoresConflicts(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeDisabled, r)

	a := h.createTask(t, "disabled-A", repo, "main", "pkg/auth")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}

	b := h.createTask(t, "disabled-B", repo, "feature", "pkg/auth")
	if queued, err := h.sched.Schedule(b); err != nil || queued {
		t.Fatalf("disabled mode must dispatch ignoring conflicts, got queued=%v err=%v", queued, err)
	}
	if got := h.sched.RunningLen(); got != 2 {
		t.Fatalf("expected both tasks running in disabled mode, got %d", got)
	}
	if got := h.sched.QueueLen(); got != 0 {
		t.Fatalf("disabled mode must never queue, got %d queued", got)
	}

	r.release("disabled-A")
	r.release("disabled-B")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 })
}

func TestTerritoryScheduler_StrictModeRejectsConflict(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeStrict, r)

	a := h.createTask(t, "strict-A", repo, "main")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}

	b := h.createTask(t, "strict-B", repo, "main")
	queued, err := h.sched.Schedule(b)
	if err == nil {
		t.Fatal("expected strict mode to reject the conflicting task")
	}
	if queued {
		t.Fatal("strict mode must not queue a rejected task")
	}
	if !strings.Contains(err.Error(), "territory conflict") {
		t.Fatalf("expected error to mention territory conflict, got %v", err)
	}
	if got := h.sched.RunningLen(); got != 1 {
		t.Fatalf("expected only task A running, got %d", got)
	}
	if got := h.sched.QueueLen(); got != 0 {
		t.Fatalf("expected empty queue after rejection, got %d", got)
	}

	r.release("strict-A")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 })
}

func TestTerritoryScheduler_CancelRemovesQueuedTask(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeQueue, r)

	a := h.createTask(t, "cancel-A", repo, "main")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}
	b := h.createTask(t, "cancel-B", repo, "main")
	if queued, err := h.sched.Schedule(b); err != nil || !queued {
		t.Fatalf("expected task B to be queued, got queued=%v err=%v", queued, err)
	}
	c := h.createTask(t, "cancel-C", repo, "main")
	if queued, err := h.sched.Schedule(c); err != nil || !queued {
		t.Fatalf("expected task C to be queued, got queued=%v err=%v", queued, err)
	}

	if h.sched.Cancel(a.TaskID, "must not cancel running task") {
		t.Fatal("Cancel must not remove a running task")
	}
	if !h.sched.Cancel(b.TaskID, "user requested") {
		t.Fatal("expected Cancel to remove queued task B")
	}
	if got := h.sched.QueueLen(); got != 1 {
		t.Fatalf("expected queue length 1 after cancel, got %d", got)
	}
	if got := h.sched.QueuedTasks(); !equalStrings(got, []string{c.TaskID}) {
		t.Fatalf("expected task C to remain queued, got %v", got)
	}
	if st, ok := h.tm.GetTask(b.TaskID); !ok || st.CurrentStatus() != protocol.TaskStatusCanceled {
		t.Fatalf("expected canceled task B, got found=%v status=%v", ok, st.CurrentStatus())
	}
	if h.sched.Cancel("does-not-exist", "x") {
		t.Fatal("expected Cancel of an unknown task to return false")
	}

	r.release("cancel-A")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 1 && h.sched.QueueLen() == 0 })
	if got := h.sched.RunningTasks(); !equalStrings(got, []string{c.TaskID}) {
		t.Fatalf("expected task C to run after A completed, got %v", got)
	}

	r.release("cancel-C")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 })
}

func TestTerritoryScheduler_DispatchRejectsLockedBranch(t *testing.T) {
	const repo = "github.com/org/repo"

	r := newSchedulerGateRunner()
	h := newSchedulerHarness(t, protocol.TerritoryModeQueue, r)

	a := h.createTask(t, "lock-A", repo, "main")
	if queued, err := h.sched.Schedule(a); err != nil || queued {
		t.Fatalf("expected task A to dispatch, got queued=%v err=%v", queued, err)
	}

	b := h.createTask(t, "lock-B", repo, "main")
	if err := h.sched.Dispatch(b); !errors.Is(err, registry.ErrBranchLocked) {
		t.Fatalf("expected ErrBranchLocked from Dispatch, got %v", err)
	}
	if got := b.CurrentStatus(); got != protocol.TaskStatusQueued {
		t.Fatalf("expected task B to remain queued after rejected dispatch, got %s", got)
	}

	r.release("lock-A")
	waitForCondition(t, func() bool { return h.sched.RunningLen() == 0 })
}

func TestTerritoryScheduler_ConcurrentScheduleAndDrain(t *testing.T) {
	const repo = "github.com/org/repo"
	const count = 24

	h := newSchedulerHarness(t, protocol.TerritoryModeQueue, schedulerImmediateRunner{})

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		tasks []*task.ManagedTask
	)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mt, err := h.tm.CreateTask(protocol.TaskRequest{
				Agent:     "worker",
				Task:      fmt.Sprintf("concurrent-%d", i),
				GitRepo:   repo,
				GitBranch: "main",
			})
			if err != nil {
				t.Errorf("failed to create task %d: %v", i, err)
				return
			}
			mu.Lock()
			tasks = append(tasks, mt)
			mu.Unlock()
			_, _ = h.sched.Schedule(mt)
		}(i)
	}
	wg.Wait()

	if len(tasks) != count {
		t.Fatalf("expected %d created tasks, got %d", count, len(tasks))
	}

	waitForCondition(t, func() bool {
		return h.sched.QueueLen() == 0 && h.sched.RunningLen() == 0
	})

	for _, mt := range tasks {
		if got := mt.CurrentStatus(); got != protocol.TaskStatusCompleted {
			t.Fatalf("task %s expected completed, got %s", mt.TaskID, got)
		}
	}
}
