package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func setupTestCertDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := InitCertSchema(db); err != nil {
		db.Close()
		t.Fatalf("failed to init cert schema: %v", err)
	}

	return db
}

func TestCertStoreIssueAndGet(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	cert := &CertRecord{
		NodeID:     "worker-alpha",
		CommonName: "worker-alpha",
		Serial:     "12345",
		CertPEM:    "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		IssuedAt:   time.Now(),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
	}

	// Issue cert
	err := store.IssueCert(ctx, cert)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	// Get cert
	retrieved, err := store.GetCert(ctx, "worker-alpha")
	if err != nil {
		t.Fatalf("GetCert failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetCert returned nil")
	}

	if retrieved.NodeID != cert.NodeID {
		t.Errorf("expected NodeID %s, got %s", cert.NodeID, retrieved.NodeID)
	}

	if retrieved.Serial != cert.Serial {
		t.Errorf("expected Serial %s, got %s", cert.Serial, retrieved.Serial)
	}

	if retrieved.Revoked {
		t.Error("newly issued cert should not be revoked")
	}
}

func TestCertStoreRevoke(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	cert := &CertRecord{
		NodeID:     "worker-beta",
		CommonName: "worker-beta",
		Serial:     "67890",
		CertPEM:    "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		IssuedAt:   time.Now(),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
	}

	err := store.IssueCert(ctx, cert)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	// Revoke cert
	err = store.RevokeCert(ctx, "worker-beta")
	if err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	// Check revocation
	retrieved, err := store.GetCert(ctx, "worker-beta")
	if err != nil {
		t.Fatalf("GetCert failed: %v", err)
	}

	if !retrieved.Revoked {
		t.Error("cert should be revoked")
	}

	if retrieved.RevokedAt == nil {
		t.Error("RevokedAt should be set")
	}

	// Check IsCertRevoked
	isRevoked, err := store.IsCertRevoked(ctx, "67890")
	if err != nil {
		t.Fatalf("IsCertRevoked failed: %v", err)
	}
	if !isRevoked {
		t.Error("cert should be reported as revoked")
	}
}

func TestCertStoreList(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	// Issue multiple certs
	for i := 0; i < 3; i++ {
		cert := &CertRecord{
			NodeID:     "worker-" + string(rune('a'+i)),
			CommonName: "worker-" + string(rune('a'+i)),
			Serial:     "serial-" + string(rune('0'+i)),
			CertPEM:    "pem-data",
			IssuedAt:   time.Now().Add(-time.Duration(i) * time.Hour),
			ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
		}
		err := store.IssueCert(ctx, cert)
		if err != nil {
			t.Fatalf("IssueCert failed: %v", err)
		}
	}

	// List certs
	certs, err := store.ListCerts(ctx)
	if err != nil {
		t.Fatalf("ListCerts failed: %v", err)
	}

	if len(certs) != 3 {
		t.Errorf("expected 3 certs, got %d", len(certs))
	}
}

func TestCertStoreDelete(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	cert := &CertRecord{
		NodeID:     "worker-to-delete",
		CommonName: "worker-to-delete",
		Serial:     "delete-me",
		CertPEM:    "pem-data",
		IssuedAt:   time.Now(),
		ExpiresAt:  time.Now().Add(365 * 24 * time.Hour),
	}

	err := store.IssueCert(ctx, cert)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}

	// Delete cert
	err = store.DeleteCert(ctx, "worker-to-delete")
	if err != nil {
		t.Fatalf("DeleteCert failed: %v", err)
	}

	// Verify deleted
	retrieved, err := store.GetCert(ctx, "worker-to-delete")
	if err != nil {
		t.Fatalf("GetCert failed: %v", err)
	}
	if retrieved != nil {
		t.Error("cert should be deleted")
	}
}

func TestCertStoreGetNotFound(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	retrieved, err := store.GetCert(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetCert should not error for nonexistent: %v", err)
	}
	if retrieved != nil {
		t.Error("GetCert should return nil for nonexistent")
	}
}

func TestCertStoreIsCertRevokedUnknown(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	store := NewSQLiteCertStore(db)
	ctx := context.Background()

	// Unknown serial should return false (not revoked)
	isRevoked, err := store.IsCertRevoked(ctx, "unknown-serial")
	if err != nil {
		t.Fatalf("IsCertRevoked failed: %v", err)
	}
	if isRevoked {
		t.Error("unknown cert should not be reported as revoked")
	}
}
