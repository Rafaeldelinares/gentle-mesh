package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"

	_ "modernc.org/sqlite"
)

// insertTaskSQL inserts a task row with every NOT NULL column filled, for the
// raw handles that take the write lock.
const insertTaskSQL = `INSERT INTO tasks (
	task_id, branch, repository, domain, blast_radius, phase,
	current_action, status, worker_id, tags, edit_surfaces,
	created_at, started_at, finished_at, error_message, result_summary
) VALUES (?, 'main', 'repo', 'domain', 'none', 'analysis', 'working', 'running', 'worker-1', '[]', '[]', 0, 0, 0, '', '');`

// TestSQLiteStore_EveryPooledConnectionHasBusyTimeout pins the invariant that
// broke CI in PR #175 (#177): a SQLite pragma is per connection, so setting it
// with a one-off Exec only configures the connection that served that call.
// Every other connection in the pool kept busy_timeout = 0 and returned
// SQLITE_BUSY immediately instead of waiting for the write lock.
//
// The test grabs one pooled connection and asks for a second one while the
// first is held: the second is a brand new physical connection, so its
// busy_timeout is exactly the value the pool would hand to a concurrent
// writer.
func TestSQLiteStore_EveryPooledConnectionHasBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire first connection: %v", err)
	}
	defer held.Close()

	second, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire second connection: %v", err)
	}
	defer second.Close()

	var timeout int
	if err := second.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}

	if timeout != 5000 {
		t.Fatalf("a fresh pooled connection has busy_timeout=%d, want 5000: "+
			"the pragma was applied to one connection only, so concurrent writers "+
			"get SQLITE_BUSY instead of waiting for the write lock", timeout)
	}
}

// TestSQLiteStore_WaitsForWriteLockHeldByAnotherHandle is the black-box
// consequence: with another handle holding the write lock, a write through the
// store must wait for it (busy_timeout) instead of failing with SQLITE_BUSY.
func TestSQLiteStore_WaitsForWriteLockHeldByAnotherHandle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wait.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	// The second handle represents another process with the same configuration,
	// so it carries the same pragmas.
	other, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer other.Close()

	ctx := context.Background()

	// Hold one pooled connection so the write below lands on a brand new
	// physical connection — the one that used to have busy_timeout = 0.
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire held connection: %v", err)
	}
	defer held.Close()

	// The other handle takes the exclusive write lock and keeps it for a
	// moment.
	if _, err := other.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		t.Fatalf("begin immediate on other handle: %v", err)
	}
	if _, err := other.ExecContext(ctx, insertTaskSQL, "lock-holder"); err != nil {
		t.Fatalf("write on other handle: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = other.Exec("COMMIT;")
		close(released)
	}()
	defer func() {
		<-released
	}()

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- store.SaveTask(context.Background(), &TaskRecord{
			TaskID:    "task-waits",
			CreatedAt: time.Now().UnixNano(),
			Status:    protocol.TaskStatusRunning,
		})
	}()

	select {
	case err := <-writeErr:
		if err != nil {
			if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
				t.Fatalf("write failed with SQLITE_BUSY instead of waiting for the write lock: %v", err)
			}
			t.Fatalf("write failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("write never completed: it should have waited for the other handle to release the lock")
	}
}

// TestSQLiteStore_DSNHandlesSpecialCharactersInPath makes sure the DSN form
// used to carry the pragmas does not break on paths that SQLite would
// otherwise parse as URI syntax.
func TestSQLiteStore_DSNHandlesSpecialCharactersInPath(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"plain.db",
		"with space.db",
		"with?question.db",
		"with#hash.db",
		"with%percent.db",
	} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(dir, name)
			store, err := NewSQLiteStore(dbPath)
			if err != nil {
				t.Fatalf("NewSQLiteStore(%q): %v", dbPath, err)
			}
			defer store.Close()

			if err := store.SaveTask(context.Background(), &TaskRecord{
				TaskID:    "task-1",
				CreatedAt: time.Now().UnixNano(),
				Status:    protocol.TaskStatusRunning,
			}); err != nil {
				t.Fatalf("write on %q: %v", dbPath, err)
			}

			if _, err := os.Stat(dbPath); err != nil {
				t.Fatalf("expected the database file at %q: %v", dbPath, err)
			}
		})
	}
}

// TestSQLiteStore_ConcurrentWritersDoNotGetBusy keeps the regression honest:
// concurrent writers across two handles on the same file must not surface
// SQLITE_BUSY to the caller.
func TestSQLiteStore_ConcurrentWritersDoNotGetBusy(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "concurrent.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	// The second handle represents another process with the same configuration,
	// so it carries the same pragmas.
	other, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer other.Close()

	ctx := context.Background()

	const writers = 8
	const perWriter = 10

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if id%2 == 0 {
					if err := store.SaveTask(ctx, &TaskRecord{
						TaskID:    "store-task-" + string(rune('a'+id)) + string(rune('0'+i)),
						CreatedAt: time.Now().UnixNano(),
						Status:    protocol.TaskStatusRunning,
					}); err != nil {
						errs <- err
						return
					}
					continue
				}
				_, err := other.ExecContext(ctx, insertTaskSQL,
					"other-task-"+string(rune('a'+id))+string(rune('0'+i)))
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		t.Fatalf("concurrent write failed: %v", err)
	}
}

// TestSQLiteStore_InMemoryKeepsOneConnection guards the in-memory path: an
// in-memory database lives inside its connection, so the pool must not hand
// out more than one.
func TestSQLiteStore_InMemoryKeepsOneConnection(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore(:memory:): %v", err)
	}
	defer store.Close()

	if err := store.SaveTask(context.Background(), &TaskRecord{
		TaskID:    "task-memory",
		CreatedAt: time.Now().UnixNano(),
		Status:    protocol.TaskStatusRunning,
	}); err != nil {
		t.Fatalf("write on in-memory store: %v", err)
	}

	got, err := store.GetTask(context.Background(), "task-memory")
	if err != nil {
		t.Fatalf("read back from in-memory store: %v", err)
	}
	if got == nil || got.TaskID != "task-memory" {
		t.Fatalf("expected the task to be readable from the same in-memory database, got %+v", got)
	}
}
