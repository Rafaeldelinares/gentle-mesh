// Package joinlog implements the append-only, hash-chained event log that
// holds the mesh's knowledge (RFC-003).
//
// This file holds the event format and the append path. The format mirrors the
// receipt chain already in the repository (RFC-002 S7): the event hash is the
// SHA-256 of the JCS canonical form (RFC 8785) of the event, and the link to
// the previous event is part of what is hashed, so an altered link is
// detectable.
//
// The append path implements Option C (RFC-003 §7, issue #166):
// in-process serialization plus BEGIN IMMEDIATE on a dedicated connection,
// backed by a UNIQUE(prev_hash) constraint in SQLite (including the empty
// string for genesis) as an invariant safeguard against forks.
package joinlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"

	_ "modernc.org/sqlite"
)

// Event is one entry of the mesh knowledge log.
//
// The format is deliberately small: it is the high-level event log of RFC-003
// (task created, territory declared, collision detected, task finished), not a
// universal reconstruction log (RFC-003 N3).
type Event struct {
	// ID uniquely identifies the event.
	ID string `json:"id"`
	// Type is the high-level event kind.
	Type string `json:"type"`
	// Actor is the identity that authored the event.
	Actor string `json:"actor"`
	// Payload is the typed, event-specific data.
	Payload json.RawMessage `json:"payload"`
	// CreatedAt is informative metadata. It is never used to order the mesh:
	// the order of the log is the order of the chain (RFC-002 R6).
	CreatedAt time.Time `json:"created_at"`
	// PrevHash is the hash of the preceding event, empty for the first one.
	// It is part of what is hashed, so the link cannot be rewritten unnoticed.
	PrevHash string `json:"prev_hash,omitempty"`
	// Hash is the SHA-256 of the JCS canonical form of the event with the
	// hash field cleared.
	Hash string `json:"hash"`
}

// ComputeEventHash returns the SHA-256 (hex) of the JCS canonical form of the
// event, with the hash field cleared and the chain link kept.
func ComputeEventHash(e Event) (string, error) {
	if e.ID == "" && e.Type == "" && e.Actor == "" {
		return "", fmt.Errorf("event is empty")
	}
	cleared := e
	cleared.Hash = ""
	data, err := jcs.Marshal(&cleared)
	if err != nil {
		return "", fmt.Errorf("jcs marshal: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Log is the append-only hash-chained event log.
type Log struct {
	mu sync.Mutex
	db *sql.DB
}

// New opens (or creates) a join log backed by the SQLite database at path.
func New(path string) (*Log, error) {
	// The pragmas travel in the DSN because a SQLite pragma is per
	// connection: setting them with a one-off Exec would only configure
	// whichever pooled connection served that call, and the others would
	// fail with SQLITE_BUSY instead of waiting for the write lock.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open join log: %w", err)
	}
	l := &Log{db: db}
	if err := l.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the underlying database handle.
func (l *Log) Close() error { return l.db.Close() }

func (l *Log) initSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS join_log (
	id TEXT PRIMARY KEY,
	seq INTEGER NOT NULL,
	type TEXT NOT NULL,
	actor TEXT NOT NULL,
	payload TEXT NOT NULL,
	created_at TEXT NOT NULL,
	prev_hash TEXT NOT NULL DEFAULT '',
	hash TEXT NOT NULL,
	UNIQUE(prev_hash)
);
CREATE INDEX IF NOT EXISTS idx_join_log_seq ON join_log(seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_join_log_prev_hash ON join_log(prev_hash);
`
	if _, err := l.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("join log schema: %w", err)
	}
	return nil
}

// Head returns the hash of the last event in the log, or the empty string when
// the log is empty.
func (l *Log) Head(ctx context.Context) (string, error) {
	var head string
	err := l.db.QueryRowContext(ctx, `SELECT hash FROM join_log ORDER BY seq DESC LIMIT 1;`).Scan(&head)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("join log head: %w", err)
	}
	return head, nil
}

// Append writes one event to the log.
//
// Concurrency is handled by Option C (RFC-003 §7, issue #166):
// 1. In-process mutex (mu.Lock) serializes calls within this process.
// 2. A dedicated connection executes BEGIN IMMEDIATE, acquiring the write lock
//    before reading the current head. This ensures that concurrent connections
//    are serialized at the SQLite level.
// 3. The schema enforces UNIQUE(prev_hash) (including the empty string for the
//    genesis event), serving as an invariant safeguard against chain forks.
func (l *Log) Append(ctx context.Context, e Event) error {
	if e.ID == "" {
		id, err := newEventID()
		if err != nil {
			return err
		}
		e.ID = id
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	conn, err := l.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("join log acquire conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		return fmt.Errorf("join log busy timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return fmt.Errorf("join log begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK;")
		}
	}()

	var head string
	var nextSeq int64 = 1
	var lastSeq sql.NullInt64
	err = conn.QueryRowContext(ctx, `SELECT hash, seq FROM join_log ORDER BY seq DESC LIMIT 1;`).Scan(&head, &lastSeq)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("join log head: %w", err)
	}
	if err == sql.ErrNoRows {
		head = ""
		nextSeq = 1
	} else if lastSeq.Valid {
		nextSeq = lastSeq.Int64 + 1
	}

	e.PrevHash = head
	hash, err := ComputeEventHash(e)
	if err != nil {
		return err
	}
	e.Hash = hash

	payload := string(e.Payload)
	if payload == "" {
		payload = "{}"
	}

	_, err = conn.ExecContext(ctx,
		`INSERT INTO join_log (id, seq, type, actor, payload, created_at, prev_hash, hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?);`,
		e.ID, nextSeq, e.Type, e.Actor, payload, e.CreatedAt.UTC().Format(time.RFC3339Nano), e.PrevHash, e.Hash)
	if err != nil {
		return fmt.Errorf("join log append insert: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		return fmt.Errorf("join log commit: %w", err)
	}
	committed = true
	return nil
}

// Events returns every event in the log, in chain order.
func (l *Log) Events(ctx context.Context) ([]Event, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, type, actor, payload, created_at, prev_hash, hash FROM join_log ORDER BY seq ASC;`)
	if err != nil {
		return nil, fmt.Errorf("join log events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payload, createdAt string
		if err := rows.Scan(&e.ID, &e.Type, &e.Actor, &payload, &createdAt, &e.PrevHash, &e.Hash); err != nil {
			return nil, fmt.Errorf("join log scan: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		if ts, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			e.CreatedAt = ts
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("join log events: %w", err)
	}
	return events, nil
}

// ChainError describes a broken link in the chain.
type ChainError struct {
	// Index is the position of the offending event.
	Index int
	// EventID identifies the offending event.
	EventID string
	// Reason explains what is wrong.
	Reason string
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("join log chain broken at index %d (event %s): %s", e.Index, e.EventID, e.Reason)
}

// VerifyChain checks that the given events form a single linear chain: the
// first event has no predecessor, every other event references the hash of its
// immediate predecessor, no two events share a predecessor, and every event
// hash is the hash of its own content.
//
// It returns the first violation found, or nil when the chain is intact.
func VerifyChain(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	seen := make(map[string]int, len(events))
	prev := ""
	for i, e := range events {
		if e.PrevHash != prev {
			return &ChainError{
				Index:   i,
				EventID: e.ID,
				Reason:  fmt.Sprintf("prev_hash %q does not match the previous event hash %q", e.PrevHash, prev),
			}
		}
		if other, ok := seen[e.PrevHash]; ok && e.PrevHash != "" {
			return &ChainError{
				Index:   i,
				EventID: e.ID,
				Reason: fmt.Sprintf("forked chain: this event and the event at index %d share the same predecessor %q",
					other, e.PrevHash),
			}
		}
		seen[e.PrevHash] = i
		recomputed, err := ComputeEventHash(e)
		if err != nil {
			return &ChainError{Index: i, EventID: e.ID, Reason: err.Error()}
		}
		if recomputed != e.Hash {
			return &ChainError{
				Index:   i,
				EventID: e.ID,
				Reason:  fmt.Sprintf("event hash %q does not match its content (%q)", e.Hash, recomputed),
			}
		}
		prev = e.Hash
	}
	return nil
}

func newEventID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("join log event id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
