package http_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// lifecycleErrorRunner returns the configured error for every task.
type lifecycleErrorRunner struct{ err error }

func (r *lifecycleErrorRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	return r.err
}

func newLifecycleScheduler(t *testing.T, mgr *task.TaskManager, r runner.Runner) *meshhttp.TerritoryScheduler {
	t.Helper()
	return meshhttp.NewTerritoryScheduler(meshhttp.SchedulerConfig{
		Mode:             protocol.TerritoryModeQueue,
		TaskManager:      mgr,
		TerritoryManager: federation.NewTerritoryManager(federation.ManagerConfig{}),
		Registry:         registry.NewRegistry(0),
		Runner:           r,
	})
}

func waitStatus(t *testing.T, mgr *task.TaskManager, id string, want protocol.TaskStatus) protocol.TaskState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mt, ok := mgr.GetTask(id); ok {
			if snap := mt.Snapshot(); snap.Status == want {
				return snap
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	mt, _ := mgr.GetTask(id)
	if mt == nil {
		t.Fatalf("task %s disappeared", id)
	}
	t.Fatalf("task %s never reached %s (status=%s)", id, want, mt.Snapshot().Status)
	return protocol.TaskState{}
}

// A runner error must leave the cause in the state (with the timeout code that makes the
// task.timeout webhook fire) and in the task log.
func TestSchedulerFailureCarriesTimeoutCause(t *testing.T) {
	dir := t.TempDir()
	mgr, err := task.NewTaskManager(dir, 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()
	sched := newLifecycleScheduler(t, mgr, &lifecycleErrorRunner{err: context.DeadlineExceeded})

	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "t", TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if _, err := sched.Schedule(mt); err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}
	snap := waitStatus(t, mgr, mt.TaskID, protocol.TaskStatusFailed)
	if snap.Error == nil || snap.Error.Code != "TASK_TIMEOUT" {
		t.Errorf("expected the TASK_TIMEOUT cause (webhook condition), got %+v", snap.Error)
	}
	if snap.Error != nil && !strings.Contains(snap.Error.Message, "task timeout") {
		t.Errorf("expected the stable 'task timeout' cause, got %q", snap.Error.Message)
	}
	logBytes, _ := os.ReadFile(filepath.Join(dir, mt.TaskID+".jsonl"))
	if !strings.Contains(string(logBytes), `"type":"error"`) || !strings.Contains(string(logBytes), "TASK_TIMEOUT") {
		t.Errorf("the log must contain the error event, got %s", string(logBytes))
	}
}

// A transport error must be reported as a dispatch failure with the error detail.
func TestSchedulerFailureCarriesDispatchCause(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()
	sched := newLifecycleScheduler(t, mgr, &lifecycleErrorRunner{err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")})

	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "t"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if _, err := sched.Schedule(mt); err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}
	snap := waitStatus(t, mgr, mt.TaskID, protocol.TaskStatusFailed)
	if snap.Error == nil || snap.Error.Code != "DISPATCH_FAILED" || !strings.Contains(snap.Error.Message, "dispatch failed: ") {
		t.Errorf("expected a dispatch failure cause, got %+v", snap.Error)
	}
}

// probeOrderRunner records the dispatch order and completes (or panics for) each task.
type probeOrderRunner struct {
	mu        sync.Mutex
	order     []string
	panicRepo string
	delay     time.Duration
}

func (r *probeOrderRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	id := ""
	if mt, ok := sink.(*task.ManagedTask); ok {
		id = mt.TaskID
	}
	r.mu.Lock()
	r.order = append(r.order, id)
	doPanic := r.panicRepo != "" && req.GitRepo == r.panicRepo
	r.mu.Unlock()
	if doPanic {
		panic("probe runner panic")
	}
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	_, err := sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "done"})
	return err
}

func (r *probeOrderRunner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// An old queued task is failed at startup (never re-dispatched), and a task created afterwards
// on the same repository and branch runs normally: the territory is free.
func TestSchedulerFailsOldQueuedTasksAndNewTaskRuns(t *testing.T) {
	mem := store.NewMemoryStore()
	id := "task-old-queued"
	if err := mem.SaveTask(context.Background(), &store.TaskRecord{
		TaskID: id, Branch: "b", Repository: "r", Domain: "d",
		BlastRadius: protocol.BlastRadiusIsolated, Phase: "queued", CurrentAction: "Task queued",
		Status: protocol.TaskStatusQueued, Tags: []string{"t"}, EditSurfaces: []string{"f.go"},
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	rec := &probeOrderRunner{}
	sched := newLifecycleScheduler(t, mgr, rec)
	if snap := waitStatus(t, mgr, id, protocol.TaskStatusFailed); snap.Error == nil || snap.Error.Code != task.OrphanedTaskCode {
		t.Errorf("the old queued task must fail with the restart cause, got %+v", snap.Error)
	}
	if got := rec.seen(); len(got) != 0 {
		t.Errorf("the old queued task must not be dispatched again, got %v", got)
	}
	if sched.QueueLen() != 0 {
		t.Errorf("nothing may be re-queued at startup, got %d", sched.QueueLen())
	}

	// A new task on the same repository and branch runs: no lock or territory is held.
	nuevo, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "new", GitRepo: "r", GitBranch: "b", Domain: "d", EditSurfaces: []string{"f.go"}})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if _, err := sched.Schedule(nuevo); err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}
	waitStatus(t, mgr, nuevo.TaskID, protocol.TaskStatusCompleted)
	if got := rec.seen(); len(got) != 1 || got[0] != nuevo.TaskID {
		t.Errorf("only the new task must run, got %v", got)
	}
}

// A panicking runner finalizes its task with the panic cause and releases the branch lock, and
// FailTask stays safe (no second panic, cause preserved).
func TestSchedulerPanickingRunnerFinalizesTaskAndReleasesLock(t *testing.T) {
	reg := registry.NewRegistry(0)
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(store.NewMemoryStore()))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()
	sched := meshhttp.NewTerritoryScheduler(meshhttp.SchedulerConfig{
		Mode:             protocol.TerritoryModeQueue,
		TaskManager:      mgr,
		TerritoryManager: federation.NewTerritoryManager(federation.ManagerConfig{}),
		Registry:         reg,
		Runner:           &probeOrderRunner{panicRepo: "r-lock"},
	})
	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "t", GitRepo: "r-lock", GitBranch: "b-lock"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if _, err := sched.Schedule(mt); err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}
	snap := waitStatus(t, mgr, mt.TaskID, protocol.TaskStatusFailed)
	if snap.Error == nil || snap.Error.Code != "RUNNER_PANIC" {
		t.Errorf("expected the panic cause, got %+v", snap.Error)
	}
	// The lock is released by complete() after the task is finalized, so there is a real window
	// between "failed" and "lock released": poll for it with a bounded wait instead of asserting
	// immediately, and still fail if it never happens.
	locked := true
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if _, _, stillLocked := reg.Locks().GetLock("r-lock", "b-lock"); !stillLocked {
			locked = false
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if locked {
		t.Error("the branch lock must be released after the panic (still locked after 2s)")
	}
	if err := mgr.FailTask(mt.TaskID, "OTHER", "other"); err == nil {
		t.Error("FailTask on a terminal task must report an error")
	}
	if got := mt.Snapshot().Error.Code; got != "RUNNER_PANIC" {
		t.Errorf("the original cause must be preserved, got %s", got)
	}
}

// A preparing row cannot have a runner attached (only dispatchUnderLock starts one, and it
// transitions to running first), so failing it at startup loses nothing and frees the territory.
func TestSchedulerPreparingRowIsFailedAndTerritoryIsFree(t *testing.T) {
	mem := store.NewMemoryStore()
	if err := mem.SaveTask(context.Background(), &store.TaskRecord{
		TaskID: "prep-1", Branch: "branch-p", Repository: "repo-p", Domain: "dom-p",
		BlastRadius: protocol.BlastRadiusIsolated, Phase: "queue", CurrentAction: "Task queued",
		Status: protocol.TaskStatusPreparing, Tags: []string{"t"}, EditSurfaces: []string{"f.go"},
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	mgr, err := task.NewTaskManager(t.TempDir(), 0, task.WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	mt, ok := mgr.GetTask("prep-1")
	if !ok {
		t.Fatal("the preparing task is missing after rehydration")
	}
	snap := mt.Snapshot()
	if snap.Status != protocol.TaskStatusFailed || snap.Error == nil || snap.Error.Code != task.OrphanedTaskCode {
		t.Errorf("a preparing task must be failed with the restart cause, got %s %+v", snap.Status, snap.Error)
	}
	if n := len(mgr.RunningTerritories()); n != 0 {
		t.Errorf("the territory must not be primed with the failed preparing task, got %d", n)
	}
}
