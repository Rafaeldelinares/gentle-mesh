package task

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

func newWorkerIDTask(t *testing.T) *ManagedTask {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewManagedTask("task-worker-id", protocol.TaskRequest{Agent: "worker", Task: "who"}, ctx, cancel, nil)
}

func TestManagedTaskAttributeNodeFillsWorkerID(t *testing.T) {
	mt := newWorkerIDTask(t)

	if got := mt.Snapshot().WorkerID; got != "" {
		t.Fatalf("expected an empty worker id before attribution, got %q", got)
	}

	mt.AttributeNode("node-alpha")
	if got := mt.Snapshot().WorkerID; got != "node-alpha" {
		t.Fatalf("worker id = %q, want node-alpha", got)
	}

	// First writer wins: a later node never rewrites who started the work.
	mt.AttributeNode("node-beta")
	if got := mt.Snapshot().WorkerID; got != "node-alpha" {
		t.Fatalf("worker id = %q, want node-alpha (first attribution)", got)
	}

	mt.AttributeNode("")
	if got := mt.Snapshot().WorkerID; got != "node-alpha" {
		t.Fatalf("an empty attribution changed the worker id: %q", got)
	}
}

func TestManagedTaskSnapshotOmitsWorkerIDWhenNotAttributed(t *testing.T) {
	raw, err := json.Marshal(newWorkerIDTask(t).Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(raw), "worker_id") {
		t.Fatalf("a task that was never attributed must not carry worker_id: %s", raw)
	}
}

func TestManagedTaskKeepsTheFirstCause(t *testing.T) {
	mt := newWorkerIDTask(t)

	if _, err := mt.EmitEvent(protocol.EventError, protocol.ErrorPayload{Code: "TASK_TIMEOUT", Message: "task timeout"}); err != nil {
		t.Fatalf("first failing event rejected: %v", err)
	}
	// A later cause (for example the node reporting its own failure) must not replace it.
	_, _ = mt.EmitEvent(protocol.EventError, protocol.ErrorPayload{Code: "NODE_FAILED", Message: "node said no"})

	snapshot := mt.Snapshot()
	if snapshot.Error == nil || snapshot.Error.Code != "TASK_TIMEOUT" {
		t.Fatalf("the first cause must win, got %+v", snapshot.Error)
	}
}

// TestWorkerIDSurvivesRehydration pins what the API cannot answer by itself: after the coordinator
// restarts and rebuilds its tasks from the store, a task that was attributed to a node still
// reports that node. It uses a real SQLite file, so the restarted manager reads the same database.
func TestWorkerIDSurvivesRehydration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")

	first, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr1, err := NewTaskManager(dir, time.Hour, WithStore(first))
	if err != nil {
		t.Fatalf("first manager: %v", err)
	}
	mt, err := mgr1.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "who survives a restart"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	mt.AttributeNode("node-alpha")

	// Move the task to a terminal state so the restarted manager keeps the row as it is.
	if _, err := mt.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning}); err != nil {
		t.Fatalf("running event: %v", err)
	}
	if _, err := mt.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"}); err != nil {
		t.Fatalf("completion event: %v", err)
	}

	stored, err := first.GetTask(context.Background(), mt.TaskID)
	if err != nil || stored == nil {
		t.Fatalf("stored task: %v", err)
	}
	if stored.WorkerID != "node-alpha" {
		t.Fatalf("stored worker_id = %q, want node-alpha", stored.WorkerID)
	}

	// The coordinator restarts: a fresh manager over the same database file.
	second, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	mgr2, err := NewTaskManager(dir, time.Hour, WithStore(second))
	if err != nil {
		t.Fatalf("second manager: %v", err)
	}
	rehydrated, ok := mgr2.GetTask(mt.TaskID)
	if !ok {
		t.Fatalf("the rehydrated task is missing")
	}
	if got := rehydrated.Snapshot().WorkerID; got != "node-alpha" {
		t.Fatalf("rehydrated worker_id = %q, want node-alpha", got)
	}
}
