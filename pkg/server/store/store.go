package store

import (
	"context"
	"errors"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// ErrTaskNotFound is returned when a task is not found in the store.
var ErrTaskNotFound = errors.New("task not found")

// TaskRecord represents a persistent subagent task record in gentle-mesh.
type TaskRecord struct {
	TaskID        string               `json:"task_id"`
	Branch        string               `json:"branch"`
	Repository    string               `json:"repository"`
	Domain        string               `json:"domain"`
	BlastRadius   protocol.BlastRadius `json:"blast_radius"`
	Phase         protocol.AgentPhase  `json:"phase"`
	CurrentAction string               `json:"current_action"`
	Status        protocol.TaskStatus  `json:"status"`
	WorkerID      string               `json:"worker_id"`
	Tags          []string             `json:"tags"`
	EditSurfaces  []string             `json:"edit_surfaces"`
	CreatedAt     int64                `json:"created_at"`
	StartedAt     int64                `json:"started_at"`
	FinishedAt    int64                `json:"finished_at"`
	ErrorMessage  string               `json:"error_message"`
	ResultSummary string               `json:"result_summary"`
}

// Clone returns a deep copy of the TaskRecord.
func (t *TaskRecord) Clone() *TaskRecord {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Tags != nil {
		cp.Tags = make([]string, len(t.Tags))
		copy(cp.Tags, t.Tags)
	}
	if t.EditSurfaces != nil {
		cp.EditSurfaces = make([]string, len(t.EditSurfaces))
		copy(cp.EditSurfaces, t.EditSurfaces)
	}
	return &cp
}

// Matches returns true if the TaskRecord satisfies the non-empty fields of TaskFilter.
func (t *TaskRecord) Matches(f TaskFilter) bool {
	if t == nil {
		return false
	}
	if f.Status != "" && t.Status != f.Status {
		return false
	}
	if f.Domain != "" && t.Domain != f.Domain {
		return false
	}
	if f.Branch != "" && t.Branch != f.Branch {
		return false
	}
	if f.Repository != "" && t.Repository != f.Repository {
		return false
	}
	if f.WorkerID != "" && t.WorkerID != f.WorkerID {
		return false
	}
	return true
}

// TaskFilter defines criteria for querying and paginating task records.
type TaskFilter struct {
	Status     protocol.TaskStatus `json:"status,omitempty"`
	Domain     string              `json:"domain,omitempty"`
	Branch     string              `json:"branch,omitempty"`
	Repository string              `json:"repository,omitempty"`
	WorkerID   string              `json:"worker_id,omitempty"`
	Limit      int                 `json:"limit,omitempty"`
	Offset     int                 `json:"offset,omitempty"`
}

// TaskStore defines the storage abstraction for subagent task records.
type TaskStore interface {
	SaveTask(ctx context.Context, task *TaskRecord) error
	GetTask(ctx context.Context, taskID string) (*TaskRecord, error)
	UpdateStatus(ctx context.Context, taskID string, status protocol.TaskStatus, completedAt int64, errMsg string, resultSummary string) error
	ListTasks(ctx context.Context, filter TaskFilter) ([]*TaskRecord, error)
	Close() error
}
