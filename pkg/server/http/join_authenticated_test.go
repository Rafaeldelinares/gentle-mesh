package http_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/joinlog"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/pki"
	serverhttp "github.com/Rafaeldelinares/gentle-mesh/pkg/server/http"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/registry"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/server/store"
	_ "modernc.org/sqlite"
)

func setupMTLSJoinServer(t *testing.T) (*serverhttp.Server, string, *pki.MeshCA, *store.SQLiteTokenStore, *joinlog.Log, *joinlog.Projector) {
	t.Helper()
	tlsDir := t.TempDir()

	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test CA", "testing", []string{"127.0.0.1", "localhost"}, true)
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

	jlPath := filepath.Join(tlsDir, "test_join.db")
	jl, err := joinlog.New(jlPath)
	if err != nil {
		t.Fatalf("failed to init join log: %v", err)
	}

	jpPath := filepath.Join(tlsDir, "test_proj.db")
	jp, err := joinlog.NewProjector(jpPath)
	if err != nil {
		t.Fatalf("failed to init projector: %v", err)
	}

	reg := registry.NewRegistry(10 * time.Minute)

	cfg := serverhttp.ServerConfig{
		TLSEnabled:    true,
		TLSCertFile:   filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:    filepath.Join(tlsDir, pki.CertKeyFile),
		RequireMTLS:   true,
		MeshCA:        ca,
		MeshCAPemFile: filepath.Join(tlsDir, pki.CAPemFile),
		TokenStore:    tokenStore,
		CertStore:     certStore,
		Registry:      reg,
		JoinLog:       jl,
		JoinProjector: jp,
	}

	srv, addr := startTestServer(t, cfg)
	srvURL := "https://" + addr

	t.Cleanup(func() {
		_ = jl.Close()
		_ = jp.Close()
	})

	return srv, srvURL, ca, tokenStore, jl, jp
}

func enrollNode(t *testing.T, srvURL string, ca *pki.MeshCA, tokenStore *store.SQLiteTokenStore, nodeID string) tls.Certificate {
	t.Helper()
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

	// Enrollment is unauthenticated mTLS
	client := clientWithCertDirect(ca, nil)
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

	cert, err := tls.X509KeyPair([]byte(enrollResp.CertPEM), []byte(csrResult.PrivateKeyPEM))
	if err != nil {
		t.Fatalf("failed to create node keypair: %v", err)
	}
	return cert
}

func clientWithCertDirect(ca *pki.MeshCA, cert *tls.Certificate) *http.Client {
	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(ca.Cert)

	tlsConfig := &tls.Config{
		RootCAs:    caCertPool,
		MinVersion: tls.VersionTLS13,
	}
	if cert != nil {
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 5 * time.Second,
	}
}

// TestNodeJoin_AnonymousRejected verifies that a client without any client certificate
// is rejected at the TLS handshake or HTTP level when RequireMTLS is true.
func TestNodeJoin_AnonymousRejected(t *testing.T) {
	_, srvURL, ca, _, jl, _ := setupMTLSJoinServer(t)

	// Client without certificate
	client := clientWithCertDirect(ca, nil)

	payload := map[string]any{
		"node_id":          "node-anonymous",
		"endpoint":         "https://10.0.0.99:9099",
		"role":             "worker",
		"agent_public_key": hex.EncodeToString(make([]byte, 32)),
	}
	body, _ := json.Marshal(payload)

	resp, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(body))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			t.Fatalf("expected non-200 for anonymous request, got %d", resp.StatusCode)
		}
	}
	// An error at TLS handshake (tls: certificate required) is also a valid rejection

	events, err := jl.Events(context.Background())
	if err != nil {
		t.Fatalf("failed to read joinlog: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events in joinlog after anonymous attempt, got %d", len(events))
	}
}

// TestNodeJoin_MTLS_SpoofingRejected verifies that a client with a valid mTLS certificate
// for "node-attacker" CANNOT register or emit a join event claiming to be "node-victim".
func TestNodeJoin_MTLS_SpoofingRejected(t *testing.T) {
	_, srvURL, ca, tokenStore, jl, _ := setupMTLSJoinServer(t)

	attackerCert := enrollNode(t, srvURL, ca, tokenStore, "node-attacker")
	client := clientWithCertDirect(ca, &attackerCert)

	victimPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	victimPubHex := hex.EncodeToString(victimPub)

	// Attacker tries to impersonate "node-victim"
	payload := map[string]any{
		"node_id":          "node-victim",
		"endpoint":         "https://10.0.0.99:9099",
		"role":             "worker",
		"agent_public_key": victimPubHex,
	}
	body, _ := json.Marshal(payload)

	resp, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected network error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden on node_id spoofing, got %d", resp.StatusCode)
	}

	// Verify that JoinLog remains pristine: no event was appended
	events, err := jl.Events(context.Background())
	if err != nil {
		t.Fatalf("failed to read joinlog: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events in joinlog after spoofing attempt, got %d", len(events))
	}
}

// TestNodeJoin_MTLS_LegitimateRegistrationEmitsVerifiedJoinLogEvent tests the complete happy path:
// 1. Client connects with valid mTLS cert matching payload.node_id.
// 2. Client sends valid Ed25519 agent_public_key.
// 3. Coordinator accepts (HTTP 200/201).
// 4. Coordinator appends a verifiable node.joined event to the JoinLog with agent_public_key.
// 5. The projector projects the node with its agent_public_key into SQLite.
func TestNodeJoin_MTLS_LegitimateRegistrationEmitsVerifiedJoinLogEvent(t *testing.T) {
	_, srvURL, ca, tokenStore, jl, jp := setupMTLSJoinServer(t)

	nodeID := "node-legit-worker"
	nodeCert := enrollNode(t, srvURL, ca, tokenStore, nodeID)
	client := clientWithCertDirect(ca, &nodeCert)

	agentPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	agentPubHex := hex.EncodeToString(agentPub)

	payload := map[string]any{
		"node_id":          nodeID,
		"endpoint":         "https://10.0.0.50:9099",
		"role":             "worker",
		"agent_public_key": agentPubHex,
		"tags":             []string{"gpu", "go"},
	}
	body, _ := json.Marshal(payload)

	resp, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected network error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200/201 on valid join, got %d: %s", resp.StatusCode, string(body))
	}

	// 1. Verify event emitted in JoinLog
	events, err := jl.Events(context.Background())
	if err != nil {
		t.Fatalf("failed to read joinlog: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event in joinlog, got %d", len(events))
	}

	evt := events[0]
	if evt.Type != joinlog.EventTypeNodeJoined {
		t.Errorf("expected event type %q, got %q", joinlog.EventTypeNodeJoined, evt.Type)
	}

	var joinedPayload joinlog.NodeJoinedPayload
	if err := json.Unmarshal(evt.Payload, &joinedPayload); err != nil {
		t.Fatalf("failed to unmarshal node.joined payload: %v", err)
	}

	if joinedPayload.NodeID != nodeID {
		t.Errorf("expected node_id %q in payload, got %q", nodeID, joinedPayload.NodeID)
	}
	if joinedPayload.AgentPublicKey != agentPubHex {
		t.Errorf("expected agent_public_key %q, got %q", agentPubHex, joinedPayload.AgentPublicKey)
	}

	// 2. Verify projection state
	snap, err := jp.CreateSnapshot(context.Background(), jl)
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	nodeProj, found := snap.State.Nodes[nodeID]
	if !found {
		t.Fatalf("node %s not found in projected snapshot", nodeID)
	}
	if nodeProj.AgentPublicKey != agentPubHex {
		t.Errorf("expected projected agent_public_key %q, got %q", agentPubHex, nodeProj.AgentPublicKey)
	}
}

// TestNodeJoin_InvalidAgentPublicKeyRejected verifies that a join request with an
// invalid Ed25519 public key (e.g. wrong size, not hex) is rejected and does not emit an event.
func TestNodeJoin_InvalidAgentPublicKeyRejected(t *testing.T) {
	_, srvURL, ca, tokenStore, jl, _ := setupMTLSJoinServer(t)

	nodeID := "node-malformed-key"
	nodeCert := enrollNode(t, srvURL, ca, tokenStore, nodeID)
	client := clientWithCertDirect(ca, &nodeCert)

	// Non-canonical Ed25519 point: y = p = 2^255 - 19 (RFC 8032 §5.1.3)
	nonCanonicalPoint := make([]byte, 32)
	nonCanonicalPoint[0] = 0xed
	for i := 1; i <= 30; i++ {
		nonCanonicalPoint[i] = 0xff
	}
	nonCanonicalPoint[31] = 0x7f
	nonCanonicalHex := hex.EncodeToString(nonCanonicalPoint)

	invalidKeys := []string{
		"not-hex-at-all",
		"abcd",                               // too short (not 32 bytes)
		hex.EncodeToString(make([]byte, 16)), // 16 bytes instead of 32
		hex.EncodeToString(make([]byte, 64)), // 64 bytes instead of 32
		nonCanonicalHex,                      // 32 bytes, but non-canonical curve point (y >= p)
	}

	for _, badKey := range invalidKeys {
		t.Run(fmt.Sprintf("key_%s", badKey), func(t *testing.T) {
			payload := map[string]any{
				"node_id":          nodeID,
				"endpoint":         "https://10.0.0.50:9099",
				"role":             "worker",
				"agent_public_key": badKey,
			}
			body, _ := json.Marshal(payload)

			resp, err := client.Post(srvURL+"/v1/mesh/join", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for bad key %q, got %d", badKey, resp.StatusCode)
			}
		})
	}

	events, err := jl.Events(context.Background())
	if err != nil {
		t.Fatalf("failed to read joinlog: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events in joinlog after malformed key attempts, got %d", len(events))
	}
}
