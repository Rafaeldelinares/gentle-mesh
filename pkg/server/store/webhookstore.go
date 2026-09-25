package store

import (
	"context"
	"database/sql"
	"time"
)

// WebhookRecord represents a registered webhook.
type WebhookRecord struct {
	ID        string
	URL       string
	Secret    string
	Events    []string // "task.completed", "task.failed", "task.timeout"
	CreatedAt time.Time
	Active    bool
}

// WebhookStore defines operations for webhook persistence.
type WebhookStore interface {
	CreateWebhook(ctx context.Context, webhook *WebhookRecord) error
	GetWebhook(ctx context.Context, id string) (*WebhookRecord, error)
	DeleteWebhook(ctx context.Context, id string) error
	ListWebhooks(ctx context.Context) ([]*WebhookRecord, error)
	ListActiveWebhooks(ctx context.Context, event string) ([]*WebhookRecord, error)
}

// SQLiteWebhookStore persists webhooks in SQLite.
type SQLiteWebhookStore struct {
	db *sql.DB
}

// NewSQLiteWebhookStore creates a new SQLite-backed webhook store.
func NewSQLiteWebhookStore(db *sql.DB) *SQLiteWebhookStore {
	return &SQLiteWebhookStore{db: db}
}

// InitWebhookSchema creates the webhooks table if it doesn't exist.
func InitWebhookSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS webhooks (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			secret TEXT,
			events TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1
		)
	`)
	return err
}

func (s *SQLiteWebhookStore) CreateWebhook(ctx context.Context, webhook *WebhookRecord) error {
	eventsStr := joinStrings(webhook.Events)
	createdAt := webhook.CreatedAt.Unix()
	activeVal := 0
	if webhook.Active {
		activeVal = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webhooks (id, url, secret, events, created_at, active) VALUES (?, ?, ?, ?, ?, ?)`,
		webhook.ID, webhook.URL, webhook.Secret, eventsStr, createdAt, activeVal)
	return err
}

func (s *SQLiteWebhookStore) GetWebhook(ctx context.Context, id string) (*WebhookRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, url, secret, events, created_at, active FROM webhooks WHERE id = ?`, id)

	var rec WebhookRecord
	var eventsStr string
	var createdAt int64
	err := row.Scan(&rec.ID, &rec.URL, &rec.Secret, &eventsStr, &createdAt, &rec.Active)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.Events = splitStrings(eventsStr)
	rec.CreatedAt = time.Unix(createdAt, 0)
	return &rec, nil
}

func (s *SQLiteWebhookStore) DeleteWebhook(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

func (s *SQLiteWebhookStore) ListWebhooks(ctx context.Context) ([]*WebhookRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, url, secret, events, created_at, active FROM webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []*WebhookRecord
	for rows.Next() {
		var rec WebhookRecord
		var eventsStr string
		var createdAt int64
		if err := rows.Scan(&rec.ID, &rec.URL, &rec.Secret, &eventsStr, &createdAt, &rec.Active); err != nil {
			return nil, err
		}
		rec.Events = splitStrings(eventsStr)
		rec.CreatedAt = time.Unix(createdAt, 0)
		webhooks = append(webhooks, &rec)
	}
	return webhooks, rows.Err()
}

func (s *SQLiteWebhookStore) ListActiveWebhooks(ctx context.Context, event string) ([]*WebhookRecord, error) {
	webhooks, err := s.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}

	var active []*WebhookRecord
	for _, w := range webhooks {
		if !w.Active {
			continue
		}
		for _, e := range w.Events {
			if e == event || e == "*" {
				active = append(active, w)
				break
			}
		}
	}
	return active, nil
}

// joinStrings joins a slice of strings with comma.
func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}

// splitStrings splits a comma-separated string.
func splitStrings(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range splitSimple(s, ",") {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// splitSimple splits without importing strings.
func splitSimple(s, sep string) []string {
	var result []string
	for {
		idx := indexOf(s, sep)
		if idx < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:idx])
		s = s[idx+len(sep):]
	}
	return result
}

func indexOf(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
