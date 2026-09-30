package envelope

import (
	"time"
)

// CurrentProtocolVersion defines the strict protocol version supported (S9).
const CurrentProtocolVersion = "2"

// ─────────────────────────────────────────────────────────────────
// Identity
// ─────────────────────────────────────────────────────────────────

// CognitiveTaskEnvelope is the root contract document signed by the emitter.
// All fields are set before hashing and signing. The envelope_hash field
// is populated by the emitter after computing the JCS canonical hash.
type CognitiveTaskEnvelope struct {
	// ProtocolVersion is the protocol version (S9: must be strictly "2").
	ProtocolVersion string `json:"protocol_version"`

	// MeshID is the network identifier this envelope belongs to.
	MeshID string `json:"mesh_id"`

	// EnvelopeID is the unique identifier for this contract (UUIDv7).
	EnvelopeID string `json:"envelope_id"`

	// EmitterAgentID is the agent that creates and signs this envelope.
	EmitterAgentID string `json:"emitter_agent_id"`

	// ExecutorAgentID is the intended recipient and executor.
	ExecutorAgentID string `json:"executor_agent_id"`

	// Territory defines the execution context (repository, branch, workspace).
	Territory Territory `json:"territory"`

	// Preconditions are local invariants that the executor validates
	// synchronously during the pre-flight handshake before accepting.
	Preconditions []Precondition `json:"preconditions"`

	// Assertions are the falsifiable settlement criteria evaluated
	// deterministically by the settlement engine after execution.
	Assertions []Assertion `json:"assertions"`

	// TimeoutSeconds is the maximum execution window. If exceeded,
	// the contract transitions to SETTLEMENT_TIMEOUT.
	TimeoutSeconds int `json:"timeout_seconds"`

	// MaxRemediations limits how many local repairs the executor may
	// attempt before the contract transitions to SETTLEMENT_FAILED.
	// A value of 0 means no remediation is allowed.
	MaxRemediations int `json:"max_remediations"`

	// NoSubdelegation, when true, forbids the executor from delegating
	// the contract to any third agent. This is always true in v1.
	NoSubdelegation bool `json:"no_subdelegation"`

	// CreatedAt is the epoch timestamp when the envelope was created.
	CreatedAt time.Time `json:"created_at"`

	// EmitterSignature is the Ed25519 signature over the JCS canonical
	// hash of this envelope (with this field set to "").
	// Populated by the emitter before transmission.
	EmitterSignature string `json:"emitter_signature,omitempty"`

	// EnvelopeHash is the JCS canonical SHA-256 hash of this envelope
	// (with both signature fields set to ""), computed by the emitter.
	// This hash appears in the final SettlementReceipt for correlation.
	EnvelopeHash string `json:"envelope_hash,omitempty"`
}

// ─────────────────────────────────────────────────────────────────
// Territory
// ─────────────────────────────────────────────────────────────────

// Territory defines the execution context that the executor operates in.
// It describes the repository, branch, and workspace path that constitute
// the "territory" — the physical location where work happens.
type Territory struct {
	// Repository is the canonical URI of the Git repository.
	// Normalized: no trailing slashes, no .git suffix.
	// Examples: "github.com/org/repo", "https://github.com/org/repo"
	Repository string `json:"repository"`

	// Branch is the Git branch (or ref) where execution occurs.
	// Must be a valid Git ref name.
	Branch string `json:"branch"`

	// WorkspacePath is the absolute filesystem path on the executor's
	// host where the repository is checked out.
	// Example: "/srv/workspace/repo"
	WorkspacePath string `json:"workspace_path"`
}

// ─────────────────────────────────────────────────────────────────
// Preconditions (pre-flight handshake)
// ─────────────────────────────────────────────────────────────────

// PreconditionType defines the kinds of local checks the executor
// performs during the pre-flight handshake.
type PreconditionType string

const (
	// PreconditionCommandExitCode validates that a command exits with code 0.
	PreconditionCommandExitCode PreconditionType = "command_exit_code"
	// PreconditionGitCleanWorktree checks that the worktree is clean.
	PreconditionGitCleanWorktree PreconditionType = "git_clean_worktree"
	// PreconditionPortAvailable verifies a TCP port is free.
	PreconditionPortAvailable PreconditionType = "port_available"
	// PreconditionToolAvailable checks that a binary is in PATH.
	PreconditionToolAvailable PreconditionType = "tool_available"
)

// Precondition is a local invariant that the executor evaluates synchronously
// during the pre-flight handshake, before accepting the contract.
type Precondition struct {
	// Type determines which check is performed.
	Type PreconditionType `json:"type"`
	// Params carries type-specific configuration.
	Params map[string]string `json:"params"`
}

// ToolParams returns the command to check for PreconditionToolAvailable.
func (p Precondition) ToolParams() (tool string, minVersion string, ok bool) {
	if p.Type != PreconditionToolAvailable {
		return "", "", false
	}
	return p.Params["tool"], p.Params["min_version"], true
}

// CommandParams returns the command string for PreconditionCommandExitCode.
func (p Precondition) CommandParams() (command string, ok bool) {
	if p.Type != PreconditionCommandExitCode {
		return "", false
	}
	return p.Params["command"], true
}

// PortParams returns the port number for PreconditionPortAvailable.
func (p Precondition) PortParams() (port int, ok bool) {
	if p.Type != PreconditionPortAvailable {
		return 0, false
	}
	return 0, true // parsed separately
}

// ─────────────────────────────────────────────────────────────────
// Assertions (settlement DSL — closed set)
// ─────────────────────────────────────────────────────────────────

// AssertionType defines the seven closed-set settlement criteria.
type AssertionType string

const (
	// AssertionFileModified checks that a file's SHA-256 changed vs. baseline.
	AssertionFileModified AssertionType = "file_modified"
	// AssertionFileHashEquals verifies a file's SHA-256 equals the expected value.
	AssertionFileHashEquals AssertionType = "file_hash_equals"
	// AssertionCommandExitCode verifies a command exits with expected code.
	AssertionCommandExitCode AssertionType = "command_exit_code"
	// AssertionCommandOutputContains checks stdout/stderr for a pattern.
	AssertionCommandOutputContains AssertionType = "command_output_contains"
	// AssertionGitCleanWorktree verifies the worktree is clean post-execution.
	AssertionGitCleanWorktree AssertionType = "git_clean_worktree"
	// AssertionPortAvailable verifies a port is free after execution.
	AssertionPortAvailable AssertionType = "port_available"
	// AssertionNoRegression runs a test suite and checks exit code 0.
	AssertionNoRegression AssertionType = "no_regression"
)

// Assertion is a falsifiable criterion evaluated by the settlement engine
// after the executor completes. Each assertion is independent and
// deterministic — it either passes or fails, with no opinion from the LLM.
type Assertion struct {
	// ID uniquely identifies this assertion within the envelope.
	// Example: "tests_auth_passing", "go_build_success"
	ID string `json:"id"`

	// Type determines which settlement check is performed.
	Type AssertionType `json:"type"`

	// Params carries type-specific configuration.
	// All paths are relative to Territory.WorkspacePath unless absolute.
	Params AssertionParams `json:"params"`
}

// AssertionParams is a discriminated union of parameters for each assertion type.
// Only the field corresponding to the assertion Type is populated.
type AssertionParams struct {
	// Common to all types.
	Description string `json:"description,omitempty"`

	// For file_modified, file_hash_equals.
	FilePath       string `json:"file_path,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`

	// For command_exit_code, command_output_contains, no_regression.
	Command          string   `json:"command,omitempty"`
	ExpectedExitCode int      `json:"expected_exit_code,omitempty"`
	ContainsPattern  string   `json:"contains_pattern,omitempty"`
	ContainsRegex    bool     `json:"contains_regex,omitempty"`
	WorkingDir       string   `json:"working_dir,omitempty"`
	Env              []string `json:"env,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`

	// For port_available.
	Port int `json:"port,omitempty"`
}

// ─────────────────────────────────────────────────────────────────
// Contract status machine
// ─────────────────────────────────────────────────────────────────

// ContractStatus is the state of the contract lifecycle.
type ContractStatus string

const (
	ContractStatusProposed           ContractStatus = "PROPOSED"
	ContractStatusAccepted           ContractStatus = "ACCEPTED"
	ContractStatusExecuting         ContractStatus = "EXECUTING"
	ContractStatusSettling         ContractStatus = "SETTLING"
	ContractStatusRemediating      ContractStatus = "REMEDIATING"
	ContractStatusSettled          ContractStatus = "SETTLED"
	ContractStatusAcceptedFinal     ContractStatus = "ACCEPTED_FINAL"
	ContractStatusRejected          ContractStatus = "REJECTED"
	ContractStatusExpired          ContractStatus = "EXPIRED"
	ContractStatusSettlementFailed  ContractStatus = "SETTLEMENT_FAILED"
	ContractStatusDisputed         ContractStatus = "DISPUTED"
	ContractStatusAbandoned        ContractStatus = "ABANDONED"
	ContractStatusSettlementTimeout ContractStatus = "SETTLEMENT_TIMEOUT"
)

// IsTerminal returns true if the status is a final state.
func (s ContractStatus) IsTerminal() bool {
	switch s {
	case ContractStatusSettled, ContractStatusAcceptedFinal,
		ContractStatusRejected, ContractStatusExpired,
		ContractStatusSettlementFailed, ContractStatusAbandoned,
		ContractStatusSettlementTimeout:
		return true
	default:
		return false
	}
}

// String returns the string representation.
func (s ContractStatus) String() string { return string(s) }

// ─────────────────────────────────────────────────────────────────
// Lease (pre-flight handshake response)
// ─────────────────────────────────────────────────────────────────

// Lease is the executor's response to a pre-flight handshake.
// It commits the executor to the contract within the timeout window.
type Lease struct {
	// ProtocolVersion is the protocol version (S9: must be strictly "2").
	ProtocolVersion string `json:"protocol_version"`

	// MeshID is the network identifier this lease is bound to.
	MeshID string `json:"mesh_id"`

	// LeaseID uniquely identifies this lease.
	LeaseID string `json:"lease_id"`

	// EnvelopeID references the contract this lease applies to.
	EnvelopeID string `json:"envelope_id"`

	// ExecutorAgentID is the agent granting the lease.
	ExecutorAgentID string `json:"executor_agent_id"`

	// Accepted indicates whether the executor accepts the contract.
	Accepted bool `json:"accepted"`

	// PreconditionResults reports the outcome of each pre-flight check.
	PreconditionResults []PreconditionResult `json:"precondition_results,omitempty"`

	// RejectionReason explains why the lease was denied (if applicable).
	RejectionReason string `json:"rejection_reason,omitempty"`

	// ExpiresAt is when the lease window closes.
	// The contract transitions to EXPIRED if not accepted before this time.
	ExpiresAt time.Time `json:"expires_at"`

	// ExecutorSignature is the Ed25519 signature over the JCS hash of this lease.
	ExecutorSignature string `json:"executor_signature,omitempty"`
}

// PreconditionResult reports the outcome of a single pre-flight check.
type PreconditionResult struct {
	PreconditionIndex int    `json:"precondition_index"`
	Type             string `json:"type"`
	Passed           bool   `json:"passed"`
	Message          string `json:"message,omitempty"`
}
