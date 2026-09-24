package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

var (
	// ErrTaskNotFound is returned when the requested task does not exist in the manager.
	ErrTaskNotFound = errors.New("task not found")
)

// TaskManager coordinates task creation, execution contexts, log persistence,
// event streaming subscriptions, and lifecycle management across all subagent tasks.
type TaskManager struct {
	tasksDir    string
	taskTTL     time.Duration
	store       store.TaskStore
	mu          sync.RWMutex
	tasks       map[string]*ManagedTask
	closed      bool
	stopCleanup chan struct{}
	cleanupWg   sync.WaitGroup
	runnerWg    sync.WaitGroup
}

// TaskManagerOption configures a TaskManager.
type TaskManagerOption func(*TaskManager)

// WithStore sets the underlying TaskStore for the TaskManager.
func WithStore(s store.TaskStore) TaskManagerOption {
	return func(m *TaskManager) {
		m.store = s
	}
}

// Store returns the configured TaskStore, or nil if none was configured.
func (m *TaskManager) Store() store.TaskStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store
}

// NewTaskManager creates a new TaskManager storing JSONL logs in tasksDir.
func NewTaskManager(tasksDir string, taskTTL time.Duration, opts ...TaskManagerOption) (*TaskManager, error) {
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create tasks directory: %w", err)
	}

	m := &TaskManager{
		tasksDir:    tasksDir,
		taskTTL:     taskTTL,
		tasks:       make(map[string]*ManagedTask),
		stopCleanup: make(chan struct{}),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	if m.store != nil {
		records, err := m.store.ListTasks(context.Background(), store.TaskFilter{})
		if err != nil {
			return nil, fmt.Errorf("failed to rehydrate tasks from store: %w", err)
		}
		for _, rec := range records {
			if rec == nil || rec.TaskID == "" {
				continue
			}
			m.tasks[rec.TaskID] = m.rehydrateTask(rec)
		}
	}

	if taskTTL > 0 {
		interval := taskTTL / 2
		if interval < 50*time.Millisecond {
			interval = 50 * time.Millisecond
		} else if interval > 10*time.Minute {
			interval = 10 * time.Minute
		}

		m.cleanupWg.Add(1)
		go func() {
			defer m.cleanupWg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					m.CleanupExpired()
				case <-m.stopCleanup:
					return
				}
			}
		}()
	}

	return m, nil
}

// CreateTask instantiates and registers a new subagent task, allocating a unique ID,
// setting up timeout/cancellation contexts, opening its JSONL logger, and emitting the initial queued event.
func (m *TaskManager) CreateTask(req protocol.TaskRequest) (*ManagedTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, errors.New("task manager is closed")
	}

	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("failed to generate random task ID bytes: %w", err)
	}
	taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))

	var ctx context.Context
	var cancel context.CancelFunc
	if req.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(req.TimeoutSeconds)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}

	logPath := filepath.Join(m.tasksDir, taskID+".jsonl")
	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize task logger: %w", err)
	}

	task := NewManagedTask(taskID, req, ctx, cancel, logger)

	if m.store != nil {
		task.SetOnUpdate(m.syncTaskToStore)
		blastRadius := task.BlastRadius
		if blastRadius == "" {
			blastRadius = protocol.BlastRadiusIsolated
		}
		record := &store.TaskRecord{
			TaskID:        taskID,
			Branch:        req.GitBranch,
			Repository:    req.GitRepo,
			Domain:        req.Domain,
			BlastRadius:   blastRadius,
			Phase:         task.Phase,
			CurrentAction: "Task queued",
			Status:        protocol.TaskStatusQueued,
			WorkerID:      task.NodeID,
			Tags:          append([]string(nil), req.Tags...),
			EditSurfaces:  append([]string(nil), req.EditSurfaces...),
			CreatedAt:     task.CreatedAt,
			StartedAt:     0,
			FinishedAt:    0,
			ErrorMessage:  "",
			ResultSummary: "",
		}
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.store.SaveTask(saveCtx, record)
		saveCancel()
		if err != nil {
			cancel()
			_ = logger.Close()
			return nil, fmt.Errorf("failed to save task record to store: %w", err)
		}
	}

	// Emit initial status: queued
	if _, err := task.EmitEvent(protocol.EventStatus, protocol.StatusPayload{
		Status: protocol.TaskStatusQueued,
	}); err != nil {
		cancel()
		_ = logger.Close()
		return nil, fmt.Errorf("failed to emit initial task status: %w", err)
	}

	m.tasks[taskID] = task
	return task, nil
}

// GetTask returns the ManagedTask for taskID if present.
func (m *TaskManager) GetTask(taskID string) (*ManagedTask, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	return task, exists
}

// ListTasks returns point-in-time state snapshots for all registered tasks.
func (m *TaskManager) ListTasks() []*protocol.TaskState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*protocol.TaskState, 0, len(m.tasks))
	for _, t := range m.tasks {
		st := t.Snapshot()
		result = append(result, &st)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt == result[j].CreatedAt {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})

	return result
}

// ActiveTerritories returns ActiveTerritory snapshots for all tasks currently in
// Queued, Preparing, or Running states.
func (m *TaskManager) ActiveTerritories() []protocol.ActiveTerritory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]protocol.ActiveTerritory, 0)
	for _, t := range m.tasks {
		t.mu.RLock()
		status := t.Status
		t.mu.RUnlock()

		if status == protocol.TaskStatusRunning ||
			status == protocol.TaskStatusPreparing ||
			status == protocol.TaskStatusQueued {
			result = append(result, t.Territory())
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].StartedAt == result[j].StartedAt {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].StartedAt < result[j].StartedAt
	})

	return result
}

// RunningTerritories returns ActiveTerritory snapshots for tasks currently in
// Preparing or Running states only, excluding Queued and terminal tasks.
func (m *TaskManager) RunningTerritories() []protocol.ActiveTerritory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]protocol.ActiveTerritory, 0)
	for _, t := range m.tasks {
		t.mu.RLock()
		status := t.Status
		t.mu.RUnlock()

		if status == protocol.TaskStatusRunning ||
			status == protocol.TaskStatusPreparing {
			result = append(result, t.Territory())
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].StartedAt == result[j].StartedAt {
			return result[i].TaskID < result[j].TaskID
		}
		return result[i].StartedAt < result[j].StartedAt
	})

	return result
}

// CancelTask cancels an active task with the given reason, transitions state to canceled,
// and notifies live event subscribers.
func (m *TaskManager) CancelTask(taskID string, reason string) error {
	m.mu.RLock()
	task, exists := m.tasks[taskID]
	m.mu.RUnlock()

	if !exists {
		return ErrTaskNotFound
	}

	task.mu.Lock()
	if task.isTerminalLocked() {
		task.mu.Unlock()
		return ErrTaskAlreadyFinished
	}

	task.cancel()
	task.Status = protocol.TaskStatusCanceled
	now := time.Now()
	if task.FinishedAt == 0 {
		task.FinishedAt = now.Unix()
		task.finishedTime = now
	}

	id := task.eventSeq.Add(1)
	evt, err := protocol.NewEvent(id, task.TaskID, protocol.EventStatus, protocol.StatusPayload{
		Status:  protocol.TaskStatusCanceled,
		Message: reason,
	})
	if err != nil {
		task.mu.Unlock()
		return err
	}

	if task.logger != nil {
		_ = task.logger.WriteEvent(*evt)
	}

	for sub := range task.subscribers {
		select {
		case sub <- *evt:
		default:
		}
	}

	for sub := range task.subscribers {
		close(sub)
	}
	task.subscribers = make(map[chan protocol.Event]struct{})
	task.mu.Unlock()

	task.notifyUpdate()
	return nil
}

// SubmitReply resolves a pending interactive query on a task and acknowledges the response.
func (m *TaskManager) SubmitReply(taskID string, reply protocol.TaskReplyRequest) error {
	task, exists := m.GetTask(taskID)
	if !exists {
		return ErrTaskNotFound
	}

	if err := task.ResolveQuery(reply.QueryID, reply.Answer); err != nil {
		return err
	}

	_, _ = task.EmitEvent(protocol.EventToolResult, protocol.ToolResultPayload{
		CallID: reply.QueryID,
		Output: reply.Answer,
	})

	return nil
}

// Subscribe returns an event channel replaying history since sinceID and streaming live events.
func (m *TaskManager) Subscribe(taskID string, sinceID int64) (<-chan protocol.Event, func(), error) {
	task, exists := m.GetTask(taskID)
	if !exists {
		return nil, nil, ErrTaskNotFound
	}
	return task.Subscribe(sinceID)
}

// CleanupExpired purges terminal tasks older than taskTTL and closes their loggers.
func (m *TaskManager) CleanupExpired() {
	if m.taskTTL <= 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}

	now := time.Now()
	for id, task := range m.tasks {
		task.mu.RLock()
		finishedAt := task.FinishedAt
		fTime := task.finishedTime
		logger := task.logger
		task.mu.RUnlock()

		if finishedAt > 0 {
			var elapsed time.Duration
			if !fTime.IsZero() {
				elapsed = now.Sub(fTime)
			} else {
				elapsed = now.Sub(time.Unix(finishedAt, 0))
			}

			if elapsed > m.taskTTL {
				if logger != nil {
					_ = logger.Close()
				}
				delete(m.tasks, id)
				logFile := filepath.Join(m.tasksDir, id+".jsonl")
				_ = os.Remove(logFile)
			}
		}
	}
}

// TrackRunner increments the active runner WaitGroup and returns a release func to be deferred by the runner goroutine.
func (m *TaskManager) TrackRunner() func() {
	m.runnerWg.Add(1)
	return func() {
		m.runnerWg.Done()
	}
}

// Close cancels all running tasks and closes all underlying event loggers.
func (m *TaskManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true

	for _, task := range m.tasks {
		task.cancel()
	}
	m.mu.Unlock()

	m.runnerWg.Wait()

	m.mu.Lock()
	var firstErr error
	for _, task := range m.tasks {
		task.mu.Lock()
		if task.logger != nil {
			if err := task.logger.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		for sub := range task.subscribers {
			close(sub)
		}
		task.subscribers = make(map[chan protocol.Event]struct{})
		task.mu.Unlock()
	}

	if m.stopCleanup != nil {
		close(m.stopCleanup)
	}

	if m.store != nil {
		if err := m.store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.mu.Unlock()

	m.cleanupWg.Wait()

	return firstErr
}

func (m *TaskManager) rehydrateTask(rec *store.TaskRecord) *ManagedTask {
	blastRadius := rec.BlastRadius
	if blastRadius == "" {
		blastRadius = protocol.BlastRadiusIsolated
	}
	req := protocol.TaskRequest{
		GitBranch:    rec.Branch,
		GitRepo:      rec.Repository,
		Domain:       rec.Domain,
		BlastRadius:  blastRadius,
		EditSurfaces: append([]string(nil), rec.EditSurfaces...),
		Tags:         append([]string(nil), rec.Tags...),
	}

	ctx, cancel := context.WithCancel(context.Background())

	logPath := filepath.Join(m.tasksDir, rec.TaskID+".jsonl")
	var logger *JSONLLogger
	if _, err := os.Stat(logPath); err == nil {
		logger, _ = NewJSONLLogger(logPath)
	}

	lastActivity := rec.FinishedAt
	if lastActivity == 0 {
		lastActivity = rec.StartedAt
	}
	if lastActivity == 0 {
		lastActivity = rec.CreatedAt
	}

	task := &ManagedTask{
		TaskID:         rec.TaskID,
		Request:        req,
		Status:         rec.Status,
		CreatedAt:      rec.CreatedAt,
		StartedAt:      rec.StartedAt,
		FinishedAt:     rec.FinishedAt,
		Domain:         rec.Domain,
		BlastRadius:    blastRadius,
		Phase:          rec.Phase,
		CurrentAction:  rec.CurrentAction,
		LastActivityAt: lastActivity,
		EditSurfaces:   append([]string(nil), rec.EditSurfaces...),
		NodeID:         rec.WorkerID,
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		subscribers:    make(map[chan protocol.Event]struct{}),
		pendingQueries: make(map[string]chan string),
	}

	if rec.FinishedAt > 0 {
		task.finishedTime = time.Unix(rec.FinishedAt, 0)
	}

	if rec.ResultSummary != "" || rec.Status == protocol.TaskStatusCompleted {
		task.Completion = &protocol.CompletionPayload{
			Result: rec.ResultSummary,
		}
	}

	if rec.ErrorMessage != "" || rec.Status == protocol.TaskStatusFailed {
		task.Error = &protocol.ErrorPayload{
			Message: rec.ErrorMessage,
		}
	}

	if task.isTerminalLocked() {
		cancel()
	}

	if logger != nil {
		events, err := logger.ReadEvents(0)
		if err == nil && len(events) > 0 {
			task.eventSeq.Store(events[len(events)-1].ID)
		}
	}

	task.SetOnUpdate(m.syncTaskToStore)
	return task
}

func (m *TaskManager) syncTaskToStore(t *ManagedTask) {
	if t == nil {
		return
	}
	m.mu.RLock()
	st := m.store
	closed := m.closed
	m.mu.RUnlock()
	if st == nil || closed {
		return
	}

	t.mu.RLock()
	var errMsg string
	if t.Error != nil {
		errMsg = t.Error.Message
	}
	var summary string
	if t.Completion != nil {
		summary = t.Completion.Result
	}

	record := &store.TaskRecord{
		TaskID:        t.TaskID,
		Branch:        t.Request.GitBranch,
		Repository:    t.Request.GitRepo,
		Domain:        t.Domain,
		BlastRadius:   t.BlastRadius,
		Phase:         t.Phase,
		CurrentAction: t.CurrentAction,
		Status:        t.Status,
		WorkerID:      t.NodeID,
		Tags:          append([]string(nil), t.Request.Tags...),
		EditSurfaces:  append([]string(nil), t.EditSurfaces...),
		CreatedAt:     t.CreatedAt,
		StartedAt:     t.StartedAt,
		FinishedAt:    t.FinishedAt,
		ErrorMessage:  errMsg,
		ResultSummary: summary,
	}
	taskID := t.TaskID
	status := t.Status
	finishedAt := t.FinishedAt
	t.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = st.UpdateStatus(ctx, taskID, status, finishedAt, errMsg, summary)
	_ = st.SaveTask(ctx, record)
}
