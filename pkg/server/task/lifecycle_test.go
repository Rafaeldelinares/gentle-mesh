package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/store"
)

func recordFor(id string, status protocol.TaskStatus) *store.TaskRecord {
	return &store.TaskRecord{
		TaskID:        id,
		Branch:        "b",
		Repository:    "r",
		Domain:        "d",
		BlastRadius:   protocol.BlastRadiusIsolated,
		Phase:         "executing",
		CurrentAction: "running",
		Status:        status,
		Tags:          []string{"t"},
		EditSurfaces:  []string{"f.go"},
		CreatedAt:     time.Now().Unix(),
	}
}

func storedRecord(t *testing.T, mem *store.MemoryStore, id string) *store.TaskRecord {
	t.Helper()
	recs, err := mem.ListTasks(context.Background(), store.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	for _, r := range recs {
		if r != nil && r.TaskID == id {
			return r
		}
	}
	return nil
}

// A task that was running when the coordinator stopped cannot be resumed: it must be failed
// with the explicit cause, recorded in the state, the log and the store.
func TestRehydrateRunningTaskIsFailedWithCause(t *testing.T) {
	dir := t.TempDir()
	mem := store.NewMemoryStore()
	id := "task-orphan"
	if err := mem.SaveTask(context.Background(), recordFor(id, protocol.TaskStatusRunning)); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	// Give the task an existing JSONL log so the rehydrated task keeps a logger.
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to seed the log: %v", err)
	}

	mgr, err := NewTaskManager(dir, 0, WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	mt, ok := mgr.GetTask(id)
	if !ok {
		t.Fatal("the rehydrated task is missing")
	}
	snap := mt.Snapshot()
	if snap.Status != protocol.TaskStatusFailed {
		t.Errorf("expected failed, got %s", snap.Status)
	}
	if snap.Error == nil || snap.Error.Code != OrphanedTaskCode || !strings.Contains(snap.Error.Message, "coordinator restarted") {
		t.Errorf("expected the restart cause, got %+v", snap.Error)
	}
	if rec := storedRecord(t, mem, id); rec == nil || rec.Status != protocol.TaskStatusFailed || !strings.Contains(rec.ErrorMessage, "coordinator restarted") {
		t.Errorf("the store must keep status+message, got %+v", rec)
	}
	logBytes, _ := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if !strings.Contains(string(logBytes), `"type":"error"`) || !strings.Contains(string(logBytes), "coordinator restarted") {
		t.Errorf("the task log must contain an error event with the cause, got %s", string(logBytes))
	}
}

// A queued task of a previous run cannot be replayed faithfully (the stored row keeps neither
// the task text nor the priority), so it is failed with the restart cause too.
func TestRehydrateQueuedTaskIsFailedWithCause(t *testing.T) {
	dir := t.TempDir()
	mem := store.NewMemoryStore()
	id := "task-queued"
	if err := mem.SaveTask(context.Background(), recordFor(id, protocol.TaskStatusQueued)); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to seed the log: %v", err)
	}

	mgr, err := NewTaskManager(dir, 0, WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	mt, ok := mgr.GetTask(id)
	if !ok {
		t.Fatal("the rehydrated task is missing")
	}
	snap := mt.Snapshot()
	if snap.Status != protocol.TaskStatusFailed {
		t.Errorf("expected failed, got %s", snap.Status)
	}
	if snap.Error == nil || snap.Error.Code != OrphanedTaskCode || !strings.Contains(snap.Error.Message, "coordinator restarted") {
		t.Errorf("expected the restart cause, got %+v", snap.Error)
	}
	if rec := storedRecord(t, mem, id); rec == nil || rec.Status != protocol.TaskStatusFailed || !strings.Contains(rec.ErrorMessage, "coordinator restarted") {
		t.Errorf("the store must keep status+message, got %+v", rec)
	}
	logBytes, _ := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if !strings.Contains(string(logBytes), `"type":"error"`) {
		t.Errorf("the task log must contain an error event, got %s", string(logBytes))
	}
}

// Two old queued tasks that conflict with each other both fail at startup, and neither of them
// keeps the territory: nothing is left primed for the next task.
func TestRehydrateConflictingQueuedTasksAreAllFailedAndTerritoryFree(t *testing.T) {
	mem := store.NewMemoryStore()
	for _, id := range []string{"q-a", "q-b"} {
		rec := recordFor(id, protocol.TaskStatusQueued)
		rec.Repository = "repo-c"
		rec.Branch = "branch-c"
		rec.Domain = "dom-c"
		rec.EditSurfaces = []string{"same.go"}
		if err := mem.SaveTask(context.Background(), rec); err != nil {
			t.Fatalf("SaveTask failed: %v", err)
		}
	}

	mgr, err := NewTaskManager(t.TempDir(), 0, WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	for _, id := range []string{"q-a", "q-b"} {
		mt, ok := mgr.GetTask(id)
		if !ok {
			t.Fatalf("%s is missing after rehydration", id)
		}
		snap := mt.Snapshot()
		if snap.Status != protocol.TaskStatusFailed || snap.Error == nil || snap.Error.Code != OrphanedTaskCode {
			t.Errorf("%s must be failed with the restart cause, got %s %+v", id, snap.Status, snap.Error)
		}
		if rec := storedRecord(t, mem, id); rec == nil || rec.Status != protocol.TaskStatusFailed {
			t.Errorf("%s: the store must keep the failed status, got %+v", id, rec)
		}
	}
	if n := len(mgr.RunningTerritories()); n != 0 {
		t.Errorf("the territory must be free, got %d primed territories", n)
	}
	if n := len(mgr.ActiveTerritories()); n != 0 {
		t.Errorf("no active territory must be left, got %d", n)
	}
}

// Terminal rows of an existing database are preserved as they are (no loss, no rewriting).
func TestRehydrateKeepsTerminalRows(t *testing.T) {
	mem := store.NewMemoryStore()
	done := recordFor("task-done", protocol.TaskStatusCompleted)
	done.ResultSummary = "ok"
	if err := mem.SaveTask(context.Background(), done); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	mgr, err := NewTaskManager(t.TempDir(), 0, WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	mt, ok := mgr.GetTask("task-done")
	if !ok {
		t.Fatal("the completed task is missing")
	}
	if got := mt.Snapshot().Status; got != protocol.TaskStatusCompleted {
		t.Errorf("a completed task must stay completed, got %s", got)
	}
	if rec := storedRecord(t, mem, "task-done"); rec == nil || rec.ResultSummary != "ok" {
		t.Errorf("the stored row must be preserved, got %+v", rec)
	}
}

// FailTask records the cause in state, log and store, and is a no-op on a terminal task.
// The task is set up through the rehydration path, which is how production reaches it
// (a persisted running task on restart) and also through a dispatched task.
func TestFailTaskRecordsCauseAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mem := store.NewMemoryStore()
	id := "task-dispatched"
	if err := mem.SaveTask(context.Background(), recordFor(id, protocol.TaskStatusRunning)); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to seed the log: %v", err)
	}
	mgr, err := NewTaskManager(dir, 0, WithStore(mem))
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	defer mgr.Close()

	// A second task goes through the normal creation path and the scheduler-like transition.
	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "t"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if err := mt.SetStatus(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("SetStatus(running) failed: %v", err)
	}
	if err := mgr.FailTask(mt.TaskID, "DISPATCH_FAILED", "dispatch failed: boom"); err != nil {
		t.Fatalf("FailTask failed: %v", err)
	}
	snap := mt.Snapshot()
	if snap.Status != protocol.TaskStatusFailed || snap.Error == nil || snap.Error.Code != "DISPATCH_FAILED" {
		t.Fatalf("expected failed with the dispatch cause, got %s %+v", snap.Status, snap.Error)
	}
	if rec := storedRecord(t, mem, mt.TaskID); rec == nil || rec.ErrorMessage != "dispatch failed: boom" {
		t.Errorf("the store must keep the message, got %+v", rec)
	}
	logBytes, _ := os.ReadFile(filepath.Join(dir, mt.TaskID+".jsonl"))
	if !strings.Contains(string(logBytes), `"type":"error"`) {
		t.Errorf("the log must contain an error event, got %s", string(logBytes))
	}
	if err := mgr.FailTask(mt.TaskID, "OTHER", "other"); err == nil {
		t.Error("failing a terminal task must report an error")
	}
	if got := mt.Snapshot().Error.Code; got != "DISPATCH_FAILED" {
		t.Errorf("the original cause must be preserved, got %s", got)
	}
}
