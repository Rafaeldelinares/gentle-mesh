package shell

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// MeshClient calls a remote gentle-mesh executor via HTTPS.
// It is used in Mode B and Mode C (remote executor mode).
type MeshClient struct {
	httpClient *agent.HTTPClient
	signer     *signing.BasicSigner
	baseURL    string
}

// NewMeshClient creates a MeshClient for the given executor URL.
// By default, it performs standard TLS verification against system roots.
// Custom TLS options (such as agent.WithCACert or agent.WithDevInsecureTLS)
// can be provided explicitly via opts.
func NewMeshClient(baseURL string, signer *signing.BasicSigner, opts ...agent.TLSClientOption) (*MeshClient, error) {
	client, err := agent.NewHTTPClientTLS(baseURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	return &MeshClient{
		httpClient: client,
		signer:     signer,
		baseURL:    baseURL,
	}, nil
}

// SubmitEnvelope signs and submits a CognitiveTaskEnvelope to the remote executor.
// It signs the envelope with Ed25519 using JCS canonicalization (RFC 8785).
func (m *MeshClient) SubmitEnvelope(ctx context.Context, env *envelope.CognitiveTaskEnvelope) (*agent.EnvelopeResponse, error) {
	if err := envelope.SignEnvelope(env, m.signer); err != nil {
		return nil, fmt.Errorf("sign envelope: %w", err)
	}

	// Step 3: Serialize and submit.
	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	return m.httpClient.SubmitEnvelope(ctx, envJSON)
}

// Settle instructs the remote executor to evaluate assertions and return a receipt.
func (m *MeshClient) Settle(ctx context.Context, env *envelope.CognitiveTaskEnvelope, leaseID string) (*receipt.SettlementReceipt, error) {
	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	resp, err := m.httpClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      leaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(resp.ReceiptJSON, &rec); err != nil {
		return nil, fmt.Errorf("parse receipt JSON: %w", err)
	}

	return &rec, nil
}

// Execute submits an envelope to the remote executor, waits for lease, settles.
// This is a convenience method that combines SubmitEnvelope + Settle.
func (m *MeshClient) Execute(ctx context.Context, env *envelope.CognitiveTaskEnvelope) (*receipt.SettlementReceipt, error) {
	leaseResp, err := m.SubmitEnvelope(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("submit envelope: %w", err)
	}
	if !leaseResp.Accepted {
		return nil, fmt.Errorf("lease rejected: %s", leaseResp.Error)
	}

	rec, err := m.Settle(ctx, env, leaseResp.LeaseID)
	if err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}

	return rec, nil
}

// Health checks the remote executor's health and returns basic node info.
func (m *MeshClient) Health(ctx context.Context) (*RemoteNodeInfo, error) {
	resp, err := m.httpClient.Health(ctx)
	if err != nil {
		return nil, fmt.Errorf("health check: %w", err)
	}
	return &RemoteNodeInfo{
		AgentID:   resp.AgentID,
		PublicKey: resp.PublicKey,
	}, nil
}

// RemoteNodeInfo describes a remote gentle-mesh executor.
type RemoteNodeInfo struct {
	AgentID   string
	PublicKey string // hex-encoded Ed25519 public key
}

// Check runs pre-flight checks on the remote executor via POST /preflight (RFC-002 A2).
// It delegates evaluation of preconditions to the remote executor and returns
// a ReadinessReport with the actual remote execution results.
func (m *MeshClient) Check(ctx context.Context, preconditions []envelope.Precondition) (*ReadinessReport, error) {
	resp, err := m.httpClient.Preflight(ctx, &agent.PreflightRequest{
		Preconditions: preconditions,
	})
	if err != nil {
		return nil, fmt.Errorf("remote preflight check: %w", err)
	}

	return &ReadinessReport{
		Passed:          resp.Passed,
		Results:         resp.Results,
		RejectionReason: resp.RejectionReason,
	}, nil
}
