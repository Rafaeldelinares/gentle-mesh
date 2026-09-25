package protocol

// TaskStatus represents the lifecycle state of a task in gentle-mesh.
type TaskStatus string

const (
	TaskStatusQueued    TaskStatus = "queued"
	TaskStatusPreparing TaskStatus = "preparing"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCanceled  TaskStatus = "canceled"
)

// TaskRequest defines the payload for creating and dispatching a new remote subagent task.
type TaskRequest struct {
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	SessionID      string      `json:"session_id,omitempty"`
	Agent          string      `json:"agent"`
	Task           string      `json:"task"`
	Prompt         string      `json:"prompt,omitempty"`
	Context        string      `json:"context,omitempty"`
	WorkspaceRoot  string      `json:"workspace_root,omitempty"`
	GitRepo        string      `json:"git_repo,omitempty"`
	GitBranch      string      `json:"git_branch,omitempty"`
	Domain         string      `json:"domain,omitempty"`
	BlastRadius    BlastRadius `json:"blast_radius,omitempty"`
	EditSurfaces   []string    `json:"edit_surfaces,omitempty"`
	Patch          string      `json:"patch,omitempty"`
	TimeoutSeconds int         `json:"timeout_seconds,omitempty"`
	// Retry configuration
	MaxRetries    int          `json:"max_retries,omitempty"` // 0 = no retry
	RetryDelay    int          `json:"retry_delay_seconds,omitempty"` // delay between retries
	Tags           []string    `json:"tags,omitempty"`
}

// TaskResponse represents the initial acknowledgement returned when a task is accepted.
type TaskResponse struct {
	TaskID    string     `json:"task_id"`
	SessionID string     `json:"session_id,omitempty"`
	Status    TaskStatus `json:"status"`
	EventsURL string     `json:"events_url"`
	CreatedAt int64      `json:"created_at"`
}

// TaskState represents a full point-in-time snapshot of a task lifecycle.
type TaskState struct {
	TaskID     string             `json:"task_id"`
	SessionID  string             `json:"session_id,omitempty"`
	Request    TaskRequest        `json:"request"`
	Status     TaskStatus         `json:"status"`
	CreatedAt  int64              `json:"created_at"`
	StartedAt  int64              `json:"started_at,omitempty"`
	FinishedAt int64              `json:"finished_at,omitempty"`
	Completion *CompletionPayload `json:"completion,omitempty"`
	Error      *ErrorPayload      `json:"error,omitempty"`
}

// TaskReplyRequest represents a response to an interactive query event from a subagent.
type TaskReplyRequest struct {
	QueryID string `json:"query_id"`
	Answer  string `json:"answer"`
}
