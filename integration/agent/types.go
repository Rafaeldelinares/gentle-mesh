// Package agent provides HTTP server and client for RFC-002 agent communication.
// It wraps the core protocol packages (envelope, settlement, receipt, signing)
// with a simple REST API for integration testing.
package agent

import (
	"context"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
)

// Role defines whether this agent is an emitter (A) or executor (B).
type Role string

const (
	RoleEmitter  Role = "emitter"
	RoleExecutor Role = "executor"
)

// Config holds the runtime configuration for an agent instance.
type Config struct {
	AgentID      string
	MeshID       string // Configured mesh network ID for isolation
	Role         Role
	ChainDBPath  string
	WorkspaceDir string
	EvalTimeout  time.Duration
	MaxRemed     int
	Signer       Signer // Ed25519 signing capability

	// TLS configuration. If CertFile and KeyFile are set, the server
	// runs HTTPS instead of HTTP.
	TLSCertFile string
	TLSKeyFile  string
	// ClientCAFile enables mutual TLS (mTLS). If set, the server requires
	// a valid client certificate signed by this CA.
	ClientCAFile string
}

// Signer abstracts Ed25519 signing for test harness flexibility.
type Signer interface {
	Sign(data []byte) (signature string, err error)
	Verify(data, signature []byte) bool
	PublicKeyHex() string
}

// ─────────────────────────────────────────────────────────────────
// HTTP API types
// ─────────────────────────────────────────────────────────────────

// EnvelopeRequest is the POST /envelopes request body.
type EnvelopeRequest struct {
	EnvelopeJSON []byte `json:"envelope_json"`
}

// EnvelopeResponse is the POST /envelopes response body.
type EnvelopeResponse struct {
	Accepted bool   `json:"accepted"`
	LeaseID  string `json:"lease_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// SettleRequest is the POST /settle request body.
type SettleRequest struct {
	EnvelopeJSON []byte `json:"envelope_json"`
	LeaseID      string `json:"lease_id"`
}

// SettleResponse is the POST /settle response body.
type SettleResponse struct {
	ReceiptJSON []byte `json:"receipt_json"`
	ReceiptID   string `json:"receipt_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ReceiptResponse is the GET /receipts/:id response body.
type ReceiptResponse struct {
	ReceiptJSON []byte `json:"receipt_json"`
	ReceiptID   string `json:"receipt_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ChainResponse is the GET /chain response body.
type ChainResponse struct {
	Receipts []ChainReceiptEntry `json:"receipts"`
}

// ChainReceiptEntry is a single entry in the chain response.
type ChainReceiptEntry struct {
	ReceiptID           string `json:"receipt_id"`
	ContractID          string `json:"contract_id"`
	EnvelopeHash        string `json:"envelope_hash"`
	Verdict             string `json:"verdict"`
	ExecutorSignedAt    string `json:"executor_signed_at"`
	ExecutorSignature   string `json:"executor_signature,omitempty"` // needed for chain verification
	PreviousReceiptHash string `json:"previous_receipt_hash,omitempty"`
	EmitterAcceptance   string `json:"emitter_acceptance,omitempty"`
	EmitterSignedAt     string `json:"emitter_signed_at,omitempty"`
}

// VerifyRequest is the POST /verify request body.
type VerifyRequest struct {
	ReceiptJSON []byte `json:"receipt_json"`
}

// VerifyResponse is the POST /verify response body.
type VerifyResponse struct {
	Valid         bool     `json:"valid"`
	Errors        []string `json:"errors,omitempty"`
	ChainValid    bool     `json:"chain_valid"`
	ChainErrors   []string `json:"chain_errors,omitempty"`
	ExecutorSigOK bool     `json:"executor_sig_ok"`
	EmitterSigOK  bool     `json:"emitter_sig_ok"`
}

// AcceptRequest is the POST /accept request body.
// The emitter (A) sends this to the executor (B) to accept a settled receipt.
// A must pre-sign the acceptance locally using AcceptReceipt and send the
// pre-computed emitter_signature to avoid transmitting private keys.
type AcceptRequest struct {
	ReceiptJSON         []byte `json:"receipt_json"`
	ExecutorSignedAtRFC string `json:"executor_signed_at_rfc"` // RFC3339Nano from the receipt
	EmitterSignature    string `json:"emitter_signature"`      // Ed25519 sig from A (pre-computed locally)
}

// AcceptResponse is the POST /accept response body.
type AcceptResponse struct {
	ReceiptJSON []byte `json:"receipt_json"`
	ReceiptID   string `json:"receipt_id,omitempty"`
	Accepted    bool   `json:"accepted"`
	Error       string `json:"error,omitempty"`
}

// DisputeRequest is the POST /dispute request body.
// The emitter (A) sends this to the executor (B) to formally dispute a settled receipt.
// A must pre-sign the dispute locally using receipt.DisputeReceipt and send the
// pre-computed emitter_signature to avoid transmitting private keys.
type DisputeRequest struct {
	ReceiptJSON         []byte `json:"receipt_json"`
	ExecutorSignedAtRFC string `json:"executor_signed_at_rfc"` // RFC3339Nano from the receipt
	EmitterSignature    string `json:"emitter_signature"`      // Ed25519 sig from A (pre-computed locally)
	DisputeReason       string `json:"dispute_reason"`         // Human-readable reason for the dispute
}

// DisputeResponse is the POST /dispute response body.
type DisputeResponse struct {
	ReceiptJSON []byte `json:"receipt_json"`
	ReceiptID   string `json:"receipt_id,omitempty"`
	Disputed    bool   `json:"disputed"`
	Error       string `json:"error,omitempty"`
}

// InjectReceiptRequest is the POST /inject-receipt request body.
// Intended ONLY for testing and security validation.
type InjectReceiptRequest struct {
	ReceiptJSON []byte `json:"receipt_json"`
}

// InjectReceiptResponse is the POST /inject-receipt response body.
type InjectReceiptResponse struct {
	ReceiptID string `json:"receipt_id,omitempty"`
	Injected  bool   `json:"injected"`
	Error     string `json:"error,omitempty"`
}

// VerifyChainRequest is the POST /verify-chain request body.
type VerifyChainRequest struct {
	EmitterID   string `json:"emitter_id"`
	ExecutorID  string `json:"executor_id"`
	ExecutorKey string `json:"executor_key"` // hex-encoded Ed25519 public key
	EmitterKey  string `json:"emitter_key"`  // hex-encoded Ed25519 public key
}

// VerifyChainResult is a single receipt verification result.
type VerifyChainResult struct {
	ReceiptID         string `json:"receipt_id"`
	ExecutorSigValid  bool   `json:"executor_sig_valid"`
	EmitterSigValid   bool   `json:"emitter_sig_valid"`
	PreviousHashValid bool   `json:"previous_hash_valid"`
	Error             string `json:"error,omitempty"`
}

// VerifyChainResponse is the POST /verify-chain response body.
type VerifyChainResponse struct {
	Results  []VerifyChainResult `json:"results"`
	AllValid bool                `json:"all_valid"`
	Error    string              `json:"error,omitempty"`
}

// HealthResponse is the GET /health response body.
type HealthResponse struct {
	Status    string    `json:"status"`
	AgentID   string    `json:"agent_id"`
	Role      string    `json:"role"`
	Timestamp time.Time `json:"timestamp"`
	// PublicKey is the agent's Ed25519 public key as hex (for remote verification).
	PublicKey string `json:"public_key,omitempty"`
}

// ErrorResponse is a generic error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// ExecuteRequest is an optional internal step where B executes the task.
// In a real agent this would be an LLM, but for integration testing
// we simulate execution by running a command.
type ExecuteRequest struct {
	Command    string            `json:"command"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// ExecuteResponse is the result of simulated execution.
type ExecuteResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error,omitempty"`
}

// ─────────────────────────────────────────────────────────────────
// Client interface
// ─────────────────────────────────────────────────────────────────

// Client allows a test harness to interact with an agent.
type Client interface {
	// SubmitEnvelope sends a CognitiveTaskEnvelope to an executor and
	// waits for the lease response.
	SubmitEnvelope(ctx context.Context, envelopeJSON []byte) (*EnvelopeResponse, error)

	// CreateLease requests a lease for an envelope via POST /leases and validates the returned lease.
	CreateLease(ctx context.Context, envelopeJSON []byte) (*envelope.Lease, error)

	// ExecuteTask tells the executor to perform the task (simulated execution).
	ExecuteTask(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, error)

	// Settle triggers the settlement engine on the executor.
	Settle(ctx context.Context, req *SettleRequest) (*SettleResponse, error)

	// GetReceipt retrieves a receipt by ID.
	GetReceipt(ctx context.Context, receiptID string) (*ReceiptResponse, error)

	// GetChain retrieves the full receipt chain for an agent pair.
	GetChain(ctx context.Context, emitterID, executorID string) (*ChainResponse, error)

	// VerifyReceipt validates a receipt and its chain integrity.
	VerifyReceipt(ctx context.Context, req *VerifyRequest) (*VerifyResponse, error)

	// Health checks if the agent is responsive.
	Health(ctx context.Context) (*HealthResponse, error)

	// Close releases resources.
	Close() error
}
