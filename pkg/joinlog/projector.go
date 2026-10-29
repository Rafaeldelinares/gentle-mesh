package joinlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// TerritoryHistoryEntry records a released territory lock in the projection.
// Every field comes from the event log itself (including EventTime, which is
// the immutable e.CreatedAt of the event, never time.Now() during projection).
type TerritoryHistoryEntry struct {
	TaskID       string        `json:"task_id"`
	TerritoryKey string        `json:"territory_key"`
	Reason       ReleaseReason `json:"reason"`
	EventTime    time.Time     `json:"event_time"`
}

// ActiveTerritoryLock records a currently held lock in the projection.
type ActiveTerritoryLock struct {
	TerritoryKey string    `json:"territory_key"`
	TaskID       string    `json:"task_id"`
	ClaimedAt    time.Time `json:"claimed_at"`
}

// TerritoryVerification is the projected evidence of an opportunistic territory
// verification (RFC-003 J5). It is additive: it never replaces the claim or the
// release of the territory it refers to.
type TerritoryVerification struct {
	TaskID           string          `json:"task_id"`
	GitRepo          string          `json:"git_repo"`
	GitBranch        string          `json:"git_branch"`
	DeclaredSurfaces []string        `json:"declared_surfaces"`
	ObservedFiles    []string        `json:"observed_files"`
	Discrepancies    []string        `json:"discrepancies"`
	Status           TerritoryStatus `json:"status"`
	VerifiedAt       time.Time       `json:"verified_at"`
}

// MeshState holds the projected state of the mesh derived purely from the event log.
// All fields are deterministic: two projections of the same log yield identical
// MeshState values regardless of when or where they were executed.
type MeshState struct {
	Nodes                 map[string]NodeJoinedPayload   `json:"nodes"`
	Tasks                 map[string]TaskCreatedPayload  `json:"tasks"`
	ActiveLocks           map[string]ActiveTerritoryLock `json:"active_locks"`
	SettledTasks          map[string]TaskSettledPayload  `json:"settled_tasks"`
	TerritoryHistory      []TerritoryHistoryEntry        `json:"territory_history"`
	TerritoryVerifications []TerritoryVerification       `json:"territory_verifications"`
}

// Projector consumes events from a joinlog.Log and projects them into a SQLite
// database or in-memory view. It is completely deterministic and never uses the
// local wall-clock (RFC-003 J6, issue #169).
type Projector struct {
	db *sql.DB
}

// NewProjector creates a new Projector backed by SQLite at dbPath.
func NewProjector(dbPath string) (*Projector, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create projector dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open projector db: %w", err)
	}
	p := &Projector{db: db}
	if err := p.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

// Close closes the underlying database.
func (p *Projector) Close() error {
	return p.db.Close()
}

func (p *Projector) initSchema(ctx context.Context) error {
	// The schema records ONLY information derived from the event log.
	// No DEFAULT CURRENT_TIMESTAMP: all timestamps are the event's CreatedAt (RFC-003 J6).
	const schema = `
CREATE TABLE IF NOT EXISTS proj_nodes (
	node_id TEXT PRIMARY KEY,
	role TEXT NOT NULL,
	endpoint TEXT NOT NULL,
	tags TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS proj_tasks (
	task_id TEXT PRIMARY KEY,
	git_repo TEXT NOT NULL,
	git_branch TEXT NOT NULL,
	prompt TEXT NOT NULL,
	verdict TEXT,
	receipt_hash TEXT
);

CREATE TABLE IF NOT EXISTS proj_territory_locks (
	territory_key TEXT PRIMARY KEY, -- "repo:branch"
	task_id TEXT NOT NULL,
	claimed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proj_territory_history (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id TEXT NOT NULL,
	territory_key TEXT NOT NULL,
	reason TEXT NOT NULL,
	released_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proj_territory_verifications (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id TEXT NOT NULL,
	git_repo TEXT NOT NULL,
	git_branch TEXT NOT NULL,
	declared_surfaces TEXT NOT NULL,
	observed_files TEXT NOT NULL,
	discrepancies TEXT NOT NULL,
	status TEXT NOT NULL,
	verified_at TEXT NOT NULL
);
`
	if _, err := p.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("projector init schema: %w", err)
	}
	return nil
}

// ProjectEvent applies a single event to the projection database deterministically.
// All timestamps stored come directly from e.CreatedAt; time.Now() is never called.
func (p *Projector) ProjectEvent(ctx context.Context, e Event) error {
	eventTimestamp := e.CreatedAt.UTC().Format(time.RFC3339Nano)

	switch e.Type {
	case EventTypeNodeJoined:
		var payload NodeJoinedPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal node.joined payload: %w", err)
		}
		tagsJSON, err := json.Marshal(payload.Tags)
		if err != nil {
			tagsJSON = []byte("[]")
		}
		_, err = p.db.ExecContext(ctx,
			`INSERT INTO proj_nodes (node_id, role, endpoint, tags)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(node_id) DO UPDATE SET
			   role = excluded.role,
			   endpoint = excluded.endpoint,
			   tags = excluded.tags;`,
			payload.NodeID, payload.Role, payload.Endpoint, string(tagsJSON))
		if err != nil {
			return fmt.Errorf("project node.joined: %w", err)
		}

	case EventTypeTaskCreated:
		var payload TaskCreatedPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal task.created payload: %w", err)
		}
		_, err := p.db.ExecContext(ctx,
			`INSERT INTO proj_tasks (task_id, git_repo, git_branch, prompt)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(task_id) DO UPDATE SET
			   git_repo = excluded.git_repo,
			   git_branch = excluded.git_branch,
			   prompt = excluded.prompt;`,
			payload.TaskID, payload.GitRepo, payload.GitBranch, payload.Prompt)
		if err != nil {
			return fmt.Errorf("project task.created: %w", err)
		}

	case EventTypeTerritoryClaimed:
		var payload TerritoryClaimedPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal territory.claimed payload: %w", err)
		}
		territoryKey := fmt.Sprintf("%s:%s", payload.GitRepo, payload.GitBranch)
		_, err := p.db.ExecContext(ctx,
			`INSERT INTO proj_territory_locks (territory_key, task_id, claimed_at)
			 VALUES (?, ?, ?)
			 ON CONFLICT(territory_key) DO UPDATE SET
			   task_id = excluded.task_id,
			   claimed_at = excluded.claimed_at;`,
			territoryKey, payload.TaskID, eventTimestamp)
		if err != nil {
			return fmt.Errorf("project territory.claimed: %w", err)
		}

	case EventTypeTerritoryReleased:
		var payload TerritoryReleasedPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal territory.released payload: %w", err)
		}
		territoryKey := fmt.Sprintf("%s:%s", payload.GitRepo, payload.GitBranch)

		// 1. Remove from active locks
		if _, err := p.db.ExecContext(ctx,
			`DELETE FROM proj_territory_locks WHERE territory_key = ?;`,
			territoryKey); err != nil {
			return fmt.Errorf("delete territory lock: %w", err)
		}

		// 2. Record in territory history with explicit reason from event payload
		if _, err := p.db.ExecContext(ctx,
			`INSERT INTO proj_territory_history (task_id, territory_key, reason, released_at)
			 VALUES (?, ?, ?, ?);`,
			payload.TaskID, territoryKey, string(payload.Reason), eventTimestamp); err != nil {
			return fmt.Errorf("insert territory history: %w", err)
		}

	case EventTypeTaskSettled:
		var payload TaskSettledPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal task.settled payload: %w", err)
		}
		_, err := p.db.ExecContext(ctx,
			`UPDATE proj_tasks SET verdict = ?, receipt_hash = ? WHERE task_id = ?;`,
			payload.Verdict, payload.ReceiptHash, payload.TaskID)
		if err != nil {
			return fmt.Errorf("project task.settled: %w", err)
		}

	case EventTypeTerritoryVerified:
		// Evidence only: it never touches proj_territory_locks (the claim and
		// the release keep their own meaning) nor proj_territory_history.
		var payload TerritoryVerifiedPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal territory.verified payload: %w", err)
		}
		declaredJSON, err := json.Marshal(payload.DeclaredSurfaces)
		if err != nil {
			return fmt.Errorf("marshal declared surfaces: %w", err)
		}
		observedJSON, err := json.Marshal(payload.ObservedFiles)
		if err != nil {
			return fmt.Errorf("marshal observed files: %w", err)
		}
		discrepanciesJSON, err := json.Marshal(payload.Discrepancies)
		if err != nil {
			return fmt.Errorf("marshal discrepancies: %w", err)
		}
		if _, err := p.db.ExecContext(ctx,
			`INSERT INTO proj_territory_verifications
			   (task_id, git_repo, git_branch, declared_surfaces, observed_files, discrepancies, status, verified_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?);`,
			payload.TaskID, payload.GitRepo, payload.GitBranch,
			string(declaredJSON), string(observedJSON), string(discrepanciesJSON),
			string(payload.Status), eventTimestamp); err != nil {
			return fmt.Errorf("project territory.verified: %w", err)
		}

	default:
		// Unknown event types are tolerated without breaking replay (forward compatibility)
	}

	return nil
}

// Replay reads the entire log from the beginning and projects every event in chain order.
func (p *Projector) Replay(ctx context.Context, l *Log) error {
	events, err := l.Events(ctx)
	if err != nil {
		return fmt.Errorf("replay read events: %w", err)
	}
	for _, e := range events {
		if err := p.ProjectEvent(ctx, e); err != nil {
			return fmt.Errorf("replay project event %s: %w", e.ID, err)
		}
	}
	return nil
}

// Snapshot returns the in-memory aggregated MeshState from the projection tables.
// All fields are populated so that two snapshots can be deeply compared.
func (p *Projector) Snapshot(ctx context.Context) (*MeshState, error) {
	state := &MeshState{
		Nodes:                  make(map[string]NodeJoinedPayload),
		Tasks:                  make(map[string]TaskCreatedPayload),
		ActiveLocks:            make(map[string]ActiveTerritoryLock),
		SettledTasks:           make(map[string]TaskSettledPayload),
		TerritoryHistory:       make([]TerritoryHistoryEntry, 0),
		TerritoryVerifications: make([]TerritoryVerification, 0),
	}

	// 1. Nodes
	nodeRows, err := p.db.QueryContext(ctx, `SELECT node_id, role, endpoint, tags FROM proj_nodes ORDER BY node_id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("query proj_nodes: %w", err)
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var n NodeJoinedPayload
		var tagsJSON string
		if err := nodeRows.Scan(&n.NodeID, &n.Role, &n.Endpoint, &tagsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &n.Tags)
		state.Nodes[n.NodeID] = n
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}

	// 2. Tasks
	taskRows, err := p.db.QueryContext(ctx,
		`SELECT task_id, git_repo, git_branch, prompt, COALESCE(verdict, ''), COALESCE(receipt_hash, '')
		 FROM proj_tasks ORDER BY task_id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("query proj_tasks: %w", err)
	}
	defer taskRows.Close()
	for taskRows.Next() {
		var t TaskCreatedPayload
		var verdict, receiptHash string
		if err := taskRows.Scan(&t.TaskID, &t.GitRepo, &t.GitBranch, &t.Prompt, &verdict, &receiptHash); err != nil {
			return nil, err
		}
		state.Tasks[t.TaskID] = t
		if verdict != "" {
			state.SettledTasks[t.TaskID] = TaskSettledPayload{
				TaskID:      t.TaskID,
				Verdict:     verdict,
				ReceiptHash: receiptHash,
			}
		}
	}
	if err := taskRows.Err(); err != nil {
		return nil, err
	}

	// 3. Active territory locks
	lockRows, err := p.db.QueryContext(ctx,
		`SELECT territory_key, task_id, claimed_at FROM proj_territory_locks ORDER BY territory_key ASC;`)
	if err != nil {
		return nil, fmt.Errorf("query proj_territory_locks: %w", err)
	}
	defer lockRows.Close()
	for lockRows.Next() {
		var lock ActiveTerritoryLock
		var claimedAtStr string
		if err := lockRows.Scan(&lock.TerritoryKey, &lock.TaskID, &claimedAtStr); err != nil {
			return nil, err
		}
		if ts, err := time.Parse(time.RFC3339Nano, claimedAtStr); err == nil {
			lock.ClaimedAt = ts
		}
		state.ActiveLocks[lock.TerritoryKey] = lock
	}
	if err := lockRows.Err(); err != nil {
		return nil, err
	}

	// 4. Territory history
	histRows, err := p.db.QueryContext(ctx,
		`SELECT task_id, territory_key, reason, released_at FROM proj_territory_history ORDER BY id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("query proj_territory_history: %w", err)
	}
	defer histRows.Close()
	for histRows.Next() {
		var h TerritoryHistoryEntry
		var releasedAtStr string
		if err := histRows.Scan(&h.TaskID, &h.TerritoryKey, &h.Reason, &releasedAtStr); err != nil {
			return nil, err
		}
		if ts, err := time.Parse(time.RFC3339Nano, releasedAtStr); err == nil {
			h.EventTime = ts
		}
		state.TerritoryHistory = append(state.TerritoryHistory, h)
	}
	if err := histRows.Err(); err != nil {
		return nil, err
	}

	// 5. Territory verifications (evidence, additive)
	verifRows, err := p.db.QueryContext(ctx,
		`SELECT task_id, git_repo, git_branch, declared_surfaces, observed_files, discrepancies, status, verified_at
		 FROM proj_territory_verifications ORDER BY id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("query proj_territory_verifications: %w", err)
	}
	defer verifRows.Close()
	for verifRows.Next() {
		var v TerritoryVerification
		var declaredJSON, observedJSON, discrepanciesJSON, verifiedAtStr string
		if err := verifRows.Scan(&v.TaskID, &v.GitRepo, &v.GitBranch, &declaredJSON, &observedJSON,
			&discrepanciesJSON, &v.Status, &verifiedAtStr); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(declaredJSON), &v.DeclaredSurfaces)
		_ = json.Unmarshal([]byte(observedJSON), &v.ObservedFiles)
		_ = json.Unmarshal([]byte(discrepanciesJSON), &v.Discrepancies)
		if v.DeclaredSurfaces == nil {
			v.DeclaredSurfaces = []string{}
		}
		if v.ObservedFiles == nil {
			v.ObservedFiles = []string{}
		}
		if v.Discrepancies == nil {
			v.Discrepancies = []string{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, verifiedAtStr); err == nil {
			v.VerifiedAt = ts
		}
		state.TerritoryVerifications = append(state.TerritoryVerifications, v)
	}
	if err := verifRows.Err(); err != nil {
		return nil, err
	}

	return state, nil
}
