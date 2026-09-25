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
	// GetCert retrieves a certificate by node ID.
	GetCert(ctx context.Context, nodeID string) (*CertRecord, error)
	// ListCerts returns all issued certificates.
	ListCerts(ctx context.Context) ([]*CertRecord, error)
	// RevokeCert marks a certificate as revoked.
	RevokeCert(ctx context.Context, nodeID string) error
	// IsCertRevoked checks if a certificate is revoked.
	IsCertRevoked(ctx context.Context, serial string) (bool, error)
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
	KeyPEM     string // Only stored if needed, or could be empty for security
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Revoked    bool
	RevokedAt  *time.Time
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
			key_pem TEXT,
			issued_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0,
			revoked_at INTEGER
		);`,
		`CREATE INDEX IF NOT EXISTS idx_certs_serial ON node_certs(serial);`,
		`CREATE INDEX IF NOT EXISTS idx_certs_revoked ON node_certs(revoked);`,
	}

	for _, stmt := range ddlStatements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to initialize cert schema: %w", err)
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
		node_id, common_name, serial, cert_pem, key_pem,
		issued_at, expires_at, revoked
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(node_id) DO UPDATE SET
		common_name = excluded.common_name,
		serial = excluded.serial,
		cert_pem = excluded.cert_pem,
		key_pem = excluded.key_pem,
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
		cert.KeyPEM,
		cert.IssuedAt.Unix(),
		cert.ExpiresAt.Unix(),
		0, // Not revoked on issue
	)
	if err != nil {
		return fmt.Errorf("failed to issue cert: %w", err)
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

	query := `SELECT node_id, common_name, serial, cert_pem, key_pem, 
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
		&cert.KeyPEM,
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

	query := `SELECT node_id, common_name, serial, cert_pem, key_pem,
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
			&cert.KeyPEM,
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

	query := `UPDATE node_certs SET revoked = 1, revoked_at = ? WHERE node_id = ?`
	result, err := s.db.ExecContext(ctx, query, now, nodeID)
	if err != nil {
		return fmt.Errorf("failed to revoke cert: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.New("certificate not found")
	}

	return nil
}

// IsCertRevoked checks if a certificate is revoked by serial number.
func (s *SQLiteCertStore) IsCertRevoked(ctx context.Context, serial string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return false, errors.New("cert store is closed")
	}

	query := `SELECT revoked FROM node_certs WHERE serial = ?`
	row := s.db.QueryRowContext(ctx, query, serial)

	var revoked int
	err := row.Scan(&revoked)
	if err == sql.ErrNoRows {
		return false, nil // Unknown cert, treat as not revoked
	}
	if err != nil {
		return false, fmt.Errorf("failed to check revocation: %w", err)
	}

	return revoked == 1, nil
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
