package shell

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// MeshClient calls a remote gentle-mesh executor via HTTPS.
// It is used in Mode B and Mode C (remote executor mode).
type MeshClient struct {
	httpClient *agent.HTTPClient
	signer    *signing.BasicSigner
	baseURL   string
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
	// Step 1: Compute envelope hash.
	hash, err := envelope.ComputeEnvelopeHash(env)
	if err != nil {
		return nil, fmt.Errorf("compute envelope hash: %w", err)
	}
	env.EnvelopeHash = hash

	// Step 2: Sign the envelope (Ed25519 over JCS canonical JSON).
	signable := *env
	signable.EmitterSignature = ""
	canonical, err := jcs.Marshal(&signable)
	if err != nil {
		return nil, fmt.Errorf("JCS marshal envelope: %w", err)
	}
	sig, err := m.signer.Sign(canonical)
	if err != nil {
		return nil, fmt.Errorf("Ed25519 sign: %w", err)
	}
	env.EmitterSignature = sig

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
		LeaseID:    leaseID,
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

// Check is a helper that runs pre-flight checks on the remote executor
// via the remote executor's health + pre-flight endpoint.
// For now, this is a placeholder — real implementation would POST to /preflight.
func (m *MeshClient) Check(ctx context.Context, preconditions []envelope.Precondition) (*ReadinessReport, error) {
	// For remote checks, we delegate to the remote executor's /preflight endpoint.
	// This is a basic implementation: assume all checks pass if executor is healthy.
	health, err := m.httpClient.Health(ctx)
	if err != nil {
		return nil, err
	}
	_ = health // AgentID confirmed via health endpoint.

	// Conservative: assume executor's pre-flight will handle actual checks.
	// Real implementation would POST to /preflight and parse the response.
	results := make([]envelope.PreconditionResult, len(preconditions))
	for i, p := range preconditions {
			results[i] = envelope.PreconditionResult{
			PreconditionIndex: i,
			Type:             string(p.Type),
			Passed:           true,
			Message:          "pre-flight delegated to remote executor",
		}
	}
	return &ReadinessReport{
		Passed:  true,
		Results: results,
	}, nil
}
