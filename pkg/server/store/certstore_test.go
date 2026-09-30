package store

import (
	"context"
	"database/sql"
	"strings"
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

func TestCertStore_SchemaHasNoKeyPEMColumn(t *testing.T) {
	db := setupTestCertDB(t)
	defer db.Close()

	rows, err := db.Query("PRAGMA table_info(node_certs);")
	if err != nil {
		t.Fatalf("failed to query table_info: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("failed to scan column info: %v", err)
		}
		columns[name] = true
	}

	if columns["key_pem"] {
		t.Fatal("security violation: node_certs table contains key_pem column, private keys must not be stored on server")
	}
	expectedCols := []string{"node_id", "common_name", "serial", "cert_pem", "issued_at", "expires_at", "revoked"}
	for _, col := range expectedCols {
		if !columns[col] {
			t.Errorf("missing expected column %s in node_certs", col)
		}
	}
}

func TestCertStore_MigrationDropsKeyPEMFromLegacySchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	// 1. Manually create legacy schema with key_pem
	legacyDDL := `CREATE TABLE node_certs (
		node_id TEXT PRIMARY KEY,
		common_name TEXT NOT NULL,
		serial TEXT UNIQUE NOT NULL,
		cert_pem TEXT NOT NULL,
		key_pem TEXT,
		issued_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked INTEGER NOT NULL DEFAULT 0,
		revoked_at INTEGER
	);`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}

	// 2. Insert dummy legacy record with a private key
	_, err = db.Exec(`INSERT INTO node_certs (
		node_id, common_name, serial, cert_pem, key_pem, issued_at, expires_at, revoked
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-node-1", "legacy-node-1", "serial-999",
		"-----BEGIN CERTIFICATE-----\nLEGACY_CERT\n-----END CERTIFICATE-----",
		"-----BEGIN EC PRIVATE KEY-----\nLEGACY_PRIVATE_KEY_LEAK\n-----END EC PRIVATE KEY-----",
		time.Now().Unix(), time.Now().Add(24*time.Hour).Unix(), 0,
	)
	if err != nil {
		t.Fatalf("failed to insert legacy record: %v", err)
	}

	// 3. Run InitCertSchema(db) which must migrate and drop/clean key_pem
	if err := InitCertSchema(db); err != nil {
		t.Fatalf("InitCertSchema migration failed: %v", err)
	}

	// 4. Verify key_pem column no longer exists
	rows, err := db.Query("PRAGMA table_info(node_certs);")
	if err != nil {
		t.Fatalf("failed to query table_info: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("failed to scan column info: %v", err)
		}
		columns[name] = true
	}
	if columns["key_pem"] {
		t.Fatal("security violation: legacy key_pem column still present after migration")
	}

	// 5. Verify the existing cert record remains intact and queryable
	var certPEM string
	err = db.QueryRow("SELECT cert_pem FROM node_certs WHERE node_id = ?", "legacy-node-1").Scan(&certPEM)
	if err != nil {
		t.Fatalf("failed to query legacy cert after migration: %v", err)
	}
	if !strings.Contains(certPEM, "LEGACY_CERT") {
		t.Errorf("cert_pem corrupted after migration: got %s", certPEM)
	}
}

func TestCertStore_MigrationFailureReturnsError(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	// 1. Create legacy table with key_pem column
	legacyDDL := `CREATE TABLE node_certs (
		node_id TEXT PRIMARY KEY,
		common_name TEXT NOT NULL,
		serial TEXT UNIQUE NOT NULL,
		cert_pem TEXT NOT NULL,
		key_pem TEXT,
		issued_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked INTEGER NOT NULL DEFAULT 0,
		revoked_at INTEGER
	);`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}

	// 2. Add an index referencing key_pem so `ALTER TABLE node_certs DROP COLUMN key_pem;` fails in SQLite
	if _, err := db.Exec("CREATE INDEX idx_legacy_key ON node_certs(key_pem);"); err != nil {
		t.Fatalf("failed to create index on key_pem: %v", err)
	}

	// 3. Add a trigger blocking UPDATE of key_pem so `UPDATE node_certs SET key_pem = NULL;` also fails
	triggerDDL := `CREATE TRIGGER trg_block_update BEFORE UPDATE OF key_pem ON node_certs
	BEGIN
		SELECT RAISE(FAIL, 'update forbidden');
	END;`
	if _, err := db.Exec(triggerDDL); err != nil {
		t.Fatalf("failed to create trigger on key_pem: %v", err)
	}

	// 4. Insert a record so the update trigger executes on row update
	_, err = db.Exec(`INSERT INTO node_certs (
		node_id, common_name, serial, cert_pem, key_pem, issued_at, expires_at, revoked
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"node-1", "node-1", "serial-1", "cert", "key", 0, 0, 0,
	)
	if err != nil {
		t.Fatalf("failed to insert legacy record: %v", err)
	}

	// 5. InitCertSchema must return an error when both DROP COLUMN and fallback UPDATE fail
	err = InitCertSchema(db)
	if err == nil {
		t.Fatal("expected InitCertSchema to return error when both DROP COLUMN and fallback UPDATE fail, got nil")
	}
	if !strings.Contains(err.Error(), "failed to drop or sanitize") {
		t.Errorf("expected error message mentioning migration failure, got: %v", err)
	}
}


