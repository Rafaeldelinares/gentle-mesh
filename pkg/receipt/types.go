package receipt

import (
	"time"
)

// ─────────────────────────────────────────────────────────────────
// SettlementReceipt
// ─────────────────────────────────────────────────────────────────

// SettlementReceipt is the verifiable record of a completed contract settlement.
// It is emitted by the executor (B) after evaluating all assertions.
type SettlementReceipt struct {
	// ReceiptID uniquely identifies this receipt (UUIDv7).
	ReceiptID string `json:"receipt_id"`

	// ContractID references the CognitiveTaskEnvelope that generated this receipt.
	ContractID string `json:"contract_id"`

	// EnvelopeHash is the JCS canonical SHA-256 hash of the original envelope.
	// This allows correlation between the receipt and the contract.
	EnvelopeHash string `json:"envelope_hash"`

	// EmitterAgentID is the agent that sent the envelope (A).
	EmitterAgentID string `json:"emitter_agent_id"`

	// ExecutorAgentID is the agent that executed and produced this receipt (B).
	ExecutorAgentID string `json:"executor_agent_id"`

	// Territory mirrors the envelope's territory for auditability.
	Territory Territory `json:"territory"`

	// Verdict is the settlement outcome.
	Verdict Verdict `json:"verdict"`

	// Assertions contains the result of each settlement assertion.
	Assertions []AssertionResult `json:"assertions"`

	// RemediationChain records any repair attempts made during SETTLING.
	// Only populated if Verdict == VerdictRemediated.
	RemediationChain []RemediationAttempt `json:"remediation_chain,omitempty"`

	// ExecutorSignature is B's Ed25519 signature over the JCS hash of this
	// receipt (with this field and emitter_signature set to "").
	// Populated by the executor before transmission.
	ExecutorSignature string `json:"executor_signature,omitempty"`

	// ExecutorSignedAt is the timestamp when B signed the receipt.
	ExecutorSignedAt time.Time `json:"executor_signed_at"`

	// PreviousReceiptHash is the SHA-256 of the executor's signature from the
	// previous receipt in this (A,B) pair's chain. Empty for the first receipt.
	PreviousReceiptHash string `json:"previous_receipt_hash,omitempty"`

	// EmitterAcceptance is A's formal response: ACCEPTED or DISPUTED.
	EmitterAcceptance Acceptance `json:"emitter_acceptance,omitempty"`

	// EmitterAcceptanceAt is the timestamp when A signed the acceptance.
	EmitterAcceptanceAt *time.Time `json:"emitter_acceptance_at,omitempty"`

	// DisputeReason explains why A disputed the receipt (if applicable).
	DisputeReason string `json:"dispute_reason,omitempty"`

	// EmitterSignature is A's Ed25519 signature over the JCS hash of the
	// receipt with both signatures set to "".
	EmitterSignature string `json:"emitter_signature,omitempty"`
}

// Territory mirrors the envelope's Territory for full auditability.
type Territory struct {
	Repository    string `json:"repository"`
	Branch       string `json:"branch"`
	WorkspacePath string `json:"workspace_path"`
}

// ─────────────────────────────────────────────────────────────────
// AssertionResult
// ─────────────────────────────────────────────────────────────────

// AssertionResult records the outcome of a single settlement assertion.
type AssertionResult struct {
	// AssertionIndex matches the position in the envelope's Assertions array.
	AssertionIndex int `json:"assertion_index"`

	// AssertionID is the human-readable identifier from the envelope.
	AssertionID string `json:"assertion_id"`

	// AssertionType mirrors the assertion type from the envelope.
	AssertionType string `json:"assertion_type"`

	// Result is the outcome: PASS or FAIL.
	Result Result `json:"result"`

	// Evidence contains the raw output that led to this result.
	Evidence AssertionEvidence `json:"evidence"`

	// Message provides human-readable context.
	Message string `json:"message,omitempty"`
}

// AssertionEvidence captures the raw data that determined the assertion result.
type AssertionEvidence struct {
	// For command_exit_code, command_output_contains, no_regression:
	Command        string `json:"command,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
	StdoutHash    string `json:"stdout_hash,omitempty"`
	StderrHash    string `json:"stderr_hash,omitempty"`
	StdoutContains bool   `json:"stdout_contains,omitempty"`
	StderrContains bool   `json:"stderr_contains,omitempty"`

	// For file_modified, file_hash_equals:
	FilePath        string `json:"file_path,omitempty"`
	ExpectedSHA256  string `json:"expected_sha256,omitempty"`
	ActualSHA256    string `json:"actual_sha256,omitempty"`
	FileWasModified bool   `json:"file_was_modified,omitempty"`

	// For git_clean_worktree:
	GitStatus string `json:"git_status,omitempty"`

	// For port_available:
	Port         int  `json:"port,omitempty"`
	PortWasFree bool `json:"port_was_free,omitempty"`

	// Timestamp of the assertion check.
	CheckedAt time.Time `json:"checked_at"`
}

// ─────────────────────────────────────────────────────────────────
// RemediationAttempt
// ─────────────────────────────────────────────────────────────────

// RemediationAttempt records a repair action taken during SETTLING.
type RemediationAttempt struct {
	// AttemptIndex is the 0-based position in the remediation chain.
	AttemptIndex int `json:"attempt_index"`

	// TriggeredBy is which assertion failed and triggered this repair.
	TriggeredByAssertionID string `json:"triggered_by_assertion_id"`

	// Action is a description of the repair performed.
	Action string `json:"action"`

	// Command is the shell command executed (if any).
	Command string `json:"command,omitempty"`

	// ExitCode is the result of the repair command.
	ExitCode int `json:"exit_code,omitempty"`

	// AssertionsRechecked is the list of assertion IDs re-evaluated after this repair.
	AssertionsRechecked []string `json:"assertions_rechecked,omitempty"`

	// Result is the outcome: SUCCESS or FAILURE.
	Result RemediationResult `json:"result"`

	// Message is human-readable context.
	Message string `json:"message,omitempty"`

	// AttemptedAt is when the repair was attempted.
	AttemptedAt time.Time `json:"attempted_at"`
}

// ─────────────────────────────────────────────────────────────────
// Verdict (settlement outcome)
// ─────────────────────────────────────────────────────────────────

// Verdict is the settlement outcome determined by the settlement engine.
type Verdict string

const (
	// VerdictSettledClean means all assertions passed on the first evaluation.
	VerdictSettledClean Verdict = "SETTLED_CLEAN"
	// VerdictRemediated means all assertions passed after at least one repair.
	VerdictRemediated Verdict = "SETTLED_WITH_REPAIR"
	// VerdictFailed means assertions failed and no remediations remain.
	VerdictFailed Verdict = "SETTLEMENT_FAILED"
	// VerdictDisputed means the emitter formally rejected the receipt.
	VerdictDisputed Verdict = "SETTLEMENT_DISPUTED"
	// VerdictTimeout means the contract window expired before settling.
	VerdictTimeout Verdict = "SETTLEMENT_TIMEOUT"
)

// String implements fmt.Stringer.
func (v Verdict) String() string { return string(v) }

// IsTerminal returns true if the verdict is final (no more remediation possible).
func (v Verdict) IsTerminal() bool {
	switch v {
	case VerdictSettledClean, VerdictRemediated, VerdictFailed, VerdictTimeout:
		return true
	case VerdictDisputed:
		return false // dispute has its own lifecycle
	default:
		return false
	}
}

// IsSuccess returns true if the contract is considered successfully settled.
func (v Verdict) IsSuccess() bool {
	return v == VerdictSettledClean || v == VerdictRemediated
}

// ─────────────────────────────────────────────────────────────────
// Acceptance (emitter's response)
// ─────────────────────────────────────────────────────────────────

// Acceptance is the emitter's formal response to a receipt.
type Acceptance string

const (
	// AcceptanceAccepted means A accepts the receipt and its verdict.
	AcceptanceAccepted Acceptance = "ACCEPTED"
	// AcceptanceDisputed means A rejects the receipt.
	AcceptanceDisputed Acceptance = "DISPUTED"
)

// String implements fmt.Stringer.
func (a Acceptance) String() string { return string(a) }

// IsValid returns true if the acceptance is a known value.
func (a Acceptance) IsValid() bool {
	return a == AcceptanceAccepted || a == AcceptanceDisputed
}

// ─────────────────────────────────────────────────────────────────
// Result (per-assertion outcome)
// ─────────────────────────────────────────────────────────────────

// Result is the outcome of a single assertion evaluation.
type Result string

const (
	// ResultPass means the assertion condition was met.
	ResultPass Result = "PASS"
	// ResultFail means the assertion condition was not met.
	ResultFail Result = "FAIL"
	// ResultSkip means the assertion was not evaluated (e.g., timeout).
	ResultSkip Result = "SKIP"
)

// String implements fmt.Stringer.
func (r Result) String() string { return string(r) }

// ─────────────────────────────────────────────────────────────────
// RemediationResult
// ─────────────────────────────────────────────────────────────────

// RemediationResult is the outcome of a remediation attempt.
type RemediationResult string

const (
	RemediationSuccess RemediationResult = "SUCCESS"
	RemediationFailure RemediationResult = "FAILURE"
)

// String implements fmt.Stringer.
func (r RemediationResult) String() string { return string(r) }

// ─────────────────────────────────────────────────────────────────
// ReceiptStatus (chain state)
// ─────────────────────────────────────────────────────────────────

// ReceiptStatus tracks the receipt's position in the acceptance lifecycle.
type ReceiptStatus string

const (
	ReceiptStatusEmitted   ReceiptStatus = "EMITTED"
	ReceiptStatusAccepted  ReceiptStatus = "ACCEPTED"
	ReceiptStatusDisputed  ReceiptStatus = "DISPUTED"
	ReceiptStatusResolved  ReceiptStatus = "RESOLVED"
	ReceiptStatusStale     ReceiptStatus = "STALE"
)

// IsTerminal returns true if this is a final state.
func (s ReceiptStatus) IsTerminal() bool {
	return s == ReceiptStatusAccepted || s == ReceiptStatusResolved || s == ReceiptStatusStale
}

// String implements fmt.Stringer.
func (s ReceiptStatus) String() string { return string(s) }
