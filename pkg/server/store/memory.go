package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var _ TaskStore = (*MemoryStore)(nil)

// MemoryStore is an in-memory, thread-safe implementation of TaskStore.
type MemoryStore struct {
	mu     sync.RWMutex
	tasks  map[string]*TaskRecord
	closed bool
}

// NewMemoryStore initializes a new in-memory TaskStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks: make(map[string]*TaskRecord),
	}
}

// SaveTask persists or updates a TaskRecord in memory.
func (s *MemoryStore) SaveTask(ctx context.Context, task *TaskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task == nil {
		return errors.New("task cannot be nil")
	}
	if task.TaskID == "" {
		return errors.New("task id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("store is closed")
	}

	s.tasks[task.TaskID] = task.Clone()
	return nil
}

// GetTask retrieves a TaskRecord by its ID.
func (s *MemoryStore) GetTask(ctx context.Context, taskID string) (*TaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, ErrTaskNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, errors.New("store is closed")
	}

	task, exists := s.tasks[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}

	return task.Clone(), nil
}

// UpdateStatus updates the status, completion time, error, and result of a task.
func (s *MemoryStore) UpdateStatus(ctx context.Context, taskID string, status protocol.TaskStatus, completedAt int64, errMsg string, resultSummary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if taskID == "" {
		return ErrTaskNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("store is closed")
	}

	task, exists := s.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	task.Status = status
	if completedAt != 0 {
		task.FinishedAt = completedAt
	}
	task.ErrorMessage = errMsg
	task.ResultSummary = resultSummary

	return nil
}

// ListTasks returns all tasks satisfying the filter, sorted by CreatedAt DESC, with pagination.
func (s *MemoryStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*TaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, errors.New("store is closed")
	}

	var matching []*TaskRecord
	for _, task := range s.tasks {
		if task.Matches(filter) {
			matching = append(matching, task.Clone())
		}
	}

	// Sort by CreatedAt DESC, then TaskID DESC for deterministic ordering
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].CreatedAt != matching[j].CreatedAt {
			return matching[i].CreatedAt > matching[j].CreatedAt
		}
		return matching[i].TaskID > matching[j].TaskID
	})

	// Apply Offset
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(matching) {
		return []*TaskRecord{}, nil
	}
	matching = matching[offset:]

	// Apply Limit
	if filter.Limit > 0 && len(matching) > filter.Limit {
		matching = matching[:filter.Limit]
	}

	if matching == nil {
		matching = []*TaskRecord{}
	}

	return matching, nil
}

// Close marks the store as closed and releases resources.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	s.tasks = make(map[string]*TaskRecord)
	return nil
}
