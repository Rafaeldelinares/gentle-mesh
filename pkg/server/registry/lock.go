package registry

import (
	"errors"
	"sync"
	"time"
)

var (
	// ErrBranchLocked is returned when a branch is already locked by another task.
	ErrBranchLocked = errors.New("branch is locked by another task")
	// ErrLockNotOwned is returned when an attempt is made to release a lock owned by another task.
	ErrLockNotOwned = errors.New("branch lock is not owned by this task")
)

type branchKey struct {
	repo   string
	branch string
}

type branchLock struct {
	taskID     string
	acquiredAt time.Time
}

// BranchLockManager manages exclusive branch locks for repositories in the mesh.
type BranchLockManager struct {
	mu    sync.RWMutex
	locks map[branchKey]branchLock
}

// NewBranchLockManager creates a new BranchLockManager.
func NewBranchLockManager() *BranchLockManager {
	return &BranchLockManager{
		locks: make(map[branchKey]branchLock),
	}
}

// ClaimLock attempts to acquire an exclusive lock for the given repo and branch.
// If repo or branch is empty, it acts as a no-op and returns nil.
// If already locked by the same taskID, it refreshes the lock and returns nil.
// If locked by a different taskID, it returns ErrBranchLocked.
func (m *BranchLockManager) ClaimLock(repo, branch, taskID string) error {
	if repo == "" || branch == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := branchKey{repo: repo, branch: branch}
	if existing, exists := m.locks[key]; exists {
		if existing.taskID != taskID {
			return ErrBranchLocked
		}
		m.locks[key] = branchLock{
			taskID:     taskID,
			acquiredAt: time.Now(),
		}
		return nil
	}

	m.locks[key] = branchLock{
		taskID:     taskID,
		acquiredAt: time.Now(),
	}
	return nil
}

// ReleaseLock releases the exclusive lock for the given repo and branch if held by taskID.
// If repo or branch is empty, it returns nil.
// If not locked, it returns nil.
// If locked by another task, it returns ErrLockNotOwned.
func (m *BranchLockManager) ReleaseLock(repo, branch, taskID string) error {
	if repo == "" || branch == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := branchKey{repo: repo, branch: branch}
	existing, exists := m.locks[key]
	if !exists {
		return nil
	}

	if existing.taskID != taskID {
		return ErrLockNotOwned
	}

	delete(m.locks, key)
	return nil
}

// GetLock returns the current lock holder, acquisition time, and whether the branch is locked.
// If repo or branch is empty, it returns false.
func (m *BranchLockManager) GetLock(repo, branch string) (taskID string, acquiredAt time.Time, locked bool) {
	if repo == "" || branch == "" {
		return "", time.Time{}, false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	key := branchKey{repo: repo, branch: branch}
	lock, exists := m.locks[key]
	if !exists {
		return "", time.Time{}, false
	}

	return lock.taskID, lock.acquiredAt, true
}
