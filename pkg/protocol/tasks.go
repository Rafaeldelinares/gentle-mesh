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
	TaskID         string      `json:"task_id,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	SessionID      string      `json:"session_id,omitempty"`
	Agent          string      `json:"agent"`
	Task           string      `json:"task"`
	Prompt         string      `json:"prompt,omitempty"`
	Context        string      `json:"context,omitempty"`
	WorkspaceRoot  string      `json:"workspace_root,omitempty"`
	GitRepo        string      `json:"git_repo,omitempty"`
	// GitBranch is the branch this task is booked on: it is the key of the coordinator's
	// exclusive branch lock (and part of the task territory), not a checkout. No runner runs
	// git, so the branch is never created, switched to or checked for existence. Accepted
	// responses carry GitBranchLockOnlyWarning so clients are told this on the wire.
	GitBranch      string      `json:"git_branch,omitempty"`
	Domain         string      `json:"domain,omitempty"`
	BlastRadius    BlastRadius `json:"blast_radius,omitempty"`
	EditSurfaces   []string    `json:"edit_surfaces,omitempty"`
	Patch          string      `json:"patch,omitempty"`
	TimeoutSeconds int         `json:"timeout_seconds,omitempty"`
	// Retry configuration
	MaxRetries    int          `json:"max_retries,omitempty"` // 0 = no retry
	RetryDelay    int          `json:"retry_delay_seconds,omitempty"` // delay between retries
	// Priority: higher = runs first (default 0, range -100 to 100)
	Priority      int          `json:"priority,omitempty"`
	Tags           []string    `json:"tags,omitempty"`
	// Checkpoint resume
	ResumeFrom    *CheckpointPayload `json:"resume_from,omitempty"` // Resume from checkpoint
	EnableCheckpoint bool            `json:"enable_checkpoint"`    // Enable periodic checkpointing
	CheckpointInterval int           `json:"checkpoint_interval_secs"` // Seconds between checkpoints
}

// GitBranchLockOnlyWarning is the stable advisory text shipped in TaskResponse.Warnings when a
// task request carries a non-empty git_branch.
const GitBranchLockOnlyWarning = "git_branch is only a mutual exclusion key: no runner checks the branch out, and a branch that does not exist is not an error"

// TaskResponse represents the initial acknowledgement returned when a task is accepted.
type TaskResponse struct {
	TaskID    string     `json:"task_id"`
	SessionID string     `json:"session_id,omitempty"`
	Status    TaskStatus `json:"status"`
	EventsURL string     `json:"events_url"`
	CreatedAt int64      `json:"created_at"`
	// Warnings carries additive advisory notes about the accepted request. It is omitempty, so
	// a response without warnings keeps the exact JSON shape of the pre-warning contract.
	Warnings []string `json:"warnings,omitempty"`
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
	// WorkerID is the mesh node that accepted this task. Additive and optional: a task that was
	// never attributed (for example a run answered by the coordinator's local fallback) keeps
	// producing exactly the same JSON as before.
	WorkerID string `json:"worker_id,omitempty"`
	// Checkpoint for resume
	Checkpoint *CheckpointPayload  `json:"checkpoint,omitempty"`
}

// CheckpointPayload stores task progress for resume capability.
type CheckpointPayload struct {
	Step        int               `json:"step"`          // Current step number
	TotalSteps  int               `json:"total_steps"`   // Estimated total steps
	Progress    string            `json:"progress"`      // Human-readable progress
	FilesDone   []string         `json:"files_done"`    // Files modified so far
	Context     map[string]string `json:"context"`      // Arbitrary key-value state
	LastUpdated int64            `json:"last_updated"`  // Unix timestamp
}

// TaskReplyRequest represents a response to an interactive query event from a subagent.
type TaskReplyRequest struct {
	QueryID string `json:"query_id"`
	Answer  string `json:"answer"`
}
