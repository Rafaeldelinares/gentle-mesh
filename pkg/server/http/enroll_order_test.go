package http_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

// enrollTestServer starts a TLS coordinator with an enrollment token store and
// no bearer token, so the requests exercise handleCertsEnroll itself.
func enrollTestServer(t *testing.T, tokens map[string]int) (*http.Client, string, *store.SQLiteTokenStore) {
	t.Helper()

	tlsDir := t.TempDir()
	ca, _, err := pki.EnsureMeshTLS(tlsDir, "Gentle Mesh Test", "testing", []string{"localhost"}, true)
	if err != nil {
		t.Fatalf("failed to init TLS: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatalf("failed to open token db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.InitTokenSchema(db); err != nil {
		t.Fatalf("failed to init token schema: %v", err)
	}

	tokenStore := store.NewSQLiteTokenStore(db)
	expiresAt := time.Now().Add(24 * time.Hour)
	for token, maxUses := range tokens {
		if err := tokenStore.CreateToken(context.Background(), &store.TokenRecord{
			Token:     token,
			CreatedAt: time.Now(),
			ExpiresAt: &expiresAt,
			MaxUses:   maxUses,
		}); err != nil {
			t.Fatalf("failed to create token %s: %v", token, err)
		}
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
		TokenStore:       tokenStore,
	}

	_, addr := startTestServer(t, cfg)
	return createClient(t, ca, nil), addr, tokenStore
}

func enrollRequestBody(t *testing.T, fields map[string]string) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("failed to marshal enrollment body: %v", err)
	}
	return bytes.NewReader(raw)
}

func enrollPost(t *testing.T, client *http.Client, addr string, fields map[string]string) (int, map[string]string) {
	t.Helper()
	resp, err := client.Post("https://"+addr+"/v1/certs/enroll", "application/json", enrollRequestBody(t, fields))
	if err != nil {
		t.Fatalf("POST /v1/certs/enroll failed: %v", err)
	}
	defer resp.Body.Close()

	var decoded map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func enrollmentUses(t *testing.T, tokenStore *store.SQLiteTokenStore, token string) int {
	t.Helper()
	rec, err := tokenStore.GetToken(context.Background(), token)
	if err != nil {
		t.Fatalf("GetToken(%s) failed: %v", token, err)
	}
	if rec == nil {
		t.Fatalf("token %s not found", token)
	}
	return rec.Uses
}

func enrollmentCSR(t *testing.T) string {
	t.Helper()
	res, err := pki.GenerateCSR("enroll-order-node")
	if err != nil {
		t.Fatalf("failed to generate CSR: %v", err)
	}
	return res.CSRPEM
}

// A request that handleCertsEnroll is going to reject must not consume the
// enrollment token: node_id and CSR parseability are validated before UseToken.
func TestHandleCertsEnrollRejectsBeforeConsumingToken(t *testing.T) {
	const (
		tokenMissingNodeID = "enroll-order-missing-node-id"
		tokenBadCSR        = "enroll-order-bad-csr"
	)

	client, addr, tokenStore := enrollTestServer(t, map[string]int{
		tokenMissingNodeID: 1,
		tokenBadCSR:        1,
	})
	validCSR := enrollmentCSR(t)

	cases := []struct {
		name      string
		fields    map[string]string
		token     string
		wantCode  int
		wantError string
	}{
		{
			name:      "missing node_id",
			fields:    map[string]string{"token": tokenMissingNodeID, "csr": validCSR},
			token:     tokenMissingNodeID,
			wantCode:  http.StatusBadRequest,
			wantError: "node_id is required",
		},
		{
			// The 500 (and its exact wording) is what this route answered before
			// the reorder; only the token burn is fixed here.
			name:      "unparseable csr",
			fields:    map[string]string{"token": tokenBadCSR, "csr": "not-a-valid-csr-pem", "node_id": "order-node"},
			token:     tokenBadCSR,
			wantCode:  http.StatusInternalServerError,
			wantError: "failed to sign certificate: invalid certificate: no PEM block found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := enrollPost(t, client, addr, tc.fields)
			if code != tc.wantCode {
				t.Fatalf("got status %d, want %d (body %v)", code, tc.wantCode, body)
			}
			if body["error"] != tc.wantError {
				t.Fatalf("got error %q, want %q", body["error"], tc.wantError)
			}
			if uses := enrollmentUses(t, tokenStore, tc.token); uses != 0 {
				t.Fatalf("a rejected request burned the token: uses=%d, want 0", uses)
			}
		})
	}
}

// A valid request still issues the certificate and consumes exactly one use.
func TestHandleCertsEnrollConsumesOneUseOnSuccess(t *testing.T) {
	const token = "enroll-order-success"

	client, addr, tokenStore := enrollTestServer(t, map[string]int{token: 1})

	code, body := enrollPost(t, client, addr, map[string]string{
		"token":   token,
		"csr":     enrollmentCSR(t),
		"node_id": "order-node",
	})

	if code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %v)", code, body)
	}
	if body["cert_pem"] == "" {
		t.Fatalf("expected a signed certificate, got %v", body)
	}
	if body["node_id"] != "order-node" {
		t.Fatalf("got node_id %q, want %q", body["node_id"], "order-node")
	}
	if uses := enrollmentUses(t, tokenStore, token); uses != 1 {
		t.Fatalf("uses=%d, want 1", uses)
	}
}
