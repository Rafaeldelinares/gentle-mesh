package http_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/pki"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
	meshhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/store"
)

// setupMTLSTestEnvironment initializes CA, server certs, and test database with token and cert stores.
func setupMTLSTestEnvironment(t *testing.T) (*pki.MeshCA, string, *store.SQLiteTokenStore, *store.SQLiteCertStore, *sql.DB) {
	t.Helper()
	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test CA", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	dbPath := filepath.Join(tlsDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.InitTokenSchema(db); err != nil {
		t.Fatalf("failed to init token schema: %v", err)
	}
	if err := store.InitCertSchema(db); err != nil {
		t.Fatalf("failed to init cert schema: %v", err)
	}

	tokenStore := store.NewSQLiteTokenStore(db)
	certStore := store.NewSQLiteCertStore(db)

	return ca, tlsDir, tokenStore, certStore, db
}

func enrollTestNode(t *testing.T, srvURL string, ca *pki.MeshCA, tokenStore *store.SQLiteTokenStore, nodeID string) tls.Certificate {
	t.Helper()
	// Create an enrollment token for nodeID
	tok := "enroll-token-" + nodeID
	tokenExp := time.Now().Add(1 * time.Hour)
	if err := tokenStore.CreateToken(context.Background(), &store.TokenRecord{
		Token:     tok,
		ExpiresAt: &tokenExp,
		MaxUses:   1,
	}); err != nil {
		t.Fatalf("failed to create enrollment token: %v", err)
	}

	csrResult, err := pki.GenerateCSR(nodeID)
	if err != nil {
		t.Fatalf("failed to generate node CSR: %v", err)
	}

	enrollPayload, _ := json.Marshal(map[string]string{
		"token":   tok,
		"csr":     csrResult.CSRPEM,
		"node_id": nodeID,
	})

	client := createClient(t, ca, nil)
	resp, err := client.Post(srvURL+"/v1/certs/enroll", "application/json", bytes.NewReader(enrollPayload))
	if err != nil {
		t.Fatalf("POST /v1/certs/enroll failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("enrollment returned %d: %s", resp.StatusCode, string(body))
	}

	var enrollResp struct {
		CertPEM string `json:"cert_pem"`
		NodeID  string `json:"node_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enrollResp); err != nil {
		t.Fatalf("failed to decode enroll response: %v", err)
	}

	nodeTLSCert, err := tls.X509KeyPair([]byte(enrollResp.CertPEM), []byte(csrResult.PrivateKeyPEM))
	if err != nil {
		t.Fatalf("failed to parse enrolled keypair: %v", err)
	}

	return nodeTLSCert
}

func TestServer_EnrollPersistsCertInCertStore(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	_, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	nodeID := "node-persist-check"
	_ = enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	rec, err := certStore.GetCert(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("GetCert failed: %v", err)
	}
	if rec == nil {
		t.Fatalf("expected cert record in cert store for %q, got nil", nodeID)
	}
	if rec.NodeID != nodeID {
		t.Errorf("expected node_id %q, got %q", nodeID, rec.NodeID)
	}
	if rec.Serial == "" {
		t.Errorf("expected non-empty serial number in cert record")
	}
	if rec.Revoked {
		t.Errorf("expected newly issued cert to have Revoked=false")
	}
}

func TestServer_RevokedCertRejectedAtTLSHandshake(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	srv, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	nodeID := "node-handshake-reject"
	nodeCert := enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	// Step 1: Pre-revocation connection succeeds
	validClient := createClient(t, ca, &nodeCert)
	joinBody, _ := json.Marshal(protocol.NodeJoinRequest{
		NodeID:   nodeID,
		Endpoint: "http://127.0.0.1:9999",
	})
	resp, err := validClient.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("pre-revocation join failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on initial join, got %d", resp.StatusCode)
	}

	// Step 2: Revoke the certificate
	if err := srv.RevokeCert(context.Background(), nodeID); err != nil {
		t.Fatalf("failed to revoke cert for %s: %v", nodeID, err)
	}

	// Step 3: New TLS connection must fail during TLS handshake
	freshClient := createClient(t, ca, &nodeCert)
	_, err = freshClient.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(joinBody))
	if err == nil {
		t.Fatal("expected TLS handshake to fail for revoked certificate, but request succeeded")
	}

	// Error should indicate certificate revocation or TLS handshake failure
	errMsg := err.Error()
	if !strings.Contains(errMsg, "revoked") && !strings.Contains(errMsg, "tls:") && !strings.Contains(errMsg, "handshake") {
		t.Errorf("expected TLS handshake error mentioning revocation or TLS failure, got: %v", err)
	}
}

func TestServer_RevokedCertRejectedOnKeepAliveConnection(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	srv, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	nodeID := "node-keepalive-reject"
	nodeCert := enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	// Keep-alive HTTP client
	client := createClient(t, ca, &nodeCert)
	joinBody, _ := json.Marshal(protocol.NodeJoinRequest{
		NodeID:   nodeID,
		Endpoint: "http://127.0.0.1:9999",
	})

	// Request 1 on keep-alive connection
	resp1, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on request 1, got %d", resp1.StatusCode)
	}

	// Revoke certificate while TCP connection is pooled / alive
	if err := srv.RevokeCert(context.Background(), nodeID); err != nil {
		t.Fatalf("failed to revoke cert: %v", err)
	}

	// Request 2 on the SAME pooled keep-alive connection
	heartbeatBody, _ := json.Marshal(protocol.NodeHeartbeatRequest{
		NodeID:    nodeID,
		Timestamp: time.Now().Unix(),
	})
	resp2, err := client.Post(srvURL+"/v1/mesh/heartbeat", "application/json", bytes.NewReader(heartbeatBody))
	if err != nil {
		// If the TLS layer closed the connection immediately, that's also a valid rejection
		return
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp2.Body)
		t.Errorf("expected HTTP 403 Forbidden on keep-alive request after revocation, got %d: %s", resp2.StatusCode, string(body))
	}

	if !resp2.Close && !strings.EqualFold(resp2.Header.Get("Connection"), "close") {
		t.Errorf("expected connection to be closed by server (resp.Close=true or Connection: close header), got resp.Close=%v, header=%q", resp2.Close, resp2.Header.Get("Connection"))
	}
}

func TestServer_ValidCertAllowedAtHandshakeAndKeepAlive(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	_, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	nodeID := "node-valid-check"
	nodeCert := enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	client := createClient(t, ca, &nodeCert)
	joinBody, _ := json.Marshal(protocol.NodeJoinRequest{
		NodeID:   nodeID,
		Endpoint: "http://127.0.0.1:9999",
	})

	// Handshake + request 1
	resp1, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on request 1, got %d", resp1.StatusCode)
	}

	// Keep-alive request 2
	heartbeatBody, _ := json.Marshal(protocol.NodeHeartbeatRequest{
		NodeID:    nodeID,
		Timestamp: time.Now().Unix(),
	})
	resp2, err := client.Post(srvURL+"/v1/mesh/heartbeat", "application/json", bytes.NewReader(heartbeatBody))
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on request 2, got %d", resp2.StatusCode)
	}
}

type mockRevocationChecker struct {
	revokedSerials map[string]bool
}

func (m *mockRevocationChecker) IsCertRevoked(ctx context.Context, serial string) (bool, error) {
	return m.revokedSerials[serial], nil
}

func TestMTLSMiddleware_RejectsRevokedCertOnKeepAlive(t *testing.T) {
	serial := "9876543210123456789"
	bigSerial, _ := new(big.Int).SetString(serial, 10)
	cert := &x509.Certificate{
		SerialNumber: bigSerial,
		Subject: pkix.Name{
			CommonName: "worker-keepalive",
		},
	}

	mockChecker := &mockRevocationChecker{
		revokedSerials: make(map[string]bool),
	}

	handlerCalled := false
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	middleware := meshhttp.MTLSMiddlewareWithRevocation(mockChecker, dummyHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/mesh/heartbeat", nil)
	req.TLS = &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}

	// 1. Valid cert passes through
	w1 := httptest.NewRecorder()
	middleware.ServeHTTP(w1, req)
	if !handlerCalled || w1.Code != http.StatusOK {
		t.Fatalf("expected handler called with 200, got code=%d, called=%v", w1.Code, handlerCalled)
	}

	// 2. Mark serial as revoked in atomic cache/checker
	mockChecker.revokedSerials[serial] = true
	handlerCalled = false

	// 3. Keep-alive request on the SAME TLS connection state fails with 403 and Connection: close
	w2 := httptest.NewRecorder()
	middleware.ServeHTTP(w2, req)
	if handlerCalled {
		t.Error("handler was called for revoked certificate")
	}
	if w2.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", w2.Code)
	}
	if conn := w2.Header().Get("Connection"); !strings.EqualFold(conn, "close") {
		t.Errorf("expected 'Connection: close', got %q", conn)
	}
	if !strings.Contains(w2.Body.String(), "certificate revoked") {
		t.Errorf("expected 'certificate revoked' error message, got: %s", w2.Body.String())
	}
}

func TestServer_RevocationCacheWarmupAtStartup(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	// Pre-populate certStore with a revoked cert
	revokedNodeID := "pre-revoked-node"
	revokedSerial := "112233445566778899"
	err := certStore.IssueCert(context.Background(), &store.CertRecord{
		NodeID:     revokedNodeID,
		CommonName: revokedNodeID,
		Serial:     revokedSerial,
		CertPEM:    "dummy-pem",
		IssuedAt:   time.Now().Add(-1 * time.Hour),
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("failed to issue cert: %v", err)
	}
	if err := certStore.RevokeCert(context.Background(), revokedNodeID); err != nil {
		t.Fatalf("failed to revoke cert in store: %v", err)
	}

	// Start server; it should warm up its revocation cache from CertStore
	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	srv, _ := startTestServer(t, cfg)

	// Verify server detects revocation immediately
	revoked, err := srv.IsCertRevoked(context.Background(), revokedSerial)
	if err != nil {
		t.Fatalf("IsCertRevoked failed: %v", err)
	}
	if !revoked {
		t.Errorf("expected serial %s to be recognized as revoked on server startup", revokedSerial)
	}
}

func TestServer_RevokedCertRejectedOnTLSResumption(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)

	cfg := meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}

	srv, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	// Phase 1: Prove session resumption actually works for an unrevoked node certificate
	validNodeID := "node-resume-valid"
	validNodeCert := enrollTestNode(t, srvURL, ca, tokenStore, validNodeID)

	validTLSConfig := &tls.Config{
		RootCAs:            meshCAPool(t, ca),
		ServerName:         "localhost",
		Certificates:       []tls.Certificate{validNodeCert},
		ClientSessionCache: tls.NewLRUClientSessionCache(32),
	}

	// First connection: initial full handshake to acquire session ticket
	conn1, err := tls.Dial("tcp", addr, validTLSConfig)
	if err != nil {
		t.Fatalf("valid node first TLS dial failed: %v", err)
	}
	// Send request and read response to ensure post-handshake session ticket is received
	if _, err := conn1.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("failed to write to conn1: %v", err)
	}
	buf := make([]byte, 1024)
	_, _ = conn1.Read(buf)
	_ = conn1.Close()

	// Second connection: should successfully resume session
	conn2, err := tls.Dial("tcp", addr, validTLSConfig)
	if err != nil {
		t.Fatalf("valid node second TLS dial failed: %v", err)
	}
	if !conn2.ConnectionState().DidResume {
		t.Errorf("expected second connection to resume TLS session, but DidResume=false")
	}
	_ = conn2.Close()

	// Phase 2: Verify that a revoked certificate attempting TLS session resumption is rejected by VerifyConnection
	revokedNodeID := "node-resume-revoked"
	revokedNodeCert := enrollTestNode(t, srvURL, ca, tokenStore, revokedNodeID)

	revokedTLSConfig := &tls.Config{
		RootCAs:            meshCAPool(t, ca),
		ServerName:         "localhost",
		Certificates:       []tls.Certificate{revokedNodeCert},
		ClientSessionCache: tls.NewLRUClientSessionCache(32),
	}

	// First connection: full handshake to acquire session ticket while still valid
	rConn1, err := tls.Dial("tcp", addr, revokedTLSConfig)
	if err != nil {
		t.Fatalf("revoked node first TLS dial failed: %v", err)
	}
	if _, err := rConn1.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("failed to write to rConn1: %v", err)
	}
	_, _ = rConn1.Read(buf)
	_ = rConn1.Close()

	// Revoke the certificate
	if err := srv.RevokeCert(context.Background(), revokedNodeID); err != nil {
		t.Fatalf("failed to revoke cert for %s: %v", revokedNodeID, err)
	}

	// Second connection: attempts session resumption with cached session ticket.
	// VerifyConnection must intercept the resumed session and abort the handshake with alertBadCertificate.
	rConn2, err := tls.Dial("tcp", addr, revokedTLSConfig)
	if err == nil {
		// In TLS 1.3 1-RTT handshake, the server sends alertBadCertificate upon VerifyConnection failure.
		// Any read or write on the connection will immediately fail with the alert received from the server.
		_, writeErr := rConn2.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\n\r\n"))
		_, readErr := rConn2.Read(buf)
		_ = rConn2.Close()
		if writeErr == nil && readErr == nil {
			t.Fatal("expected connection to fail on session resumption for revoked certificate, but read and write succeeded")
		}
		err = readErr
		if err == nil {
			err = writeErr
		}
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "bad certificate") && !strings.Contains(errMsg, "revoked") && !strings.Contains(errMsg, "tls:") && !strings.Contains(errMsg, "handshake") && !strings.Contains(errMsg, "EOF") && !strings.Contains(errMsg, "closed") {
		t.Errorf("expected TLS error indicating bad certificate or closed connection, got: %v", err)
	}
}
