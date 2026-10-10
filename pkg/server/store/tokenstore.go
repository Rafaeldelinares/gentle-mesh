package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TokenStore defines the interface for managing enrollment tokens.
type TokenStore interface {
	// CreateToken creates a new enrollment token.
	CreateToken(ctx context.Context, token *TokenRecord) error
	// GetToken retrieves a token by value.
	GetToken(ctx context.Context, token string) (*TokenRecord, error)
	// UseToken marks a token as used and returns it.
	UseToken(ctx context.Context, token string) (*TokenRecord, error)
	// DeleteToken removes a token.
	DeleteToken(ctx context.Context, token string) error
	// ListTokens returns all tokens.
	ListTokens(ctx context.Context) ([]*TokenRecord, error)
	// Close closes the token store.
	Close() error
}

// TokenRecord represents an enrollment token.
type TokenRecord struct {
	Token     string
	CreatedAt time.Time
	ExpiresAt *time.Time
	UsedAt    *time.Time
	UsedBy    *string
	MaxUses   int
	Uses      int
}

// SQLiteTokenStore implements TokenStore using SQLite.
type SQLiteTokenStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	closeMu sync.RWMutex
	closed  bool
}

// NewSQLiteTokenStore creates a new SQLite token store.
func NewSQLiteTokenStore(db *sql.DB) *SQLiteTokenStore {
	return &SQLiteTokenStore{db: db}
}

// InitTokenSchema initializes the tokens table schema.
func InitTokenSchema(db *sql.DB) error {
	ddl := `CREATE TABLE IF NOT EXISTS enrollment_tokens (
		token TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL,
		expires_at INTEGER,
		used_at INTEGER,
		used_by TEXT,
		max_uses INTEGER NOT NULL DEFAULT 1,
		uses INTEGER NOT NULL DEFAULT 0
	);`

	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("failed to initialize token schema: %w", err)
	}

	return nil
}

// CreateToken creates a new enrollment token.
func (s *SQLiteTokenStore) CreateToken(ctx context.Context, token *TokenRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if token == nil {
		return errors.New("token cannot be nil")
	}
	if token.Token == "" {
		return errors.New("token value cannot be empty")
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("token store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `INSERT INTO enrollment_tokens 
		(token, created_at, expires_at, max_uses, uses, used_by) 
		VALUES (?, ?, ?, ?, ?, NULL)`

	var expiresAt *int64
	if token.ExpiresAt != nil {
		v := token.ExpiresAt.Unix()
		expiresAt = &v
	}

	_, err := s.db.ExecContext(ctx, query,
		token.Token,
		token.CreatedAt.Unix(),
		expiresAt,
		token.MaxUses,
		0,
	)
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	return nil
}

// GetToken retrieves a token by value.
func (s *SQLiteTokenStore) GetToken(ctx context.Context, token string) (*TokenRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("token store is closed")
	}

	return s.getTokenLocked(ctx, token)
}

// getTokenLocked loads one token row. The caller must already hold closeMu
// for reading, which is why it is separate from GetToken.
func (s *SQLiteTokenStore) getTokenLocked(ctx context.Context, token string) (*TokenRecord, error) {
	query := `SELECT token, created_at, expires_at, used_at, used_by, max_uses, uses 
		FROM enrollment_tokens WHERE token = ?`

	row := s.db.QueryRowContext(ctx, query, token)

	var rec TokenRecord
	var createdAt int64
	var expiresAt, usedAt *int64

	err := row.Scan(
		&rec.Token,
		&createdAt,
		&expiresAt,
		&usedAt,
		&rec.UsedBy,
		&rec.MaxUses,
		&rec.Uses,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	rec.CreatedAt = time.Unix(createdAt, 0)
	if expiresAt != nil {
		t := time.Unix(*expiresAt, 0)
		rec.ExpiresAt = &t
	}
	if usedAt != nil {
		t := time.Unix(*usedAt, 0)
		rec.UsedAt = &t
	}

	return &rec, nil
}

// UseToken consumes one use of a token.
//
// The UPDATE statement is the only gate: it performs the check and the
// increment together, so two concurrent callers cannot both act on a stale
// "uses < max_uses" read. When nothing was consumed the row is read again
// only to choose the error message.
func (s *SQLiteTokenStore) UseToken(ctx context.Context, token string) (*TokenRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("token store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().Unix()

	// expires_at NULL means the token never expires, and it stays valid at
	// exactly expires_at, matching the previous behaviour.
	// max_uses = 0 keeps its previous effective behaviour: `uses < max_uses`
	// is false from the start, so such a token can never be consumed.
	// used_by is passed as NULL so COALESCE keeps whatever is stored: UseToken
	// never sets it.
	query := `UPDATE enrollment_tokens 
		SET uses = uses + 1, used_at = ?, used_by = COALESCE(?, used_by) 
		WHERE token = ? AND uses < max_uses AND (expires_at IS NULL OR expires_at >= ?)`

	res, err := s.db.ExecContext(ctx, query, now, nil, token, now)
	if err != nil {
		return nil, fmt.Errorf("failed to use token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to use token: %w", err)
	}

	if affected == 0 {
		rec, err := s.getTokenLocked(ctx, token)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, errors.New("token not found")
		}
		if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
			return nil, errors.New("token expired")
		}
		return nil, errors.New("token max uses exceeded")
	}

	rec, err := s.getTokenLocked(ctx, token)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, errors.New("token not found")
	}

	return rec, nil
}

// DeleteToken removes a token.
func (s *SQLiteTokenStore) DeleteToken(ctx context.Context, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return errors.New("token store is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `DELETE FROM enrollment_tokens WHERE token = ?`
	_, err := s.db.ExecContext(ctx, query, token)
	if err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}

	return nil
}

// ListTokens returns all tokens.
func (s *SQLiteTokenStore) ListTokens(ctx context.Context) ([]*TokenRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed || s.db == nil {
		return nil, errors.New("token store is closed")
	}

	query := `SELECT token, created_at, expires_at, used_at, used_by, max_uses, uses 
		FROM enrollment_tokens ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tokens: %w", err)
	}
	defer rows.Close()

	var tokens []*TokenRecord
	for rows.Next() {
		var rec TokenRecord
		var createdAt int64
		var expiresAt, usedAt *int64

		err := rows.Scan(
			&rec.Token,
			&createdAt,
			&expiresAt,
			&usedAt,
			&rec.UsedBy,
			&rec.MaxUses,
			&rec.Uses,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan token: %w", err)
		}

		rec.CreatedAt = time.Unix(createdAt, 0)
		if expiresAt != nil {
			t := time.Unix(*expiresAt, 0)
			rec.ExpiresAt = &t
		}
		if usedAt != nil {
			t := time.Unix(*usedAt, 0)
			rec.UsedAt = &t
		}

		tokens = append(tokens, &rec)
	}

	return tokens, nil
}

// Close closes the token store.
func (s *SQLiteTokenStore) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.closed = true
	return nil
}
