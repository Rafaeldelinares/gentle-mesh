package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	_ "modernc.org/sqlite"
)

var _ TaskStore = (*SQLiteStore)(nil)

// SQLiteStore is a thread-safe, pure Go SQLite implementation of TaskStore.
type SQLiteStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	closeMu sync.RWMutex
	closed  bool
}

// NewSQLiteStore opens an SQLite database, configures WAL mode, busy timeout,
// and ensures the tasks table and indexes exist.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create sqlite directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// PRAGMA journal_mode=WAL
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// PRAGMA busy_timeout=5000
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Create table and indexes
	ddlStatements := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			task_id TEXT PRIMARY KEY,
			branch TEXT NOT NULL,
			repository TEXT NOT NULL,
			domain TEXT NOT NULL,
			blast_radius TEXT NOT NULL,
			phase TEXT NOT NULL,
			current_action TEXT NOT NULL,
			status TEXT NOT NULL,
			worker_id TEXT NOT NULL,
			tags TEXT NOT NULL,
			edit_surfaces TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL,
			error_message TEXT NOT NULL,
			result_summary TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_branch ON tasks(branch);`,
	}

	for _, stmt := range ddlStatements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to initialize sqlite schema: %w", err)
		}
	}

	// Initialize certificate schema
	if err := InitCertSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize cert schema: %w", err)
	}

	return &SQLiteStore{
		db: db,
	}, nil
}

// SaveTask persists or updates a TaskRecord in the SQLite database.
func (s *SQLiteStore) SaveTask(ctx context.Context, task *TaskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task == nil {
		return errors.New("task cannot be nil")
	}
	if task.TaskID == "" {
		return errors.New("task id cannot be empty")
	}

	tagsJSON, err := json.Marshal(task.Tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}
	editSurfacesJSON, err := json.Marshal(task.EditSurfaces)
	if err != nil {
		return fmt.Errorf("failed to marshal edit surfaces: %w", err)
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `
	INSERT INTO tasks (
		task_id, branch, repository, domain, blast_radius, phase,
		current_action, status, worker_id, tags, edit_surfaces,
		created_at, started_at, finished_at, error_message, result_summary
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(task_id) DO UPDATE SET
		branch = excluded.branch,
		repository = excluded.repository,
		domain = excluded.domain,
		blast_radius = excluded.blast_radius,
		phase = excluded.phase,
		current_action = excluded.current_action,
		status = excluded.status,
		worker_id = excluded.worker_id,
		tags = excluded.tags,
		edit_surfaces = excluded.edit_surfaces,
		created_at = excluded.created_at,
		started_at = excluded.started_at,
		finished_at = excluded.finished_at,
		error_message = excluded.error_message,
		result_summary = excluded.result_summary;
	`

	_, err = s.db.ExecContext(
		ctx,
		query,
		task.TaskID,
		task.Branch,
		task.Repository,
		task.Domain,
		string(task.BlastRadius),
		string(task.Phase),
		task.CurrentAction,
		string(task.Status),
		task.WorkerID,
		string(tagsJSON),
		string(editSurfacesJSON),
		task.CreatedAt,
		task.StartedAt,
		task.FinishedAt,
		task.ErrorMessage,
		task.ResultSummary,
	)
	if err != nil {
		return fmt.Errorf("failed to save task record: %w", err)
	}

	return nil
}

// GetTask retrieves a TaskRecord by its ID.
func (s *SQLiteStore) GetTask(ctx context.Context, taskID string) (*TaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, ErrTaskNotFound
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("store is closed")
	}

	query := `
	SELECT task_id, branch, repository, domain, blast_radius, phase,
	       current_action, status, worker_id, tags, edit_surfaces,
	       created_at, started_at, finished_at, error_message, result_summary
	FROM tasks
	WHERE task_id = ?;
	`

	row := s.db.QueryRowContext(ctx, query, taskID)

	var (
		task             TaskRecord
		blastRadius      string
		phase            string
		status           string
		tagsJSON         string
		editSurfacesJSON string
	)

	err := row.Scan(
		&task.TaskID,
		&task.Branch,
		&task.Repository,
		&task.Domain,
		&blastRadius,
		&phase,
		&task.CurrentAction,
		&status,
		&task.WorkerID,
		&tagsJSON,
		&editSurfacesJSON,
		&task.CreatedAt,
		&task.StartedAt,
		&task.FinishedAt,
		&task.ErrorMessage,
		&task.ResultSummary,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("failed to query task: %w", err)
	}

	task.BlastRadius = protocol.BlastRadius(blastRadius)
	task.Phase = protocol.AgentPhase(phase)
	task.Status = protocol.TaskStatus(status)

	if tagsJSON != "" && tagsJSON != "null" {
		if err := json.Unmarshal([]byte(tagsJSON), &task.Tags); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tags: %w", err)
		}
	}
	if editSurfacesJSON != "" && editSurfacesJSON != "null" {
		if err := json.Unmarshal([]byte(editSurfacesJSON), &task.EditSurfaces); err != nil {
			return nil, fmt.Errorf("failed to unmarshal edit surfaces: %w", err)
		}
	}

	return &task, nil
}

// UpdateStatus updates task status, completion time, error, and result.
func (s *SQLiteStore) UpdateStatus(ctx context.Context, taskID string, status protocol.TaskStatus, completedAt int64, errMsg string, resultSummary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if taskID == "" {
		return ErrTaskNotFound
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `
	UPDATE tasks
	SET status = ?,
	    finished_at = CASE WHEN ? != 0 THEN ? ELSE finished_at END,
	    error_message = ?,
	    result_summary = ?
	WHERE task_id = ?;
	`

	res, err := s.db.ExecContext(ctx, query, string(status), completedAt, completedAt, errMsg, resultSummary, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect affected rows: %w", err)
	}

	if rowsAffected == 0 {
		return ErrTaskNotFound
	}

	return nil
}

// ListTasks queries task records using the provided filter and pagination.
func (s *SQLiteStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*TaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("store is closed")
	}

	var conditions []string
	var args []any

	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.Domain != "" {
		conditions = append(conditions, "domain = ?")
		args = append(args, filter.Domain)
	}
	if filter.Branch != "" {
		conditions = append(conditions, "branch = ?")
		args = append(args, filter.Branch)
	}
	if filter.Repository != "" {
		conditions = append(conditions, "repository = ?")
		args = append(args, filter.Repository)
	}
	if filter.WorkerID != "" {
		conditions = append(conditions, "worker_id = ?")
		args = append(args, filter.WorkerID)
	}

	query := `
	SELECT task_id, branch, repository, domain, blast_radius, phase,
	       current_action, status, worker_id, tags, edit_surfaces,
	       created_at, started_at, finished_at, error_message, result_summary
	FROM tasks
	`

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC, task_id DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, filter.Offset)
		}
	} else if filter.Offset > 0 {
		query += " LIMIT -1 OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*TaskRecord
	for rows.Next() {
		var (
			task             TaskRecord
			blastRadius      string
			phase            string
			status           string
			tagsJSON         string
			editSurfacesJSON string
		)

		err := rows.Scan(
			&task.TaskID,
			&task.Branch,
			&task.Repository,
			&task.Domain,
			&blastRadius,
			&phase,
			&task.CurrentAction,
			&status,
			&task.WorkerID,
			&tagsJSON,
			&editSurfacesJSON,
			&task.CreatedAt,
			&task.StartedAt,
			&task.FinishedAt,
			&task.ErrorMessage,
			&task.ResultSummary,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task row: %w", err)
		}

		task.BlastRadius = protocol.BlastRadius(blastRadius)
		task.Phase = protocol.AgentPhase(phase)
		task.Status = protocol.TaskStatus(status)

		if tagsJSON != "" && tagsJSON != "null" {
			if err := json.Unmarshal([]byte(tagsJSON), &task.Tags); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tags: %w", err)
			}
		}
		if editSurfacesJSON != "" && editSurfacesJSON != "null" {
			if err := json.Unmarshal([]byte(editSurfacesJSON), &task.EditSurfaces); err != nil {
				return nil, fmt.Errorf("failed to unmarshal edit surfaces: %w", err)
			}
		}

		tasks = append(tasks, &task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	if tasks == nil {
		tasks = []*TaskRecord{}
	}

	return tasks, nil
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.closed || s.db == nil {
		return nil
	}

	s.closed = true
	err := s.db.Close()
	s.db = nil
	return err
}
