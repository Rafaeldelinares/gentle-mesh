package protocol

// ProtocolVersion2 represents the canonical S9 protocol version "2".
const ProtocolVersion2 = "2"

// A2ATaskState represents canonical task states from A2A Specification v1.0.0.
// A2A forbids adding new enum values to TaskState; protocol extensions must project
// onto these canonical states and enrich metadata or substate.
type A2ATaskState string

const (
	A2AStateSubmitted A2ATaskState = "TASK_STATE_SUBMITTED"
	A2AStateWorking   A2ATaskState = "TASK_STATE_WORKING"
	A2AStateRejected  A2ATaskState = "TASK_STATE_REJECTED"
	A2AStateCompleted A2ATaskState = "TASK_STATE_COMPLETED"
	A2AStateFailed    A2ATaskState = "TASK_STATE_FAILED"
	A2AStateCanceled  A2ATaskState = "TASK_STATE_CANCELED"
)

// IsTerminal returns true if the A2A task state is a final state.
func (s A2ATaskState) IsTerminal() bool {
	switch s {
	case A2AStateRejected, A2AStateCompleted, A2AStateFailed, A2AStateCanceled:
		return true
	default:
		return false
	}
}

// String returns the string representation of A2ATaskState.
func (s A2ATaskState) String() string {
	return string(s)
}

// CognitiveState represents the granular lifecycle states of a task in the Gentle Mesh RFC-002
// cognitive network.
type CognitiveState string

const (
	CognitiveStateSubmitted            CognitiveState = "SUBMITTED"
	CognitiveStatePreflight            CognitiveState = "PREFLIGHT"
	CognitiveStateNegotiating          CognitiveState = "NEGOTIATING"
	CognitiveStateRejectedPrecondition CognitiveState = "REJECTED_PRECONDITION"
	CognitiveStateRejectedCapability   CognitiveState = "REJECTED_CAPABILITY"
	CognitiveStateRunning              CognitiveState = "RUNNING"
	CognitiveStateSettling             CognitiveState = "SETTLING"
	CognitiveStateSettledClean         CognitiveState = "SETTLED_CLEAN"
	CognitiveStateSettlementFailed     CognitiveState = "SETTLEMENT_FAILED"
	CognitiveStateDisputed             CognitiveState = "DISPUTED"
	CognitiveStateSettlementTimeout    CognitiveState = "SETTLEMENT_TIMEOUT"
	CognitiveStateAborted              CognitiveState = "ABORTED"
)

// String returns the string representation of CognitiveState.
func (c CognitiveState) String() string {
	return string(c)
}

// A2AStateProjection represents the projection of a Gentle Mesh cognitive state onto A2A,
// including canonical TaskState and metadata enrichments (substate, reason, error).
type A2AStateProjection struct {
	State    A2ATaskState `json:"state"`
	Substate string       `json:"substate,omitempty"`
	Reason   string       `json:"reason,omitempty"`
}

// A2AStateMapping contains the deterministic mapping table from Gentle Mesh cognitive states
// to A2A canonical states and metadata as specified in docs/rfcs/002-a2a-positioning.md (Section e).
var A2AStateMapping = map[CognitiveState]A2AStateProjection{
	CognitiveStateSubmitted: {
		State:    A2AStateSubmitted,
		Substate: "acknowledged",
	},
	CognitiveStatePreflight: {
		State:    A2AStateWorking,
		Substate: "preflight_validation",
	},
	CognitiveStateNegotiating: {
		State:    A2AStateWorking,
		Substate: "preflight_validation",
	},
	CognitiveStateRejectedPrecondition: {
		State:  A2AStateRejected,
		Reason: "precondition_failed",
	},
	CognitiveStateRejectedCapability: {
		State:  A2AStateRejected,
		Reason: "missing_capability",
	},
	CognitiveStateRunning: {
		State:    A2AStateWorking,
		Substate: "executing",
	},
	CognitiveStateSettling: {
		State:    A2AStateWorking,
		Substate: "evaluating_assertions",
	},
	CognitiveStateSettledClean: {
		State: A2AStateCompleted,
	},
	CognitiveStateSettlementFailed: {
		State: A2AStateFailed,
	},
	CognitiveStateDisputed: {
		State: A2AStateFailed,
	},
	CognitiveStateSettlementTimeout: {
		State:  A2AStateFailed,
		Reason: "timeout",
	},
	CognitiveStateAborted: {
		State:  A2AStateCanceled,
		Reason: "canceled_by_client",
	},
}

// MapCognitiveToA2A projects a Gentle Mesh cognitive state to its corresponding A2A state
// and metadata representation. Returns false if the cognitive state is unknown.
func MapCognitiveToA2A(state CognitiveState) (A2AStateProjection, bool) {
	proj, ok := A2AStateMapping[state]
	return proj, ok
}
