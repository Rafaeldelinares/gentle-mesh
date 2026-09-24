package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

func newMemoryStoreForTest(t *testing.T) store.TaskStore {
	t.Helper()
	s := store.NewMemoryStore()
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func newSQLiteStoreForTest(t *testing.T) store.TaskStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func forEachStore(t *testing.T, fn func(t *testing.T, s store.TaskStore)) {
	t.Helper()
	t.Run("MemoryStore", func(t *testing.T) {
		s := newMemoryStoreForTest(t)
		fn(t, s)
	})
	t.Run("SQLiteStore", func(t *testing.T) {
		s := newSQLiteStoreForTest(t)
		fn(t, s)
	})
}

func TestSaveAndGet(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		original := &store.TaskRecord{
			TaskID:        "task-uuid-001",
			Branch:        "feature/distributed-mesh",
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Domain:        "core",
			BlastRadius:   protocol.BlastRadiusSharedSchema,
			Phase:         protocol.AgentPhasePlan,
			CurrentAction: "analyzing territory conflicts",
			Status:        protocol.TaskStatusRunning,
			WorkerID:      "node-arm64-01",
			Tags:          []string{"go", "sqlite", "mesh"},
			EditSurfaces:  []string{"pkg/server/store/store.go", "pkg/server/store/sqlite.go"},
			CreatedAt:     1710000000,
			StartedAt:     1710000010,
			FinishedAt:    1710000100,
			ErrorMessage:  "non-fatal warning encountered",
			ResultSummary: "plan validated with 0 conflicts",
		}

		err := s.SaveTask(ctx, original)
		if err != nil {
			t.Fatalf("SaveTask failed: %v", err)
		}

		got, err := s.GetTask(ctx, original.TaskID)
		if err != nil {
			t.Fatalf("GetTask failed: %v", err)
		}

		if !reflect.DeepEqual(got, original) {
			t.Fatalf("retrieved record does not match original.\nGot:  %+v\nWant: %+v", got, original)
		}
	})
}

func TestSaveAndGet_NilSlices(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		original := &store.TaskRecord{
			TaskID:     "task-uuid-nil-slices",
			Branch:     "main",
			Repository: "gentle-mesh",
			Status:     protocol.TaskStatusQueued,
			CreatedAt:  1710000000,
		}

		err := s.SaveTask(ctx, original)
		if err != nil {
			t.Fatalf("SaveTask failed: %v", err)
		}

		got, err := s.GetTask(ctx, original.TaskID)
		if err != nil {
			t.Fatalf("GetTask failed: %v", err)
		}

		if got.TaskID != original.TaskID || got.Status != original.Status {
			t.Fatalf("expected task id and status to match, got: %+v", got)
		}
	})
}

func TestGetNotFound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		_, err := s.GetTask(ctx, "non-existent-task-id")
		if !errors.Is(err, store.ErrTaskNotFound) {
			t.Fatalf("expected ErrTaskNotFound, got: %v", err)
		}

		_, err = s.GetTask(ctx, "")
		if !errors.Is(err, store.ErrTaskNotFound) {
			t.Fatalf("expected ErrTaskNotFound for empty task ID, got: %v", err)
		}

		err = s.UpdateStatus(ctx, "non-existent-task-id", protocol.TaskStatusCompleted, 100, "", "")
		if !errors.Is(err, store.ErrTaskNotFound) {
			t.Fatalf("expected ErrTaskNotFound on UpdateStatus, got: %v", err)
		}
	})
}

func TestUpdateStatus(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		task := &store.TaskRecord{
			TaskID:        "task-update-01",
			Branch:        "main",
			Repository:    "gentle-mesh",
			Domain:        "infra",
			BlastRadius:   protocol.BlastRadiusReadOnly,
			Phase:         protocol.AgentPhaseExplore,
			CurrentAction: "scouting",
			Status:        protocol.TaskStatusQueued,
			WorkerID:      "worker-1",
			CreatedAt:     1710000000,
			StartedAt:     0,
			FinishedAt:    0,
			ErrorMessage:  "",
			ResultSummary: "",
		}

		err := s.SaveTask(ctx, task)
		if err != nil {
			t.Fatalf("SaveTask failed: %v", err)
		}

		completedAt := int64(1710000500)
		errMsg := "compilation failed on subagent"
		resultSummary := "failed after 3 retries"

		err = s.UpdateStatus(ctx, task.TaskID, protocol.TaskStatusFailed, completedAt, errMsg, resultSummary)
		if err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		updated, err := s.GetTask(ctx, task.TaskID)
		if err != nil {
			t.Fatalf("GetTask failed: %v", err)
		}

		if updated.Status != protocol.TaskStatusFailed {
			t.Errorf("expected status %q, got %q", protocol.TaskStatusFailed, updated.Status)
		}
		if updated.FinishedAt != completedAt {
			t.Errorf("expected finished_at %d, got %d", completedAt, updated.FinishedAt)
		}
		if updated.ErrorMessage != errMsg {
			t.Errorf("expected error message %q, got %q", errMsg, updated.ErrorMessage)
		}
		if updated.ResultSummary != resultSummary {
			t.Errorf("expected result summary %q, got %q", resultSummary, updated.ResultSummary)
		}
		if updated.Branch != task.Branch || updated.Domain != task.Domain {
			t.Errorf("immutable task fields were corrupted during UpdateStatus")
		}
	})
}

func TestListWithFilters(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		tasks := []*store.TaskRecord{
			{
				TaskID:     "task-1",
				Branch:     "main",
				Repository: "repo-1",
				Domain:     "infra",
				Status:     protocol.TaskStatusQueued,
				WorkerID:   "w1",
				CreatedAt:  100,
			},
			{
				TaskID:     "task-2",
				Branch:     "feat-a",
				Repository: "repo-1",
				Domain:     "core",
				Status:     protocol.TaskStatusRunning,
				WorkerID:   "w2",
				CreatedAt:  200,
			},
			{
				TaskID:     "task-3",
				Branch:     "feat-a",
				Repository: "repo-2",
				Domain:     "core",
				Status:     protocol.TaskStatusCompleted,
				WorkerID:   "w1",
				CreatedAt:  300,
			},
			{
				TaskID:     "task-4",
				Branch:     "feat-b",
				Repository: "repo-2",
				Domain:     "core",
				Status:     protocol.TaskStatusCompleted,
				WorkerID:   "w2",
				CreatedAt:  400,
			},
			{
				TaskID:     "task-5",
				Branch:     "feat-c",
				Repository: "repo-1",
				Domain:     "ui",
				Status:     protocol.TaskStatusFailed,
				WorkerID:   "w1",
				CreatedAt:  500,
			},
			{
				TaskID:     "task-6",
				Branch:     "feat-b",
				Repository: "repo-2",
				Domain:     "core",
				Status:     protocol.TaskStatusCompleted,
				WorkerID:   "w3",
				CreatedAt:  600,
			},
		}

		for _, task := range tasks {
			if err := s.SaveTask(ctx, task); err != nil {
				t.Fatalf("failed to save task %s: %v", task.TaskID, err)
			}
		}

		t.Run("All sorted by created_at DESC", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 6 {
				t.Fatalf("expected 6 tasks, got %d", len(list))
			}
			expectedOrder := []string{"task-6", "task-5", "task-4", "task-3", "task-2", "task-1"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Filter by status", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{
				Status: protocol.TaskStatusCompleted,
			})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 3 {
				t.Fatalf("expected 3 completed tasks, got %d", len(list))
			}
			expectedOrder := []string{"task-6", "task-4", "task-3"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Filter by domain", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{
				Domain: "core",
			})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 4 {
				t.Fatalf("expected 4 core tasks, got %d", len(list))
			}
			expectedOrder := []string{"task-6", "task-4", "task-3", "task-2"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Filter by branch", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{
				Branch: "feat-b",
			})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("expected 2 tasks for feat-b, got %d", len(list))
			}
			expectedOrder := []string{"task-6", "task-4"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Filter by repository", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{
				Repository: "repo-2",
			})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 3 {
				t.Fatalf("expected 3 tasks for repo-2, got %d", len(list))
			}
			expectedOrder := []string{"task-6", "task-4", "task-3"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Filter by worker_id", func(t *testing.T) {
			list, err := s.ListTasks(ctx, store.TaskFilter{
				WorkerID: "w1",
			})
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			if len(list) != 3 {
				t.Fatalf("expected 3 tasks for w1, got %d", len(list))
			}
			expectedOrder := []string{"task-5", "task-3", "task-1"}
			for i, exp := range expectedOrder {
				if list[i].TaskID != exp {
					t.Errorf("at position %d: expected %s, got %s", i, exp, list[i].TaskID)
				}
			}
		})

		t.Run("Pagination with limit and offset", func(t *testing.T) {
			page1, err := s.ListTasks(ctx, store.TaskFilter{
				Limit:  2,
				Offset: 0,
			})
			if err != nil {
				t.Fatalf("page 1 failed: %v", err)
			}
			if len(page1) != 2 || page1[0].TaskID != "task-6" || page1[1].TaskID != "task-5" {
				t.Fatalf("unexpected page 1: %+v", page1)
			}

			page2, err := s.ListTasks(ctx, store.TaskFilter{
				Limit:  2,
				Offset: 2,
			})
			if err != nil {
				t.Fatalf("page 2 failed: %v", err)
			}
			if len(page2) != 2 || page2[0].TaskID != "task-4" || page2[1].TaskID != "task-3" {
				t.Fatalf("unexpected page 2: %+v", page2)
			}

			page3, err := s.ListTasks(ctx, store.TaskFilter{
				Limit:  2,
				Offset: 4,
			})
			if err != nil {
				t.Fatalf("page 3 failed: %v", err)
			}
			if len(page3) != 2 || page3[0].TaskID != "task-2" || page3[1].TaskID != "task-1" {
				t.Fatalf("unexpected page 3: %+v", page3)
			}

			pageEmpty, err := s.ListTasks(ctx, store.TaskFilter{
				Limit:  2,
				Offset: 10,
			})
			if err != nil {
				t.Fatalf("empty page failed: %v", err)
			}
			if len(pageEmpty) != 0 {
				t.Fatalf("expected empty page, got %d items", len(pageEmpty))
			}
		})
	})
}

func TestSQLiteCrashRecovery(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "crash_recovery.db")

	initialTask := &store.TaskRecord{
		TaskID:        "task-crash-001",
		Branch:        "feature/wal-mode",
		Repository:    "gentle-mesh",
		Domain:        "storage",
		BlastRadius:   protocol.BlastRadiusIsolated,
		Phase:         protocol.AgentPhaseApply,
		CurrentAction: "writing wal tests",
		Status:        protocol.TaskStatusRunning,
		WorkerID:      "worker-crash-node",
		Tags:          []string{"persistence", "recovery", "sqlite"},
		EditSurfaces:  []string{"pkg/server/store/sqlite.go"},
		CreatedAt:     1710001000,
		StartedAt:     1710001005,
		FinishedAt:    0,
		ErrorMessage:  "",
		ResultSummary: "",
	}

	// 1. Open store, write task, and close
	store1, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open store1: %v", err)
	}
	if err := store1.SaveTask(ctx, initialTask); err != nil {
		t.Fatalf("failed to save task in store1: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("failed to close store1: %v", err)
	}

	// 2. Re-open store from same file and verify task is recovered intact
	store2, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open store2 on existing file: %v", err)
	}
	defer store2.Close()

	recovered, err := store2.GetTask(ctx, initialTask.TaskID)
	if err != nil {
		t.Fatalf("failed to get task from store2: %v", err)
	}

	if !reflect.DeepEqual(recovered, initialTask) {
		t.Fatalf("recovered task mismatch.\nGot:  %+v\nWant: %+v", recovered, initialTask)
	}

	// 3. Update status in store2, close, and re-open to verify update persists
	err = store2.UpdateStatus(ctx, initialTask.TaskID, protocol.TaskStatusCompleted, 1710001500, "", "recovery verified")
	if err != nil {
		t.Fatalf("failed to update status in store2: %v", err)
	}
	if err := store2.Close(); err != nil {
		t.Fatalf("failed to close store2: %v", err)
	}

	store3, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open store3: %v", err)
	}
	defer store3.Close()

	recoveredAfterUpdate, err := store3.GetTask(ctx, initialTask.TaskID)
	if err != nil {
		t.Fatalf("failed to get task from store3: %v", err)
	}

	if recoveredAfterUpdate.Status != protocol.TaskStatusCompleted {
		t.Errorf("expected status %q, got %q", protocol.TaskStatusCompleted, recoveredAfterUpdate.Status)
	}
	if recoveredAfterUpdate.FinishedAt != 1710001500 {
		t.Errorf("expected finished_at 1710001500, got %d", recoveredAfterUpdate.FinishedAt)
	}
	if recoveredAfterUpdate.ResultSummary != "recovery verified" {
		t.Errorf("expected result summary %q, got %q", "recovery verified", recoveredAfterUpdate.ResultSummary)
	}
}

func TestConcurrentAccess(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()
		const goroutines = 30
		var wg sync.WaitGroup
		errs := make(chan error, goroutines*4)

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()

				taskID := fmt.Sprintf("task-concurrent-%03d", idx)
				task := &store.TaskRecord{
					TaskID:     taskID,
					Branch:     fmt.Sprintf("feat-%d", idx%3),
					Repository: "repo-mesh",
					Domain:     "core",
					Status:     protocol.TaskStatusQueued,
					WorkerID:   fmt.Sprintf("worker-%d", idx%5),
					Tags:       []string{fmt.Sprintf("tag-%d", idx)},
					CreatedAt:  time.Now().UnixNano(),
				}

				// 1. Save
				if err := s.SaveTask(ctx, task); err != nil {
					errs <- fmt.Errorf("goroutine %d SaveTask: %w", idx, err)
					return
				}

				// 2. Read
				got, err := s.GetTask(ctx, taskID)
				if err != nil {
					errs <- fmt.Errorf("goroutine %d GetTask: %w", idx, err)
					return
				}
				if got.TaskID != taskID {
					errs <- fmt.Errorf("goroutine %d mismatch task ID: got %s, want %s", idx, got.TaskID, taskID)
					return
				}

				// 3. Update
				if err := s.UpdateStatus(ctx, taskID, protocol.TaskStatusRunning, 0, "", "in progress"); err != nil {
					errs <- fmt.Errorf("goroutine %d UpdateStatus running: %w", idx, err)
					return
				}

				// 4. List
				list, err := s.ListTasks(ctx, store.TaskFilter{
					Domain: "core",
					Limit:  5,
				})
				if err != nil {
					errs <- fmt.Errorf("goroutine %d ListTasks: %w", idx, err)
					return
				}
				if len(list) == 0 {
					errs <- fmt.Errorf("goroutine %d ListTasks returned 0 items", idx)
					return
				}

				// 5. Complete
				if err := s.UpdateStatus(ctx, taskID, protocol.TaskStatusCompleted, time.Now().Unix(), "", "done"); err != nil {
					errs <- fmt.Errorf("goroutine %d UpdateStatus completed: %w", idx, err)
					return
				}
			}(i)
		}

		wg.Wait()
		close(errs)

		for err := range errs {
			t.Errorf("concurrent error: %v", err)
		}
	})
}

func TestSaveTask_Upsert(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		task := &store.TaskRecord{
			TaskID:        "task-upsert-1",
			Branch:        "main",
			Repository:    "repo-1",
			Status:        protocol.TaskStatusQueued,
			CreatedAt:     100,
			ResultSummary: "initial",
		}

		if err := s.SaveTask(ctx, task); err != nil {
			t.Fatalf("first SaveTask failed: %v", err)
		}

		taskUpdated := &store.TaskRecord{
			TaskID:        "task-upsert-1",
			Branch:        "feature/upsert",
			Repository:    "repo-1",
			Status:        protocol.TaskStatusRunning,
			CreatedAt:     100,
			ResultSummary: "updated",
		}

		if err := s.SaveTask(ctx, taskUpdated); err != nil {
			t.Fatalf("second SaveTask (upsert) failed: %v", err)
		}

		got, err := s.GetTask(ctx, "task-upsert-1")
		if err != nil {
			t.Fatalf("GetTask failed: %v", err)
		}

		if got.Branch != "feature/upsert" || got.Status != protocol.TaskStatusRunning || got.ResultSummary != "updated" {
			t.Fatalf("upsert did not update fields properly: %+v", got)
		}
	})
}

func TestInvalidInput(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		ctx := context.Background()

		// Nil task record
		err := s.SaveTask(ctx, nil)
		if err == nil {
			t.Fatalf("expected error saving nil task, got nil")
		}

		// Empty task ID
		err = s.SaveTask(ctx, &store.TaskRecord{})
		if err == nil {
			t.Fatalf("expected error saving task with empty ID, got nil")
		}
	})
}

func TestContextCancellation(t *testing.T) {
	forEachStore(t, func(t *testing.T, s store.TaskStore) {
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		task := &store.TaskRecord{
			TaskID:    "task-canceled-1",
			Status:    protocol.TaskStatusQueued,
			CreatedAt: 100,
		}

		if err := s.SaveTask(canceledCtx, task); !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled on SaveTask, got: %v", err)
		}

		if _, err := s.GetTask(canceledCtx, "task-canceled-1"); !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled on GetTask, got: %v", err)
		}

		if err := s.UpdateStatus(canceledCtx, "task-canceled-1", protocol.TaskStatusCompleted, 100, "", ""); !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled on UpdateStatus, got: %v", err)
		}

		if _, err := s.ListTasks(canceledCtx, store.TaskFilter{}); !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled on ListTasks, got: %v", err)
		}
	})
}

func TestStoreClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("MemoryStore", func(t *testing.T) {
		s := store.NewMemoryStore()
		_ = s.Close()

		if err := s.SaveTask(ctx, &store.TaskRecord{TaskID: "1"}); err == nil {
			t.Errorf("expected error on SaveTask after Close")
		}
		if _, err := s.GetTask(ctx, "1"); err == nil {
			t.Errorf("expected error on GetTask after Close")
		}
		if err := s.UpdateStatus(ctx, "1", protocol.TaskStatusCompleted, 0, "", ""); err == nil {
			t.Errorf("expected error on UpdateStatus after Close")
		}
		if _, err := s.ListTasks(ctx, store.TaskFilter{}); err == nil {
			t.Errorf("expected error on ListTasks after Close")
		}
	})

	t.Run("SQLiteStore", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "closed.db")
		s, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		_ = s.Close()

		if err := s.SaveTask(ctx, &store.TaskRecord{TaskID: "1"}); err == nil {
			t.Errorf("expected error on SaveTask after Close")
		}
		if _, err := s.GetTask(ctx, "1"); err == nil {
			t.Errorf("expected error on GetTask after Close")
		}
		if err := s.UpdateStatus(ctx, "1", protocol.TaskStatusCompleted, 0, "", ""); err == nil {
			t.Errorf("expected error on UpdateStatus after Close")
		}
		if _, err := s.ListTasks(ctx, store.TaskFilter{}); err == nil {
			t.Errorf("expected error on ListTasks after Close")
		}
	})
}

