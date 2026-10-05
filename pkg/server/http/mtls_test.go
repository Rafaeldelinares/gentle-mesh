package http_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

// getFreePort allocates a free loopback TCP port and immediately releases it.
func getFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startTestServer starts a Server on a free loopback port using srv.Start() and waits for readiness.
func startTestServer(t *testing.T, cfg meshhttp.ServerConfig) (*meshhttp.Server, string) {
	t.Helper()
	addr := getFreePort(t)
	cfg.Addr = addr

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Wait for readiness with short dial-retry loop
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("server exited unexpectedly during startup: %v", err)
		default:
		}
		dialer := &net.Dialer{Timeout: 50 * time.Millisecond}
		tlsConfig := &tls.Config{InsecureSkipVerify: true}
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !ready {
		t.Fatalf("timed out waiting for server to be ready on %s", addr)
	}

	return srv, addr
}

// createClient creates an HTTP client trusting the mesh CA with ServerName "localhost" and optional client certificate.
func createClient(t *testing.T, ca *pki.MeshCA, clientCert *tls.Certificate) *http.Client {
	t.Helper()
	caPool := x509.NewCertPool()
	caPEM, err := pki.CertificateToPEM(ca.Cert)
	if err != nil {
		t.Fatalf("failed to encode CA cert to PEM: %v", err)
	}
	if !caPool.AppendCertsFromPEM([]byte(caPEM)) {
		t.Fatal("failed to append CA certificate to pool")
	}

	tlsConfig := &tls.Config{
		RootCAs:    caPool,
		ServerName: "localhost",
	}
	if clientCert != nil {
		tlsConfig.Certificates = []tls.Certificate{*clientCert}
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 5 * time.Second,
	}
}

// nodeCertToTLSCert converts a pki.NodeCert into a crypto/tls.Certificate.
func nodeCertToTLSCert(t *testing.T, nc *pki.NodeCert) tls.Certificate {
	t.Helper()
	keyBytes, err := x509.MarshalECPrivateKey(nc.Key)
	if err != nil {
		t.Fatalf("failed to marshal node private key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})
	certPEM, err := pki.CertificateToPEM(nc.Cert)
	if err != nil {
		t.Fatalf("failed to encode node cert to PEM: %v", err)
	}
	tlsCert, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	if err != nil {
		t.Fatalf("failed to create tls.Certificate from node cert: %v", err)
	}
	return tlsCert
}

func TestServer_MTLSRejectsClientWithoutCertificate(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
	}

	_, addr := startTestServer(t, cfg)

	client := createClient(t, ca, nil)

	resp, err := client.Get("https://" + addr + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET /v1/mesh/nodes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "client certificate required" {
		t.Fatalf("expected error %q, got %q", "client certificate required", body["error"])
	}
}

func TestServer_MTLSRejectsCertificateFromOtherCA(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
	}

	_, addr := startTestServer(t, cfg)

	otherDir := t.TempDir()
	otherCA, _, err := pki.EnsureMeshTLS(otherDir, "Other Mesh", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init other TLS: %v", err)
	}

	otherNodeCert, _, err := otherCA.GenerateNodeCert("foreign-node", 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate foreign node cert: %v", err)
	}

	foreignTLSCert := nodeCertToTLSCert(t, otherNodeCert)
	client := createClient(t, ca, &foreignTLSCert)

	resp, err := client.Get("https://" + addr + "/v1/mesh/nodes")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("expected request with certificate from different CA to fail, got status 200")
		}
	}
}

func TestServer_MTLSAcceptsValidMeshCertificate(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
	}

	_, addr := startTestServer(t, cfg)

	nodeCert, _, err := ca.GenerateNodeCert("valid-node", 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate node cert: %v", err)
	}

	validTLSCert := nodeCertToTLSCert(t, nodeCert)
	client := createClient(t, ca, &validTLSCert)

	resp, err := client.Get("https://" + addr + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET /v1/mesh/nodes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}
}

func TestServer_MTLSBootstrapRoutesWithoutCertificate(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	if err := store.InitTokenSchema(db); err != nil {
		t.Fatalf("failed to init token schema: %v", err)
	}
	tokenStore := store.NewSQLiteTokenStore(db)

	tokenExp := time.Now().Add(24 * time.Hour)
	tokenRecord := &store.TokenRecord{
		Token:     "test-enrollment-token-bootstrap",
		MaxUses:   1,
		ExpiresAt: &tokenExp,
	}
	if err := tokenStore.CreateToken(context.Background(), tokenRecord); err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
		TokenStore:       tokenStore,
	}

	_, addr := startTestServer(t, cfg)

	client := createClient(t, ca, nil)

	// 1. GET /healthz succeeds without client cert
	respHealthz, err := client.Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	respHealthz.Body.Close()
	if respHealthz.StatusCode != http.StatusOK {
		t.Fatalf("expected GET /healthz status 200, got %d", respHealthz.StatusCode)
	}

	// 2. GET /v1/mesh/ca succeeds without client cert
	respCA, err := client.Get("https://" + addr + "/v1/mesh/ca")
	if err != nil {
		t.Fatalf("GET /v1/mesh/ca failed: %v", err)
	}
	caBody, _ := io.ReadAll(respCA.Body)
	respCA.Body.Close()
	if respCA.StatusCode != http.StatusOK {
		t.Fatalf("expected GET /v1/mesh/ca status 200, got %d", respCA.StatusCode)
	}
	if !strings.Contains(string(caBody), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("expected CA PEM certificate in body, got: %s", string(caBody))
	}

	// 3. POST /v1/certs/enroll succeeds without client cert
	csrResult, err := pki.GenerateCSR("node-bootstrap-test")
	if err != nil {
		t.Fatalf("failed to generate CSR: %v", err)
	}

	enrollPayload, _ := json.Marshal(map[string]string{
		"token":   "test-enrollment-token-bootstrap",
		"csr":     csrResult.CSRPEM,
		"node_id": "node-bootstrap-test",
	})
	respEnroll, err := client.Post("https://"+addr+"/v1/certs/enroll", "application/json", bytes.NewReader(enrollPayload))
	if err != nil {
		t.Fatalf("POST /v1/certs/enroll failed: %v", err)
	}
	defer respEnroll.Body.Close()

	if respEnroll.StatusCode != http.StatusOK {
		t.Fatalf("expected POST /v1/certs/enroll status 200, got %d", respEnroll.StatusCode)
	}
}

func TestServer_MTLSFullEnrollmentFlow(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	if err := store.InitTokenSchema(db); err != nil {
		t.Fatalf("failed to init token schema: %v", err)
	}
	tokenStore := store.NewSQLiteTokenStore(db)

	tokenExp := time.Now().Add(24 * time.Hour)
	tokenRecord := &store.TokenRecord{
		Token:     "flow-enrollment-token-456",
		MaxUses:   1,
		ExpiresAt: &tokenExp,
	}
	if err := tokenStore.CreateToken(context.Background(), tokenRecord); err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
		TokenStore:       tokenStore,
	}

	_, addr := startTestServer(t, cfg)

	// Step 1: Client without cert enrolls via CSR
	bootstrapClient := createClient(t, ca, nil)

	nodeID := "node-enrolled-client"
	csrResult, err := pki.GenerateCSR(nodeID)
	if err != nil {
		t.Fatalf("failed to generate CSR: %v", err)
	}

	enrollPayload, _ := json.Marshal(map[string]string{
		"token":   "flow-enrollment-token-456",
		"csr":     csrResult.CSRPEM,
		"node_id": nodeID,
	})
	respEnroll, err := bootstrapClient.Post("https://"+addr+"/v1/certs/enroll", "application/json", bytes.NewReader(enrollPayload))
	if err != nil {
		t.Fatalf("POST /v1/certs/enroll failed: %v", err)
	}
	defer respEnroll.Body.Close()

	if respEnroll.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 from enroll, got %d", respEnroll.StatusCode)
	}

	var enrollResp meshhttp.EnrollmentResponse
	if err := json.NewDecoder(respEnroll.Body).Decode(&enrollResp); err != nil {
		t.Fatalf("failed to decode enroll response: %v", err)
	}

	if enrollResp.CertPEM == "" {
		t.Fatal("expected cert_pem in enroll response")
	}

	// Step 2: Build client cert from cert_pem + CSR private key
	clientTLSCert, err := tls.X509KeyPair([]byte(enrollResp.CertPEM), []byte(csrResult.PrivateKeyPEM))
	if err != nil {
		t.Fatalf("failed to build tls.Certificate from enrolled cert and private key: %v", err)
	}

	// Step 3: GET /v1/mesh/nodes with enrolled client cert -> 200
	authenticatedClient := createClient(t, ca, &clientTLSCert)

	respNodes, err := authenticatedClient.Get("https://" + addr + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET /v1/mesh/nodes with enrolled cert failed: %v", err)
	}
	defer respNodes.Body.Close()

	if respNodes.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", respNodes.StatusCode)
	}
}

func TestServer_RequireMTLSWithoutCAReturnsStartError(t *testing.T) {
	tlsDir := t.TempDir()
	_, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	addr := getFreePort(t)

	cfg := meshhttp.ServerConfig{
		Addr:             addr,
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           nil, // Missing MeshCA
		RequireMTLS:      true,
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	select {
	case startErr := <-errCh:
		if startErr == nil {
			t.Fatal("expected non-nil error from Start() when RequireMTLS is true and MeshCA is nil, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		_ = srv.Shutdown(context.Background())
		t.Fatal("expected Start() to return error immediately when RequireMTLS is true and MeshCA is nil, but it blocked or started")
	}
}

func TestServer_MTLSDoesNotAffectNonMTLSServer(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      false,
	}

	_, addr := startTestServer(t, cfg)

	client := createClient(t, ca, nil)

	resp, err := client.Get("https://" + addr + "/v1/mesh/nodes")
	if err != nil {
		t.Fatalf("GET /v1/mesh/nodes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServer_JoinRequiresBearerWhenTokenConfigured(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	token := "mesh-secret-bearer-token"
	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      false,
		BearerToken:      token,
	}

	_, addr := startTestServer(t, cfg)

	client := createClient(t, ca, nil)

	joinReq := protocol.NodeJoinRequest{
		NodeID:   "node-join-bearer-test",
		Endpoint: "http://127.0.0.1:9090",
		Hardware: protocol.NodeHardware{
			CPUs: 4,
			OS:   "linux",
		},
		MaxConcurrency: 2,
	}
	joinBody, _ := json.Marshal(joinReq)

	// 1. POST /v1/mesh/join without Authorization -> 401
	reqWithoutAuth, err := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/mesh/join", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	reqWithoutAuth.Header.Set("Content-Type", "application/json")

	respNoAuth, err := client.Do(reqWithoutAuth)
	if err != nil {
		t.Fatalf("POST /v1/mesh/join without auth failed: %v", err)
	}
	defer respNoAuth.Body.Close()

	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized without bearer, got %d", respNoAuth.StatusCode)
	}

	// 2. POST /v1/mesh/join with correct Bearer -> 200
	reqWithAuth, err := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/mesh/join", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	reqWithAuth.Header.Set("Content-Type", "application/json")
	reqWithAuth.Header.Set("Authorization", "Bearer "+token)

	respWithAuth, err := client.Do(reqWithAuth)
	if err != nil {
		t.Fatalf("POST /v1/mesh/join with auth failed: %v", err)
	}
	defer respWithAuth.Body.Close()

	if respWithAuth.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK with valid bearer, got %d", respWithAuth.StatusCode)
	}
}

func TestServer_MTLSJoinBindsCertificateIdentity(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		MeshCAPemFile:    filepath.Join(tlsDir, pki.CAPemFile),
		RequireMTLS:      true,
	}

	_, addr := startTestServer(t, cfg)

	nodeCert, _, err := ca.GenerateNodeCert("bound-node", 24*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate node cert: %v", err)
	}
	tlsCert := nodeCertToTLSCert(t, nodeCert)
	client := createClient(t, ca, &tlsCert)

	join := func(nodeID string) *http.Response {
		body, err := json.Marshal(protocol.NodeJoinRequest{
			NodeID:         nodeID,
			Endpoint:       "http://127.0.0.1:9999",
			MaxConcurrency: 1,
		})
		if err != nil {
			t.Fatalf("failed to marshal join request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, "https://"+addr+"/v1/mesh/join", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create join request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/mesh/join failed: %v", err)
		}
		return resp
	}

	// A certificate whose CN matches node_id is accepted.
	respMatch := join("bound-node")
	defer respMatch.Body.Close()
	if respMatch.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for matching identity, got %d", respMatch.StatusCode)
	}

	// A certificate whose CN differs from node_id is rejected.
	respMismatch := join("other-node")
	defer respMismatch.Body.Close()
	if respMismatch.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status 403 for mismatched identity, got %d", respMismatch.StatusCode)
	}
	var errBody map[string]string
	if err := json.NewDecoder(respMismatch.Body).Decode(&errBody); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if errBody["error"] != "client certificate identity does not match node_id" {
		t.Fatalf("unexpected error body: %q", errBody["error"])
	}
}
