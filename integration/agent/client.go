// Package agent provides HTTP server and client for RFC-002 agent communication.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// HTTPClient is a simple HTTP client that implements the Client interface.
type HTTPClient struct {
	baseURL string
	client  *http.Client
}

// NewHTTPClient creates a new HTTP client for an agent at the given base URL.
// The client does NOT verify server certificates (for development only).
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// TLSClientOption configures TLS settings for NewHTTPClientTLS.
type TLSClientOption func(*tls.Config)

// WithCACert adds a root CA certificate for server verification.
// Use this in production or when using self-signed certificates.
func WithCACert(caCertPath string) TLSClientOption {
	return func(cfg *tls.Config) {
		caPEM, err := os.ReadFile(caCertPath)
		if err != nil {
			return // Let caller handle the error at dial time
		}
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caPEM) {
			cfg.RootCAs = pool
		}
	}
}

// WithClientCert adds a client certificate for mTLS.
func WithClientCert(certFile, keyFile string) TLSClientOption {
	return func(cfg *tls.Config) {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			cfg.Certificates = []tls.Certificate{cert}
		}
	}
}

// WithInsecureSkipVerify disables server certificate verification.
// WARNING: Use only for local development with self-signed certificates.
// Never use in production.
func WithInsecureSkipVerify() TLSClientOption {
	return func(cfg *tls.Config) {
		cfg.InsecureSkipVerify = true
	}
}

// NewHTTPClientTLS creates an HTTPS client with TLS configuration.
// Pass TLS options like WithCACert, WithClientCert, or WithInsecureSkipVerify.
// In production, always use WithCACert to verify the server certificate.
func NewHTTPClientTLS(baseURL string, opts ...TLSClientOption) (*HTTPClient, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	for _, opt := range opts {
		opt(tlsConfig)
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		Proxy:           http.ProxyFromEnvironment,
	}

	return &HTTPClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}, nil
}

// BaseURL returns the agent's base URL.
func (c *HTTPClient) BaseURL() string {
	return c.baseURL
}

// SubmitEnvelope sends a CognitiveTaskEnvelope to the executor and waits for the lease.
func (c *HTTPClient) SubmitEnvelope(ctx context.Context, envelopeJSON []byte) (*EnvelopeResponse, error) {
	body, err := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envelopeJSON})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/envelopes", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result EnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// ExecuteTask runs a command on the executor.
func (c *HTTPClient) ExecuteTask(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/execute", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result ExecuteResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// Settle triggers settlement on the executor.
func (c *HTTPClient) Settle(ctx context.Context, req *SettleRequest) (*SettleResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/settle", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result SettleResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// GetReceipt retrieves a receipt by ID.
func (c *HTTPClient) GetReceipt(ctx context.Context, receiptID string) (*ReceiptResponse, error) {
	url := fmt.Sprintf("%s/receipts/%s", c.baseURL, receiptID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result ReceiptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// GetChain retrieves the receipt chain for an agent pair.
func (c *HTTPClient) GetChain(ctx context.Context, emitterID, executorID string) (*ChainResponse, error) {
	url := fmt.Sprintf("%s/chain?emitter=%s&executor=%s", c.baseURL, emitterID, executorID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result ChainResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// VerifyReceipt verifies a receipt's signature and chain integrity.
func (c *HTTPClient) VerifyReceipt(ctx context.Context, req *VerifyRequest) (*VerifyResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/verify", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result VerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}


// Dispute sends the emitter's formal dispute of a settled receipt to the executor.
// The emitter must pre-sign the dispute locally using receipt.DisputeReceipt.
func (c *HTTPClient) Dispute(ctx context.Context, req *DisputeRequest) (*DisputeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/dispute", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readErrorWithBody(resp)
	}

	var result DisputeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// Accept sends the emitter's acceptance of a settled receipt to the executor.
// The emitter must pre-sign the acceptance locally using receipt.AcceptReceipt.
func (c *HTTPClient) Accept(ctx context.Context, req *AcceptRequest) (*AcceptResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/accept", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readErrorWithBody(resp)
	}

	var result AcceptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// Health checks agent health.
func (c *HTTPClient) Health(ctx context.Context) (*HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health check failed: status=%d", resp.StatusCode)
	}

	var result HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// Close releases resources (no-op for HTTP client).
func (c *HTTPClient) Close() error {
	return nil
}

// ─────────────────────────────────────────────────────────────────
// Error helpers
// ─────────────────────────────────────────────────────────────────

func (c *HTTPClient) readError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp ErrorResponse
	if json.Unmarshal(body, &errResp) == nil {
		return fmt.Errorf("agent error: %s (code=%d)", errResp.Error, resp.StatusCode)
	}
	return fmt.Errorf("agent error: status=%d body=%s", resp.StatusCode, string(body))
}

// readErrorWithBody is like readError but always includes the response body.
func (c *HTTPClient) readErrorWithBody(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp ErrorResponse
	if json.Unmarshal(body, &errResp) == nil {
		return fmt.Errorf("agent error: %s (code=%d) body=%s", errResp.Error, resp.StatusCode, string(body))
	}
	return fmt.Errorf("agent error: status=%d body=%s", resp.StatusCode, string(body))
}
