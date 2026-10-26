package joinlog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
)

// RFC-003 J2 (serialización sin colisión) requires that concurrent writers to
// the same log produce a single linear chain: no two events share the same
// predecessor, and every event references the hash of its immediate
// predecessor.
//
// This test verifies Option C (RFC-003 §7, issue #166): in-process mutex
// plus BEGIN IMMEDIATE on a dedicated connection, backed by a UNIQUE(prev_hash)
// constraint in SQLite.
func TestJoinLog_ConcurrentAppendsProduceOneLinearChain(t *testing.T) {
	ctx := context.Background()
	log, err := joinlog.New(filepath.Join(t.TempDir(), "join.db"))
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	const writers = 16
	const appendsPerWriter = 8

	var wg sync.WaitGroup
	errs := make(chan error, writers*appendsPerWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(actor int) {
			defer wg.Done()
			for i := 0; i < appendsPerWriter; i++ {
				payload, err := json.Marshal(map[string]any{
					"task_id": fmt.Sprintf("task-%d-%d", actor, i),
				})
				if err != nil {
					errs <- err
					return
				}
				if err := log.Append(ctx, joinlog.Event{
					Type:    "task.created",
					Actor:   fmt.Sprintf("actor-%d", actor),
					Payload: payload,
				}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append failed: %v", err)
	}

	events, err := log.Events(ctx)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if got, want := len(events), writers*appendsPerWriter; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}

	if err := joinlog.VerifyChain(events); err != nil {
		t.Fatalf("the join log chain must stay linear under concurrent appends: %v", err)
	}

	// Invariant check: exactly one genesis event (prev_hash == "") exists.
	genesisCount := 0
	for _, e := range events {
		if e.PrevHash == "" {
			genesisCount++
		}
	}
	if genesisCount != 1 {
		t.Fatalf("expected exactly 1 genesis event, got %d", genesisCount)
	}
}

// TestJoinLog_GenesisUniqueConstraint explicitly verifies that the schema's
// UNIQUE(prev_hash) constraint catches two genesis events (prev_hash == "")
// attempting to collide, closing the two-roots failure mode identified in RED.
func TestJoinLog_GenesisUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "genesis.db")
	log, err := joinlog.New(dbPath)
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	// 1. Normal genesis append via public API.
	err = log.Append(ctx, joinlog.Event{
		ID:      "genesis-1",
		Type:    "task.created",
		Actor:   "actor-1",
		Payload: json.RawMessage(`{"v":1}`),
	})
	if err != nil {
		t.Fatalf("genesis append failed: %v", err)
	}

	events, err := log.Events(ctx)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 || events[0].PrevHash != "" {
		t.Fatalf("expected 1 genesis event with empty prev_hash, got %+v", events)
	}

	// 2. Direct database insertion simulating a concurrent or rogue writer
	// that bypassed the in-process lock and attempts to insert a second
	// genesis event with prev_hash = "".
	rawDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx,
		`INSERT INTO join_log (id, seq, type, actor, payload, created_at, prev_hash, hash)
		 VALUES (?, 99, 'task.rogue', 'actor-rogue', '{}', ?, '', 'fakehash-second-genesis');`,
		"genesis-2", time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		t.Fatalf("expected UNIQUE constraint violation for duplicate genesis prev_hash='', but insert succeeded")
	}

	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("expected UNIQUE constraint error, got: %v", err)
	}
}

// TestJoinLog_ForkUniqueConstraint explicitly verifies that the schema's
// UNIQUE(prev_hash) constraint catches two events that share the same
// non-empty predecessor, closing the intermediate fork failure mode.
func TestJoinLog_ForkUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "fork.db")
	log, err := joinlog.New(dbPath)
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	// 1. Append genesis.
	if err := log.Append(ctx, joinlog.Event{
		ID:      "genesis",
		Type:    "task.created",
		Actor:   "actor-1",
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("append genesis: %v", err)
	}

	head, err := log.Head(ctx)
	if err != nil || head == "" {
		t.Fatalf("expected non-empty head, got %q (err: %v)", head, err)
	}

	// 2. Append first legitimate child of genesis.
	if err := log.Append(ctx, joinlog.Event{
		ID:      "child-1",
		Type:    "task.updated",
		Actor:   "actor-1",
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("append child-1: %v", err)
	}

	// 3. Attempt direct insert of child-2 also pointing to genesis (forking at genesis head).
	rawDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx,
		`INSERT INTO join_log (id, seq, type, actor, payload, created_at, prev_hash, hash)
		 VALUES ('child-2', 99, 'task.fork', 'actor-fork', '{}', ?, ?, 'fakehash-fork');`,
		time.Now().UTC().Format(time.RFC3339Nano), head)
	if err == nil {
		t.Fatalf("expected UNIQUE constraint violation when sharing prev_hash %q, but insert succeeded", head)
	}

	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("expected UNIQUE constraint error, got: %v", err)
	}
}

// TestJoinLog_CrossHandleSerialization verifies that BEGIN IMMEDIATE on dedicated
// connections correctly serializes writers across multiple distinct *joinlog.Log
// instances pointing to the same SQLite database file (without sharing l.mu).
func TestJoinLog_CrossHandleSerialization(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "multi_handle.db")

	const handleCount = 4
	const appendsPerHandle = 6

	handles := make([]*joinlog.Log, handleCount)
	for i := 0; i < handleCount; i++ {
		h, err := joinlog.New(dbPath)
		if err != nil {
			t.Fatalf("open handle %d: %v", i, err)
		}
		defer h.Close()
		handles[i] = h
	}

	var wg sync.WaitGroup
	errs := make(chan error, handleCount*appendsPerHandle)
	for hIdx, h := range handles {
		wg.Add(1)
		go func(handleID int, l *joinlog.Log) {
			defer wg.Done()
			for i := 0; i < appendsPerHandle; i++ {
				err := l.Append(ctx, joinlog.Event{
					Type:    "peer.joined",
					Actor:   fmt.Sprintf("handle-%d", handleID),
					Payload: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
				})
				if err != nil {
					errs <- fmt.Errorf("handle %d append %d: %w", handleID, i, err)
					return
				}
			}
		}(hIdx, h)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cross-handle append failed: %v", err)
	}

	events, err := handles[0].Events(ctx)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if got, want := len(events), handleCount*appendsPerHandle; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}
	if err := joinlog.VerifyChain(events); err != nil {
		t.Fatalf("chain corrupted across multiple handles: %v", err)
	}
}
