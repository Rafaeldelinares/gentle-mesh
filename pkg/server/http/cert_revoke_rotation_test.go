package http_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

// rotationServerConfig builds a coordinator config over an existing cert store,
// which is what a restart looks like: a brand new Server warming its state up
// from the database that survived the previous process.
func rotationServerConfig(t *testing.T, ca *pki.MeshCA, tlsDir string, certStore store.CertStore) meshhttp.ServerConfig {
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
		CertStore:        certStore,
	}
}

func issueTestCert(t *testing.T, certStore store.CertStore, nodeID, serial string) {
	t.Helper()
	err := certStore.IssueCert(context.Background(), &store.CertRecord{
		NodeID:     nodeID,
		CommonName: nodeID,
		Serial:     serial,
		CertPEM:    "dummy-pem",
		IssuedAt:   time.Now().Add(-time.Hour),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("issue %s for %s: %v", serial, nodeID, err)
	}
}

// TestRevokedCertSurvivesRotationAndRestart is the criterion that defines the
// close of F1-01.
//
// node_certs holds one row per node, so rotating a certificate overwrites the
// row and its serial, and cleared revoked to 0. Revocation used to live only
// there, so after a rotation the revoked serial was simply gone, and a fresh
// Server (the restart) could not warm its in-memory blocklist from a record that
// no longer existed. The revoked certificate then verified again, for the rest
// of its 365-day validity.
func TestRevokedCertSurvivesRotationAndRestart(t *testing.T) {
	ca, tlsDir, _, certStore, _ := setupMTLSTestEnvironment(t)
	ctx := context.Background()

	const (
		nodeID    = "rotating-node"
		oldSerial = "99887766554433221"
		newSerial = "11223344556677889"
	)

	issueTestCert(t, certStore, nodeID, oldSerial)
	if err := certStore.RevokeCert(ctx, nodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// The node comes back with a freshly issued certificate: the rotation
	// overwrites the row that carried the revocation.
	issueTestCert(t, certStore, nodeID, newSerial)

	srv, _ := startTestServer(t, rotationServerConfig(t, ca, tlsDir, certStore))

	revoked, err := srv.IsCertRevoked(ctx, oldSerial)
	if err != nil {
		t.Fatalf("IsCertRevoked(old serial): %v", err)
	}
	if !revoked {
		t.Errorf("the revoked serial %s verified again after its node rotated and the coordinator restarted", oldSerial)
	}

	// The replacement certificate must remain usable, or the fix would just be
	// locking the node out of the mesh forever.
	revokedNew, err := srv.IsCertRevoked(ctx, newSerial)
	if err != nil {
		t.Fatalf("IsCertRevoked(new serial): %v", err)
	}
	if revokedNew {
		t.Errorf("the replacement serial %s must not be revoked", newSerial)
	}
}

// A database error while deciding whether a certificate is revoked must not
// degrade into "not revoked": the handshake has to fail, not open.
func TestIsCertRevokedFailsClosedOnStoreError(t *testing.T) {
	ca, tlsDir, _, certStore, _ := setupMTLSTestEnvironment(t)
	issueTestCert(t, certStore, "db-error-node", "5544332211009988")

	srv, _ := startTestServer(t, rotationServerConfig(t, ca, tlsDir, &failingCertStore{
		inner: certStore,
		err:   errors.New("database is locked"),
	}))

	revoked, err := srv.IsCertRevoked(context.Background(), "5544332211009988")
	if err == nil {
		t.Fatalf("a store failure must surface as an error, got revoked=%v with no error", revoked)
	}
	if revoked {
		t.Errorf("on a store failure the result must not claim the certificate is revoked")
	}
}

// failingCertStore delegates everything to a real store except the revocation
// lookup, which always fails: the store it wraps is not consulted for a decision
// that must be read from the authoritative source.
type failingCertStore struct {
	inner store.CertStore
	err   error
}

func (s *failingCertStore) IssueCert(ctx context.Context, rec *store.CertRecord) error {
	return s.inner.IssueCert(ctx, rec)
}
func (s *failingCertStore) IssueCertIfAbsent(ctx context.Context, rec *store.CertRecord) error {
	return s.inner.IssueCertIfAbsent(ctx, rec)
}
func (s *failingCertStore) RevokeSerial(ctx context.Context, serial, nodeID string) error {
	return s.inner.RevokeSerial(ctx, serial, nodeID)
}
func (s *failingCertStore) GetCert(ctx context.Context, nodeID string) (*store.CertRecord, error) {
	return s.inner.GetCert(ctx, nodeID)
}
func (s *failingCertStore) ListCerts(ctx context.Context) ([]*store.CertRecord, error) {
	return s.inner.ListCerts(ctx)
}
func (s *failingCertStore) RevokeCert(ctx context.Context, nodeID string) error {
	return s.inner.RevokeCert(ctx, nodeID)
}
func (s *failingCertStore) IsCertRevoked(ctx context.Context, serial string) (bool, error) {
	return false, s.err
}

// RevokedSerials still works on purpose: this test is about the decision a
// handshake makes, not about startup. A store that cannot even be read at boot
// must stop the coordinator from starting, and that is a separate behaviour.
func (s *failingCertStore) RevokedSerials(ctx context.Context) ([]store.RevokedCertRecord, error) {
	return s.inner.RevokedSerials(ctx)
}
func (s *failingCertStore) DeleteCert(ctx context.Context, nodeID string) error {
	return s.inner.DeleteCert(ctx, nodeID)
}
func (s *failingCertStore) Close() error { return s.inner.Close() }
