package registry_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
)

func TestBranchLockManager_BasicClaimAndRelease(t *testing.T) {
	mgr := registry.NewBranchLockManager()

	repo := "github.com/example/repo"
	branch := "feature/login"
	taskID := "task-1"

	// Initial state: not locked
	_, _, locked := mgr.GetLock(repo, branch)
	if locked {
		t.Fatal("expected branch to not be locked initially")
	}

	// Claim lock
	if err := mgr.ClaimLock(repo, branch, taskID); err != nil {
		t.Fatalf("unexpected error claiming lock: %v", err)
	}

	// Verify locked
	gotTaskID, acquiredAt, locked := mgr.GetLock(repo, branch)
	if !locked {
		t.Fatal("expected branch to be locked")
	}
	if gotTaskID != taskID {
		t.Errorf("expected taskID %q, got %q", taskID, gotTaskID)
	}
	if acquiredAt.IsZero() {
		t.Error("expected non-zero acquiredAt time")
	}

	// Re-claim by same taskID should succeed
	if err := mgr.ClaimLock(repo, branch, taskID); err != nil {
		t.Fatalf("re-claiming by same taskID should succeed, got: %v", err)
	}

	// Claim by different taskID should fail with ErrBranchLocked
	otherTask := "task-2"
	err := mgr.ClaimLock(repo, branch, otherTask)
	if !errors.Is(err, registry.ErrBranchLocked) {
		t.Fatalf("expected ErrBranchLocked, got: %v", err)
	}

	// Release by wrong taskID should return ErrLockNotOwned
	if err := mgr.ReleaseLock(repo, branch, otherTask); !errors.Is(err, registry.ErrLockNotOwned) {
		t.Fatalf("expected ErrLockNotOwned when releasing by wrong taskID, got: %v", err)
	}

	// Branch should still be locked by taskID
	gotTaskID, _, locked = mgr.GetLock(repo, branch)
	if !locked || gotTaskID != taskID {
		t.Fatalf("expected lock to still be held by %q, got %q (locked=%v)", taskID, gotTaskID, locked)
	}

	// Release by correct taskID
	if err := mgr.ReleaseLock(repo, branch, taskID); err != nil {
		t.Fatalf("unexpected error releasing lock: %v", err)
	}

	// Verify unlocked
	_, _, locked = mgr.GetLock(repo, branch)
	if locked {
		t.Fatal("expected branch to be unlocked after release")
	}

	// Re-release should succeed as a no-op
	if err := mgr.ReleaseLock(repo, branch, taskID); err != nil {
		t.Fatalf("re-releasing should return nil, got: %v", err)
	}

	// Now otherTask can claim the lock
	if err := mgr.ClaimLock(repo, branch, otherTask); err != nil {
		t.Fatalf("expected otherTask to successfully claim released lock, got: %v", err)
	}
}

func TestBranchLockManager_EmptyRepoOrBranch(t *testing.T) {
	mgr := registry.NewBranchLockManager()

	// Empty repo or branch should be a no-op
	if err := mgr.ClaimLock("", "main", "task-1"); err != nil {
		t.Errorf("expected nil on empty repo, got: %v", err)
	}
	if err := mgr.ClaimLock("repo", "", "task-1"); err != nil {
		t.Errorf("expected nil on empty branch, got: %v", err)
	}
	if err := mgr.ClaimLock("", "", "task-1"); err != nil {
		t.Errorf("expected nil on empty repo and branch, got: %v", err)
	}

	// GetLock should return false
	if _, _, locked := mgr.GetLock("", "main"); locked {
		t.Error("expected false for empty repo")
	}
	if _, _, locked := mgr.GetLock("repo", ""); locked {
		t.Error("expected false for empty branch")
	}

	// ReleaseLock should be a no-op
	if err := mgr.ReleaseLock("", "main", "task-1"); err != nil {
		t.Errorf("expected nil on empty repo, got: %v", err)
	}
	if err := mgr.ReleaseLock("repo", "", "task-1"); err != nil {
		t.Errorf("expected nil on empty branch, got: %v", err)
	}
}

func TestBranchLockManager_IsolationBetweenBranchesAndRepos(t *testing.T) {
	mgr := registry.NewBranchLockManager()

	repoA := "github.com/example/repoA"
	repoB := "github.com/example/repoB"
	branchMain := "main"
	branchFeature := "feature"

	// Claim repoA:branchMain
	if err := mgr.ClaimLock(repoA, branchMain, "task-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// repoA:branchFeature should be free
	if err := mgr.ClaimLock(repoA, branchFeature, "task-2"); err != nil {
		t.Fatalf("expected different branch in same repo to succeed: %v", err)
	}

	// repoB:branchMain should be free
	if err := mgr.ClaimLock(repoB, branchMain, "task-3"); err != nil {
		t.Fatalf("expected same branch in different repo to succeed: %v", err)
	}

	// Verify all three are held by their respective tasks
	if id, _, locked := mgr.GetLock(repoA, branchMain); !locked || id != "task-1" {
		t.Errorf("unexpected state for repoA:branchMain: id=%s, locked=%v", id, locked)
	}
	if id, _, locked := mgr.GetLock(repoA, branchFeature); !locked || id != "task-2" {
		t.Errorf("unexpected state for repoA:branchFeature: id=%s, locked=%v", id, locked)
	}
	if id, _, locked := mgr.GetLock(repoB, branchMain); !locked || id != "task-3" {
		t.Errorf("unexpected state for repoB:branchMain: id=%s, locked=%v", id, locked)
	}
}

func TestBranchLockManager_ConcurrentClaims(t *testing.T) {
	mgr := registry.NewBranchLockManager()
	repo := "github.com/example/repo"
	branch := "main"

	const numGoroutines = 50
	var successCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			taskID := fmt.Sprintf("task-%d", id)
			err := mgr.ClaimLock(repo, branch, taskID)
			if err == nil {
				successCount.Add(1)
				// Hold briefly then release
				time.Sleep(10 * time.Millisecond)
				_ = mgr.ReleaseLock(repo, branch, taskID)
			}
		}(i)
	}

	wg.Wait()

	// At least 1 should have succeeded, and the final state should be unlocked
	if successCount.Load() == 0 {
		t.Fatal("expected at least one goroutine to acquire the lock")
	}

	_, _, locked := mgr.GetLock(repo, branch)
	if locked {
		t.Error("expected lock to be released after all goroutines finish")
	}
}

func TestBranchLockManager_ConcurrentMultipleBranches(t *testing.T) {
	mgr := registry.NewBranchLockManager()
	const numBranches = 10
	const numTasksPerBranch = 10
	var wg sync.WaitGroup

	for b := 0; b < numBranches; b++ {
		branch := fmt.Sprintf("branch-%d", b)
		for task := 0; task < numTasksPerBranch; task++ {
			wg.Add(1)
			go func(br string, tID int) {
				defer wg.Done()
				taskID := fmt.Sprintf("task-%d", tID)
				_ = mgr.ClaimLock("repo", br, taskID)
				_, _, _ = mgr.GetLock("repo", br)
				_ = mgr.ReleaseLock("repo", br, taskID)
			}(branch, task)
		}
	}

	wg.Wait()
}
