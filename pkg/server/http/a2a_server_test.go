package http_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/a2a"
	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	serverhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
	_ "modernc.org/sqlite"
)

// TestA2A_ServerServesVerifiableAgentCard tests that:
// 1. GET /.well-known/agent-card.json returns 200 OK with valid JWS Agent Card envelope.
// 2. The card's signature verifies cryptographically with the coordinator's public key.
// 3. The card's mesh block matches the coordinator's JoinLog snapshot state digest.
func TestA2A_ServerServesVerifiableAgentCard(t *testing.T) {
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

	_ = store.InitTokenSchema(db)
	_ = store.InitCertSchema(db)

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

	// Create coordinator Ed25519 signer
	coordPub, coordPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	signer, err := signing.NewBasicSigner(coordPriv, "coordinator")
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	cfg := serverhttp.ServerConfig{
		TLSEnabled:    true,
		TLSCertFile:   filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:    filepath.Join(tlsDir, pki.CertKeyFile),
		RequireMTLS:   false, // Public discovery per A1/RFC
		MeshCA:        ca,
		MeshCAPemFile: filepath.Join(tlsDir, pki.CAPemFile),
		TokenStore:    tokenStore,
		CertStore:     certStore,
		Registry:      registry.NewRegistry(10 * time.Minute),
		JoinLog:       jl,
		JoinProjector: jp,
		Signer:        signer,
	}

	srv, addr := startTestServer(t, cfg)
	_ = srv
	srvURL := "https://" + addr
	client := clientWithCertDirect(ca, nil)

	// 1. GET /.well-known/agent-card.json
	resp, err := client.Get(srvURL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET /.well-known/agent-card.json failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "application/json" && contentType != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type application/json, got %q", contentType)
	}

	var env a2a.AgentCardEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("failed to decode AgentCardEnvelope: %v", err)
	}

	// 2. Cryptographic verification
	card, err := a2a.VerifyAgentCard(&env)
	if err != nil {
		t.Fatalf("failed to verify agent card signature: %v", err)
	}

	if card.Subject.Role != "coordinator" {
		t.Errorf("expected role 'coordinator', got %q", card.Subject.Role)
	}
	if card.Subject.PublicKey != hex.EncodeToString(coordPub) {
		t.Errorf("expected coordinator public key %q, got %q", hex.EncodeToString(coordPub), card.Subject.PublicKey)
	}

	// 3. Mesh state binding check
	snap, err := jp.CreateSnapshot(context.Background(), jl)
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	if card.Mesh.StateDigest != snap.StateDigest {
		t.Errorf("expected state_digest %q matching current snapshot, got %q", snap.StateDigest, card.Mesh.StateDigest)
	}

	// 4. Verification fails if card is tampered
	tamperedEnv := env
	tamperedCard := *card
	tamperedCard.Name = "malicious-coordinator"
	tamperedPayload, _ := a2a.EncodeCanonicalPayload(&tamperedCard)
	tamperedEnv.Payload = tamperedPayload

	if _, err := a2a.VerifyAgentCard(&tamperedEnv); err == nil {
		t.Errorf("expected verification to fail for tampered card, got nil")
	}
}

// TestA2A_ServerDistributesWorkerCardWithoutSigningForIt verifies that when a client queries
// for a registered worker node's card, the coordinator distributes the worker's card
// but NEVER signs in place of the worker (RFC-003 §4.2.1.2).
func TestA2A_ServerDistributesWorkerCardWithoutSigningForIt(t *testing.T) {
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

	_ = store.InitTokenSchema(db)
	_ = store.InitCertSchema(db)

	tokenStore := store.NewSQLiteTokenStore(db)
	certStore := store.NewSQLiteCertStore(db)

	jlPath := filepath.Join(tlsDir, "test_join.db")
	jl, _ := joinlog.New(jlPath)
	jpPath := filepath.Join(tlsDir, "test_proj.db")
	jp, _ := joinlog.NewProjector(jpPath)

	// Register a worker in JoinLog with its own Ed25519 key
	workerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	workerPubHex := hex.EncodeToString(workerPub)
	workerNodeID := "worker-alpha"

	workerJoinedPayload, _ := json.Marshal(joinlog.NodeJoinedPayload{
		NodeID:         workerNodeID,
		Role:           "worker",
		Endpoint:       "https://10.0.0.15:9099",
		AgentPublicKey: workerPubHex,
		Tags:           []string{"gpu", "python"},
	})
	_ = jl.Append(context.Background(), joinlog.Event{
		Type:    joinlog.EventTypeNodeJoined,
		Payload: workerJoinedPayload,
	})
	headSeq, headHash, _ := jl.HeadPosition(context.Background())
	_ = jp.ProjectEvent(context.Background(), joinlog.Event{
		Seq:     headSeq,
		Hash:    headHash,
		Type:    joinlog.EventTypeNodeJoined,
		Payload: workerJoinedPayload,
	})

	// Coordinator signer
	_, coordPriv, _ := ed25519.GenerateKey(rand.Reader)
	coordSigner, _ := signing.NewBasicSigner(coordPriv, "coordinator")

	cfg := serverhttp.ServerConfig{
		TLSEnabled:    true,
		TLSCertFile:   filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:    filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:        ca,
		MeshCAPemFile: filepath.Join(tlsDir, pki.CAPemFile),
		TokenStore:    tokenStore,
		CertStore:     certStore,
		Registry:      registry.NewRegistry(10 * time.Minute),
		JoinLog:       jl,
		JoinProjector: jp,
		Signer:        coordSigner,
	}

	_, addr := startTestServer(t, cfg)
	client := clientWithCertDirect(ca, nil)

	// Query worker's card
	resp, err := client.Get("https://" + addr + "/.well-known/agent-card.json?node_id=" + workerNodeID)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for worker card, got %d", resp.StatusCode)
	}

	// Decode card: it must declare worker as subject and worker's public key
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("failed to decode worker card: %v", err)
	}

	if card.Subject.NodeID != workerNodeID {
		t.Errorf("expected subject node_id %q, got %q", workerNodeID, card.Subject.NodeID)
	}
	if card.Subject.PublicKey != workerPubHex {
		t.Errorf("expected subject public key %q, got %q", workerPubHex, card.Subject.PublicKey)
	}
}

// TestA2A_ServerWithoutSignerReturns503 verifies that if coordinator has no Ed25519 signer
// configured, /.well-known/agent-card.json returns HTTP 503.
func TestA2A_ServerWithoutSignerReturns503(t *testing.T) {
	tlsDir := t.TempDir()
	ca, _, _ := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test CA", "testing", []string{"127.0.0.1", "localhost"}, true)

	dbPath := filepath.Join(tlsDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_ = store.InitTokenSchema(db)
	tokenStore := store.NewSQLiteTokenStore(db)

	cfg := serverhttp.ServerConfig{
		TLSEnabled:    true,
		TLSCertFile:   filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:    filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:        ca,
		MeshCAPemFile: filepath.Join(tlsDir, pki.CAPemFile),
		TokenStore:    tokenStore,
		Registry:      registry.NewRegistry(10 * time.Minute),
		Signer:        nil, // No signer
	}

	_, addr := startTestServer(t, cfg)
	client := clientWithCertDirect(ca, nil)

	resp, err := client.Get("https://" + addr + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable without signer, got %d", resp.StatusCode)
	}
}
