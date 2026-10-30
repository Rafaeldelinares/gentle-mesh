package joinlog

// High-level event types in the closed catalog of the mesh join log (RFC-003).
//
// Every state mutation in the mesh MUST be recorded as an explicit event in
// the log at the moment it occurs. The projector/replay engine NEVER infers
// state transitions from wall-clock time (e.g. lease expirations); autonomous
// actions like the R2 reaper emit explicit events with typed reason fields.
const (
	// EventTypeNodeJoined indicates a node (worker or coordinator) has announced
	// its identity, capabilities and network endpoint to the mesh.
	EventTypeNodeJoined = "node.joined"

	// EventTypeTaskCreated indicates a new task has been dispatched and accepted
	// by the mesh.
	EventTypeTaskCreated = "task.created"

	// EventTypeTerritoryClaimed indicates an exclusive territorial lock (repo:branch)
	// has been claimed for an active task.
	EventTypeTerritoryClaimed = "territory.claimed"

	// EventTypeTerritoryReleased indicates an exclusive territorial lock has been
	// released. The payload carries the explicit ReleaseReason.
	EventTypeTerritoryReleased = "territory.released"

	// EventTypeTaskSettled indicates a task has completed, settled deterministically,
	// or terminated with a definitive verdict.
	EventTypeTaskSettled = "task.settled"

	// EventTypeTerritoryVerified indicates that an opportunistic verification
	// of declared territory vs observed activity has been performed and recorded (RFC-003 J5).
	EventTypeTerritoryVerified = "territory.verified"
)

// ReleaseReason explains why a territory lock was released.
// This is critical for zero wall-clock inference in Replay (RFC-003 J6, issue #169).
type ReleaseReason string

const (
	// ReasonTaskFinished indicates normal task completion or settlement.
	ReasonTaskFinished ReleaseReason = "task_finished"

	// ReasonTaskFailed indicates the task execution or settlement failed.
	ReasonTaskFailed ReleaseReason = "task_failed"

	// ReasonLeaseExpired indicates the autonomous R2 lease reaper detected an expired
	// lease and emitted this explicit release event at that moment in time.
	// The replay engine consumes this event directly without checking time.Since.
	ReasonLeaseExpired ReleaseReason = "lease_expired"

	// ReasonManualRelease indicates an administrative or user-requested override.
	ReasonManualRelease ReleaseReason = "manual_release"
)

// NodeJoinedPayload is the typed payload for EventTypeNodeJoined.
type NodeJoinedPayload struct {
	NodeID         string   `json:"node_id"`
	Role           string   `json:"role"` // "coordinator", "worker"
	Endpoint       string   `json:"endpoint"`
	AgentPublicKey string   `json:"agent_public_key,omitempty"` // Hex-encoded Ed25519 public key of the node's agent
	Tags           []string `json:"tags,omitempty"`
}

// TaskCreatedPayload is the typed payload for EventTypeTaskCreated.
type TaskCreatedPayload struct {
	TaskID    string `json:"task_id"`
	GitRepo   string `json:"git_repo"`
	GitBranch string `json:"git_branch"`
	Prompt    string `json:"prompt"`
}

// TerritoryClaimedPayload is the typed payload for EventTypeTerritoryClaimed.
type TerritoryClaimedPayload struct {
	TaskID    string `json:"task_id"`
	GitRepo   string `json:"git_repo"`
	GitBranch string `json:"git_branch"`
}

// TerritoryReleasedPayload is the typed payload for EventTypeTerritoryReleased.
type TerritoryReleasedPayload struct {
	TaskID    string        `json:"task_id"`
	GitRepo   string        `json:"git_repo"`
	GitBranch string        `json:"git_branch"`
	Reason    ReleaseReason `json:"reason"`
}

// TaskSettledPayload is the typed payload for EventTypeTaskSettled.
type TaskSettledPayload struct {
	TaskID      string `json:"task_id"`
	Verdict     string `json:"verdict"` // e.g. "SETTLED_CLEAN", "SETTLEMENT_FAILED", "SETTLEMENT_TIMEOUT"
	ReceiptHash string `json:"receipt_hash,omitempty"`
}

// TerritoryStatus represents the outcome of an opportunistic territory verification.
type TerritoryStatus string

const (
	TerritoryStatusClean       TerritoryStatus = "CLEAN"
	TerritoryStatusDiscrepancy TerritoryStatus = "DISCREPANCY"
)

// TerritoryVerifiedPayload carries the cryptographic evidence of an opportunistic
// territory verification (RFC-003 J5, issue #173).
type TerritoryVerifiedPayload struct {
	TaskID           string          `json:"task_id"`
	GitRepo          string          `json:"git_repo"`
	GitBranch        string          `json:"git_branch"`
	DeclaredSurfaces []string        `json:"declared_surfaces"`
	ObservedFiles    []string        `json:"observed_files"`
	Discrepancies    []string        `json:"discrepancies"`
	Status           TerritoryStatus `json:"status"` // CLEAN or DISCREPANCY
}
