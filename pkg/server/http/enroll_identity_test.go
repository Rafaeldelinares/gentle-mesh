package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

func enrollServerConfig(t *testing.T, ca *pki.MeshCA, tlsDir string, tokenStore *store.SQLiteTokenStore, certStore store.CertStore) meshhttp.ServerConfig {
	t.Helper()
	return meshhttp.ServerConfig{
		TasksDir:         t.TempDir(),
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
	}
}

func makeToken(t *testing.T, tokenStore *store.SQLiteTokenStore, name string, maxUses int) {
	t.Helper()
	exp := time.Now().Add(time.Hour)
	if err := tokenStore.CreateToken(context.Background(), &store.TokenRecord{
		Token:     name,
		ExpiresAt: &exp,
		MaxUses:   maxUses,
	}); err != nil {
		t.Fatalf("create token %s: %v", name, err)
	}
}

// enrollWithToken posts an enrollment request and returns the status code. The
// CSR always carries a freshly generated key, so the only thing being reused
// across attempts is the identity in node_id.
func enrollWithToken(t *testing.T, srvURL string, ca *pki.MeshCA, token, nodeID string) int {
	t.Helper()
	csr, err := pki.GenerateCSR(nodeID)
	if err != nil {
		t.Fatalf("generate CSR for %s: %v", nodeID, err)
	}
	payload, _ := json.Marshal(map[string]string{
		"token":   token,
		"csr":     csr.CSRPEM,
		"node_id": nodeID,
	})
	client := createClient(t, ca, nil)
	resp, err := client.Post(srvURL+"/v1/certs/enroll", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /v1/certs/enroll for %s: %v", nodeID, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func tokenUses(t *testing.T, tokenStore *store.SQLiteTokenStore, token string) int {
	t.Helper()
	rec, err := tokenStore.GetToken(context.Background(), token)
	if err != nil {
		t.Fatalf("GetToken(%s): %v", token, err)
	}
	if rec == nil {
		t.Fatalf("token %s vanished", token)
	}
	return rec.Uses
}

// TestEnrollRefusesIdentityThatAlreadyHasACertificate is the criterion that
// closes F1-02.
//
// node_id arrives in the request body and the only credential is an enrollment
// token, which is not bound to any identity. So any valid token used to mint a
// CA-signed certificate for ANY node id in the mesh, and because issuance clears
// revoked to 0, that also resurrected revoked nodes. The legitimate rotation
// path is cert-issue with an admin token, not enrollment, which is the bootstrap
// of a node that does not exist yet.
func TestEnrollRefusesIdentityThatAlreadyHasACertificate(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)
	_, addr := startTestServer(t, enrollServerConfig(t, ca, tlsDir, tokenStore, certStore))
	srvURL := "https://" + addr

	const victim = "worker-alpha"
	makeToken(t, tokenStore, "bootstrap-token", 1)
	if code := enrollWithToken(t, srvURL, ca, "bootstrap-token", victim); code != 200 {
		t.Fatalf("the bootstrap enrollment must succeed, got %d", code)
	}

	// A second, perfectly valid token must not be usable to take over an
	// identity that already exists.
	const attackerToken = "attacker-token"
	makeToken(t, tokenStore, attackerToken, 1)
	code := enrollWithToken(t, srvURL, ca, attackerToken, victim)
	if code == 200 {
		t.Fatalf("ESCAPE: a second token minted a certificate for the existing identity %q", victim)
	}

	// And the rejection must not have burned the token: refusing is not the same
	// as consuming, or an operator would lose a valid invite to an attack.
	if uses := tokenUses(t, tokenStore, attackerToken); uses != 0 {
		t.Errorf("the refused enrollment consumed the token (uses=%d); a rejection must leave it usable", uses)
	}
}

// The same attempt against a revoked node is the case that mattered most: the
// issuance path clears revoked to 0, so accepting it here would silently bring a
// compromised node back into the mesh with a valid certificate.
func TestEnrollRefusesRevokedIdentity(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)
	_, addr := startTestServer(t, enrollServerConfig(t, ca, tlsDir, tokenStore, certStore))
	srvURL := "https://" + addr

	const revoked = "worker-compromised"
	makeToken(t, tokenStore, "first-token", 1)
	if code := enrollWithToken(t, srvURL, ca, "first-token", revoked); code != 200 {
		t.Fatalf("the bootstrap enrollment must succeed, got %d", code)
	}
	if err := certStore.RevokeCert(context.Background(), revoked); err != nil {
		t.Fatalf("revoke %s: %v", revoked, err)
	}

	const second = "second-token"
	makeToken(t, tokenStore, second, 1)
	if code := enrollWithToken(t, srvURL, ca, second, revoked); code == 200 {
		t.Fatalf("ESCAPE: a revoked node was resurrected through enrollment")
	}
	if uses := tokenUses(t, tokenStore, second); uses != 0 {
		t.Errorf("the refused enrollment consumed the token (uses=%d)", uses)
	}
}

// A brand new identity must still enroll normally, or the fix is just locking
// everybody out.
func TestEnrollStillAcceptsANewIdentity(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)
	_, addr := startTestServer(t, enrollServerConfig(t, ca, tlsDir, tokenStore, certStore))
	srvURL := "https://" + addr

	makeToken(t, tokenStore, "token-a", 1)
	if code := enrollWithToken(t, srvURL, ca, "token-a", "worker-beta"); code != 200 {
		t.Fatalf("a new identity must be able to enroll, got %d", code)
	}
	makeToken(t, tokenStore, "token-b", 1)
	if code := enrollWithToken(t, srvURL, ca, "token-b", "worker-gamma"); code != 200 {
		t.Fatalf("a second new identity must be able to enroll, got %d", code)
	}
}