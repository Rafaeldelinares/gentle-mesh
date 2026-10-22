package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CertStore defines the interface for managing issued certificates.
type CertStore interface {
	// IssueCert stores a newly issued certificate.
	IssueCert(ctx context.Context, cert *CertRecord) error
	// IssueCertIfAbsent stores a certificate only when the node has none, and
	// reports ErrCertIdentityExists otherwise. The insert is the arbiter, so two
	// concurrent callers cannot both win.
	IssueCertIfAbsent(ctx context.Context, cert *CertRecord) error
	// RevokeSerial records a serial as revoked without needing a node_certs row.
	// Used when a certificate was signed but must not be usable, which is the
	// case for the loser of a concurrent enrollment race.
	RevokeSerial(ctx context.Context, serial, nodeID string) error
	// GetCert retrieves a certificate by node ID.
	GetCert(ctx context.Context, nodeID string) (*CertRecord, error)
	// ListCerts returns all issued certificates.
	ListCerts(ctx context.Context) ([]*CertRecord, error)
	// RevokeCert marks a certificate as revoked.
	RevokeCert(ctx context.Context, nodeID string) error
	// IsCertRevoked is the authoritative revocation lookup, keyed by serial.
	IsCertRevoked(ctx context.Context, serial string) (bool, error)
	// RevokedSerials lists every serial in the revocation ledger, so a starting
	// coordinator can warm its in-memory blocklist from the authoritative source.
	RevokedSerials(ctx context.Context) ([]RevokedCertRecord, error)
	// DeleteCert removes a certificate record.
	DeleteCert(ctx context.Context, nodeID string) error
	// Close closes the certificate store.
	Close() error
}

// CertRecord represents an issued node certificate record.
type CertRecord struct {
	NodeID     string
	CommonName string
	Serial     string
	CertPEM    string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Revoked    bool
	RevokedAt  *time.Time
}

// ErrCertIdentityExists reports that a certificate is already recorded for a
// node id. Enrollment uses it to lose a race it must not win: IssueCert upserts
// over node_id, so a second enrollment would silently replace the first
// certificate and leave that first one in neither table.
var ErrCertIdentityExists = errors.New("certificate already exists for this node id")

// RevokedCertRecord is an entry of the append-only revocation ledger.
type RevokedCertRecord struct {
	Serial    string
	NodeID    string
	RevokedAt time.Time
}

// SQLiteCertStore implements CertStore using SQLite.
type SQLiteCertStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	closeMu sync.RWMutex
	closed  bool
}

// NewSQLiteCertStore creates a new SQLite certificate store.
func NewSQLiteCertStore(db *sql.DB) *SQLiteCertStore {
	return &SQLiteCertStore{db: db}
}

// InitCertSchema initializes the certificates table schema.
func InitCertSchema(db *sql.DB) error {
	ddlStatements := []string{
		`CREATE TABLE IF NOT EXISTS node_certs (
			node_id TEXT PRIMARY KEY,
			common_name TEXT NOT NULL,
			serial TEXT UNIQUE NOT NULL,
			cert_pem TEXT NOT NULL,
			issued_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0,
			revoked_at INTEGER
		);`,
		`CREATE INDEX IF NOT EXISTS idx_certs_serial ON node_certs(serial);`,
		`CREATE INDEX IF NOT EXISTS idx_certs_revoked ON node_certs(revoked);`,

		// The revocation ledger. node_certs holds ONE row per node, so rotating a
		// certificate overwrites that row: the serial of the certificate it
		// replaced, and its revoked flag, are gone. Revocation is a fact that must
		// survive that, so it is recorded separately and append-only, keyed by
		// serial, which is what a TLS handshake actually presents.
		`CREATE TABLE IF NOT EXISTS revoked_serials (
			serial TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			revoked_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_revoked_serials_node ON revoked_serials(node_id);`,

		// Backfill, so revocations recorded before this ledger existed are not
		// lost on upgrade: INSERT OR IGNORE keeps re-running it idempotent.
		`INSERT OR IGNORE INTO revoked_serials (serial, node_id, revoked_at)
			SELECT serial, node_id, COALESCE(revoked_at, 0) FROM node_certs WHERE revoked = 1;`,
	}

	for _, stmt := range ddlStatements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to initialize cert schema: %w", err)
		}
	}

	// Migration: ensure legacy schema does not retain key_pem
	rows, err := db.Query("PRAGMA table_info(node_certs);")
	if err != nil {
		return fmt.Errorf("failed to query table info for node_certs: %w", err)
	}

	hasLegacyKey := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan table info for node_certs: %w", err)
		}
		if name == "key_pem" {
			hasLegacyKey = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("failed during table info iteration for node_certs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close table info rows: %w", err)
	}

	if hasLegacyKey {
		if _, dropErr := db.Exec("ALTER TABLE node_certs DROP COLUMN key_pem;"); dropErr != nil {
			if _, updateErr := db.Exec("UPDATE node_certs SET key_pem = NULL;"); updateErr != nil {
				return fmt.Errorf("failed to drop or sanitize legacy private key column: drop failed (%v), update failed (%w)", dropErr, updateErr)
			}
		}
	}

	return nil
}

// IssueCert stores a newly issued certificate.
func (s *SQLiteCertStore) IssueCert(ctx context.Context, cert *CertRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cert == nil {
		return errors.New("cert cannot be nil")
	}
	if cert.NodeID == "" {
		return errors.New("node_id cannot be empty")
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("cert store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `
	INSERT INTO node_certs (
		node_id, common_name, serial, cert_pem,
		issued_at, expires_at, revoked
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(node_id) DO UPDATE SET
		common_name = excluded.common_name,
		serial = excluded.serial,
		cert_pem = excluded.cert_pem,
		issued_at = excluded.issued_at,
		expires_at = excluded.expires_at,
		revoked = 0,
		revoked_at = NULL;
	`

	_, err := s.db.ExecContext(ctx, query,
		cert.NodeID,
		cert.CommonName,
		cert.Serial,
		cert.CertPEM,
		cert.IssuedAt.Unix(),
		cert.ExpiresAt.Unix(),
		0, // Not revoked on issue
	)
	if err != nil {
		return fmt.Errorf("failed to issue cert: %w", err)
	}

	return nil
}

// IssueCertIfAbsent stores a certificate only when the node has none.
//
// It deliberately does NOT upsert: the plain INSERT is the atomic arbiter, so
// when two callers race the same node id exactly one insert lands and the other
// gets ErrCertIdentityExists. Upserting here would be worse than losing the
// race, because the loser's certificate would be signed by the CA and then
// overwritten out of existence: presentable at a TLS handshake, absent from
// node_certs and from the revocation ledger, and valid for a year.
func (s *SQLiteCertStore) IssueCertIfAbsent(ctx context.Context, cert *CertRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("cert store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `
	INSERT INTO node_certs (
		node_id, common_name, serial, cert_pem,
		issued_at, expires_at, revoked
	) VALUES (?, ?, ?, ?, ?, ?, 0)
	`
	if _, err := s.db.ExecContext(ctx, query,
		cert.NodeID, cert.CommonName, cert.Serial, cert.CertPEM,
		cert.IssuedAt.Unix(), cert.ExpiresAt.Unix()); err != nil {
		// Any failure that left a row for this node id means we lost the race
		// rather than hitting a different error, so ask instead of matching on
		// driver error strings.
		var one int
		if qErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM node_certs WHERE node_id = ?`, cert.NodeID).Scan(&one); qErr == nil {
			return ErrCertIdentityExists
		}
		return fmt.Errorf("failed to issue cert: %w", err)
	}
	return nil
}

// RevokeSerial records a serial in the ledger without requiring a node_certs
// row, which is the case for a certificate that was signed but must not be
// usable: the loser of a concurrent enrollment has no row of its own.
func (s *SQLiteCertStore) RevokeSerial(ctx context.Context, serial, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if serial == "" {
		return errors.New("serial is required")
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("cert store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO revoked_serials (serial, node_id, revoked_at) VALUES (?, ?, ?)`,
		serial, nodeID, time.Now().Unix()); err != nil {
		return fmt.Errorf("failed to record revocation: %w", err)
	}
	return nil
}

// GetCert retrieves a certificate by node ID.
func (s *SQLiteCertStore) GetCert(ctx context.Context, nodeID string) (*CertRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("cert store is closed")
	}

	query := `SELECT node_id, common_name, serial, cert_pem, 
		issued_at, expires_at, revoked, revoked_at 
		FROM node_certs WHERE node_id = ?`

	row := s.db.QueryRowContext(ctx, query, nodeID)

	var cert CertRecord
	var revoked int
	var issuedAt, expiresAt int64
	var revokedAtPtr *int64

	err := row.Scan(
		&cert.NodeID,
		&cert.CommonName,
		&cert.Serial,
		&cert.CertPEM,
		&issuedAt,
		&expiresAt,
		&revoked,
		&revokedAtPtr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cert: %w", err)
	}

	cert.IssuedAt = time.Unix(issuedAt, 0)
	cert.ExpiresAt = time.Unix(expiresAt, 0)
	cert.Revoked = revoked == 1
	if revokedAtPtr != nil {
		t := time.Unix(*revokedAtPtr, 0)
		cert.RevokedAt = &t
	}

	return &cert, nil
}

// ListCerts returns all issued certificates.
func (s *SQLiteCertStore) ListCerts(ctx context.Context) ([]*CertRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("cert store is closed")
	}

	query := `SELECT node_id, common_name, serial, cert_pem,
		issued_at, expires_at, revoked, revoked_at 
		FROM node_certs ORDER BY issued_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list certs: %w", err)
	}
	defer rows.Close()

	var certs []*CertRecord
	for rows.Next() {
		var cert CertRecord
		var revoked int
		var issuedAt, expiresAt int64
		var revokedAtPtr *int64

		err := rows.Scan(
			&cert.NodeID,
			&cert.CommonName,
			&cert.Serial,
			&cert.CertPEM,
			&issuedAt,
			&expiresAt,
			&revoked,
			&revokedAtPtr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cert: %w", err)
		}

		cert.IssuedAt = time.Unix(issuedAt, 0)
		cert.ExpiresAt = time.Unix(expiresAt, 0)
		cert.Revoked = revoked == 1
		if revokedAtPtr != nil {
			t := time.Unix(*revokedAtPtr, 0)
			cert.RevokedAt = &t
		}

		certs = append(certs, &cert)
	}

	return certs, nil
}

// RevokeCert marks a certificate as revoked.
func (s *SQLiteCertStore) RevokeCert(ctx context.Context, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("cert store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().Unix()

	// One transaction for both records: the ledger entry is the fact the TLS
	// handshake consults, and a revocation that only landed on node_certs would
	// be erased by the next rotation of that node.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to revoke cert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE node_certs SET revoked = 1, revoked_at = ? WHERE node_id = ?`, now, nodeID)
	if err != nil {
		return fmt.Errorf("failed to revoke cert: %w", err)
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.New("certificate not found")
	}

	// Read the serial inside the transaction so the ledger records exactly what
	// was revoked, and INSERT OR IGNORE keeps a repeated revocation idempotent.
	var serial string
	if err := tx.QueryRowContext(ctx, `SELECT serial FROM node_certs WHERE node_id = ?`, nodeID).Scan(&serial); err != nil {
		return fmt.Errorf("failed to read revoked serial: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO revoked_serials (serial, node_id, revoked_at) VALUES (?, ?, ?)`,
		serial, nodeID, now); err != nil {
		return fmt.Errorf("failed to record revocation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit revocation: %w", err)
	}
	return nil
}

// IsCertRevoked is the authoritative revocation lookup, keyed by the serial a
// TLS handshake actually presents.
//
// It reads the append-only ledger, not node_certs: a node holds one row there,
// so rotating its certificate replaces the serial, and a lookup by the old
// serial would find nothing and answer "not revoked". Absence from the ledger is
// a genuine "not revoked"; a database error is returned as an error so the
// caller can fail closed.
func (s *SQLiteCertStore) IsCertRevoked(ctx context.Context, serial string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return false, errors.New("cert store is closed")
	}

	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM revoked_serials WHERE serial = ?`, serial).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check revocation: %w", err)
	}
	return true, nil
}

// RevokedSerials lists the revocation ledger so a starting coordinator can warm
// its in-memory blocklist from the authoritative source.
func (s *SQLiteCertStore) RevokedSerials(ctx context.Context) ([]RevokedCertRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("cert store is closed")
	}

	rows, err := s.db.QueryContext(ctx, `SELECT serial, node_id, revoked_at FROM revoked_serials ORDER BY revoked_at, serial`)
	if err != nil {
		return nil, fmt.Errorf("failed to list revoked serials: %w", err)
	}
	defer rows.Close()

	var out []RevokedCertRecord
	for rows.Next() {
		var rec RevokedCertRecord
		var revokedAt int64
		if err := rows.Scan(&rec.Serial, &rec.NodeID, &revokedAt); err != nil {
			return nil, fmt.Errorf("failed to scan revoked serial: %w", err)
		}
		rec.RevokedAt = time.Unix(revokedAt, 0)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list revoked serials: %w", err)
	}
	return out, nil
}

// DeleteCert removes a certificate record.
func (s *SQLiteCertStore) DeleteCert(ctx context.Context, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("cert store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `DELETE FROM node_certs WHERE node_id = ?`
	_, err := s.db.ExecContext(ctx, query, nodeID)
	if err != nil {
		return fmt.Errorf("failed to delete cert: %w", err)
	}

	return nil
}

// Close closes the certificate store.
func (s *SQLiteCertStore) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.closed = true
	return nil
}

// CertRecordToJSON converts a CertRecord to JSON.
func CertRecordToJSON(cert *CertRecord) ([]byte, error) {
	return json.Marshal(cert)
}

// JSONToCertRecord parses JSON into a CertRecord.
func JSONToCertRecord(data []byte) (*CertRecord, error) {
	var cert CertRecord
	if err := json.Unmarshal(data, &cert); err != nil {
		return nil, err
	}
	return &cert, nil
}
