package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/receipt"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

func generateCertWithCNAndSANs(cn string, sans []string, caCertPEM []byte, caKey *ecdsa.PrivateKey) ([]byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     sans,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: marshalECPrivateKey(priv)})
	return certPEM, keyPEM, nil
}

func setupMTLSTestServer(t *testing.T) (*Server, *httptest.Server, []byte, *ecdsa.PrivateKey) {
	t.Helper()
	caCert, caKey, err := generateCA()
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	srvCert, srvKey, err := generateSignedCert("localhost", caCert, caKey, "serverAuth")
	if err != nil {
		t.Fatalf("generate srv cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	os.WriteFile(certFile, srvCert, 0644)
	os.WriteFile(keyFile, srvKey, 0600)
	os.WriteFile(caFile, caCert, 0644)

	srv, err := NewServer(Config{
		AgentID:      "agent-b",
		MeshID:       "gentle-mesh-test",
		Role:         RoleExecutor,
		TLSCertFile:  certFile,
		TLSKeyFile:   keyFile,
		ClientCAFile: caFile,
		ChainDBPath:  filepath.Join(dir, "chain.db"),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)
	tlsCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	return srv, ts, caCert, caKey
}

func createSignedTestEnvelope(t *testing.T, emitterID string, signer signing.Signer) []byte {
	t.Helper()
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "gentle-mesh-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-456789abcdef",
		EmitterAgentID:  emitterID,
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{ID: "check-exit", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "true"}},
		},
		TimeoutSeconds: 30,
		CreatedAt:      time.Now().UTC(),
	}
	if err := envelope.SignEnvelope(env, signer); err != nil {
		t.Fatalf("envelope.SignEnvelope: %v", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return envJSON
}

func TestS1_Envelope_MTLS_MatchingIdentity(t *testing.T) {
	srv, ts, caCert, caKey := setupMTLSTestServer(t)
	aSigner, _ := signing.GenerateSigner("agent-a")
	_ = srv.RegisterAgentKey("agent-a", aSigner.PublicKey())

	clientCert, clientKey, _ := generateSignedCert("agent-a", caCert, caKey, "clientAuth")
	client := newMTLSClient(caCert, clientCert, clientKey)
	envJSON := createSignedTestEnvelope(t, "agent-a", aSigner)

	reqBody, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envJSON})
	resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /envelopes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestS1_Envelope_MTLS_MatchingSAN(t *testing.T) {
	srv, ts, caCert, caKey := setupMTLSTestServer(t)
	aSigner, _ := signing.GenerateSigner("agent-a")
	_ = srv.RegisterAgentKey("agent-a", aSigner.PublicKey())

	clientCert, clientKey, _ := generateCertWithCNAndSANs("workstation-42.internal", []string{"agent-a", "localhost"}, caCert, caKey)
	client := newMTLSClient(caCert, clientCert, clientKey)
	envJSON := createSignedTestEnvelope(t, "agent-a", aSigner)

	reqBody, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envJSON})
	resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /envelopes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestS1_EnvelopeEndpoints_MTLS_MismatchRejectedWith403(t *testing.T) {
	srv, ts, caCert, caKey := setupMTLSTestServer(t)
	victimSigner, _ := signing.GenerateSigner("agent-victim")
	_ = srv.RegisterAgentKey("agent-victim", victimSigner.PublicKey())

	attackerCert, attackerKey, _ := generateSignedCert("agent-attacker", caCert, caKey, "clientAuth")
	client := newMTLSClient(caCert, attackerCert, attackerKey)
	envJSON := createSignedTestEnvelope(t, "agent-victim", victimSigner)

	tests := []struct {
		name     string
		endpoint string
		body     any
	}{
		{"envelopes", "/envelopes", &EnvelopeRequest{EnvelopeJSON: envJSON}},
		{"leases", "/leases", map[string]any{"envelope_json": envJSON}},
		{"settle", "/settle", &SettleRequest{EnvelopeJSON: envJSON, LeaseID: "lease-01"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqBody, _ := json.Marshal(tc.body)
			resp, err := client.Post(ts.URL+tc.endpoint, "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.endpoint, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s status = %d, want %d (Forbidden)", tc.endpoint, resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}

func TestS1_ReceiptEndpoints_MTLS_MismatchRejectedWith403(t *testing.T) {
	srv, ts, caCert, caKey := setupMTLSTestServer(t)
	aSigner, _ := signing.GenerateSigner("agent-a")
	_ = srv.RegisterAgentKey("agent-a", aSigner.PublicKey())

	rec := &receipt.SettlementReceipt{
		ProtocolVersion:   receipt.CurrentProtocolVersion,
		MeshID:            "gentle-mesh-test",
		ReceiptID:         "rec-s1-test",
		ContractID:        "contract-01",
		EnvelopeHash:      "hash-env-01",
		EmitterAgentID:    "agent-a",
		ExecutorAgentID:   "agent-b",
		Verdict:           receipt.VerdictSettledClean,
		SequenceNumber:    1,
		ExecutorSignedAt:  time.Now().UTC(),
		ExecutorSignature: "dummy-sig",
	}
	_ = receipt.SignReceipt(rec, srv.Signer())
	if err := srv.ChainStore().SaveReceipt(context.Background(), rec); err != nil {
		t.Fatalf("SaveReceipt: %v", err)
	}

	attackerCert, attackerKey, _ := generateSignedCert("agent-attacker", caCert, caKey, "clientAuth")
	client := newMTLSClient(caCert, attackerCert, attackerKey)
	recJSON, _ := json.Marshal(rec)

	tests := []struct {
		name     string
		endpoint string
		body     any
	}{
		{
			name:     "accept",
			endpoint: "/accept",
			body: &AcceptRequest{
				ReceiptJSON:         recJSON,
				ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339),
				EmitterSignature:    "attacker-sig",
			},
		},
		{
			name:     "dispute",
			endpoint: "/dispute",
			body: &DisputeRequest{
				ReceiptJSON:         recJSON,
				ExecutorSignedAtRFC: rec.ExecutorSignedAt.Format(time.RFC3339),
				EmitterSignature:    "attacker-sig",
				DisputeReason:       "fraudulent dispute",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqBody, _ := json.Marshal(tc.body)
			resp, err := client.Post(ts.URL+tc.endpoint, "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("POST %s: %v", tc.endpoint, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s status = %d, want %d (Forbidden)", tc.endpoint, resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}

// TestS1_Envelope_TLS_NoClientCert_RejectedWith403 is a regression test:
// when the server is on TLS and receives a request without a client certificate
// (e.g. 1-way TLS or optional client auth), it MUST reject with HTTP 403 Forbidden
// rather than silently bypassing mTLS identity verification.
func TestS1_Envelope_TLS_NoClientCert_RejectedWith403(t *testing.T) {
	caCert, caKey, err := generateCA()
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	srvCert, srvKey, err := generateSignedCert("localhost", caCert, caKey, "serverAuth")
	if err != nil {
		t.Fatalf("generate srv cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	os.WriteFile(certFile, srvCert, 0644)
	os.WriteFile(keyFile, srvKey, 0600)
	os.WriteFile(caFile, caCert, 0644)

	srv, err := NewServer(Config{
		AgentID:      "agent-b",
		MeshID:       "gentle-mesh-test",
		Role:         RoleExecutor,
		TLSCertFile:  certFile,
		TLSKeyFile:   keyFile,
		ClientCAFile: caFile,
		ChainDBPath:  filepath.Join(dir, "chain.db"),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	victimSigner, _ := signing.GenerateSigner("agent-victim")
	_ = srv.RegisterAgentKey("agent-victim", victimSigner.PublicKey())

	tlsCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewUnstartedServer(mux)
	// Server has TLS active, but ClientAuth is NoClientCert (1-way TLS scenario)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.NoClientCert,
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)

	// Client validates server cert via CA, but presents NO client certificate
	client := newTLSClient(caCert)
	envJSON := createSignedTestEnvelope(t, "agent-victim", victimSigner)

	reqBody, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envJSON})
	resp, err := client.Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /envelopes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /envelopes status = %d, want %d (Forbidden: client cert required under TLS)",
			resp.StatusCode, http.StatusForbidden)
	}
}
