package joinlog_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/joinlog"
)

// TestSituatedSubscriber_SnapshotAndStream verifies that a new node or reconnecting worker:
// 1. Fetches GET /v1/mesh/snapshot
// 2. Verifies the snapshot digest (SHA256(JCS(state)))
// 3. Connects to GET /v1/mesh/events with Last-Event-ID = <headSeq>:<headHash>
// 4. Receives streaming events published after the snapshot
func TestSituatedSubscriber_SnapshotAndStream(t *testing.T) {
	l, p, ts := setupTestServer(t)

	// Populate initial events before subscriber starts
	appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "coord", joinlog.NodeJoinedPayload{
		NodeID:   "coord",
		Role:     "coordinator",
		Endpoint: ts.URL,
	})
	appendTestEvent(t, l, p, joinlog.EventTypeTaskCreated, "coord", joinlog.TaskCreatedPayload{
		TaskID:    "task-init-1",
		GitRepo:   "https://github.com/org/repo",
		GitBranch: "main",
		Prompt:    "Initial task before snapshot",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub := joinlog.NewSituatedSubscriber(joinlog.SubscriberConfig{
		CoordinatorURL: ts.URL,
		HTTPClient:     ts.Client(),
	})

	// Perform situated catch-up
	snap, err := sub.CatchUp(ctx)
	if err != nil {
		t.Fatalf("CatchUp failed: %v", err)
	}

	if snap.HeadEventSeq != 2 {
		t.Fatalf("expected snapshot head seq 2, got %d", snap.HeadEventSeq)
	}
	if len(snap.State.Tasks) != 1 {
		t.Fatalf("expected 1 task in snapshot state, got %d", len(snap.State.Tasks))
	}

	// Start streaming subscription
	eventCh := make(chan joinlog.Event, 10)
	errCh := make(chan error, 1)

	go func() {
		errCh <- sub.SubscribeEvents(ctx, snap.HeadEventSeq, snap.HeadEventHash, func(evt joinlog.Event) {
			eventCh <- evt
		})
	}()

	// Give subscription a moment to connect and receive stream
	time.Sleep(50 * time.Millisecond)

	// Emit a new event on the coordinator after snapshot was taken
	appendTestEvent(t, l, p, joinlog.EventTypeTaskCreated, "coord", joinlog.TaskCreatedPayload{
		TaskID:    "task-streamed-2",
		GitRepo:   "https://github.com/org/repo",
		GitBranch: "main",
		Prompt:    "Streamed task after snapshot",
	})

	select {
	case evt := <-eventCh:
		if evt.Type != joinlog.EventTypeTaskCreated {
			t.Fatalf("expected task.created event, got %s", evt.Type)
		}
		if evt.Seq != 3 {
			t.Fatalf("expected event seq 3, got %d", evt.Seq)
		}
	case err := <-errCh:
		t.Fatalf("unexpected stream error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for streamed event")
	}
}

// TestSituatedSubscriber_TamperedSnapshotDigestFails verifies that if the server
// returns a corrupted or tampered state_digest, CatchUp fails with ErrInvalidSnapshotDigest.
func TestSituatedSubscriber_TamperedSnapshotDigestFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/mesh/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Send a tampered snapshot where state_digest is wrong
		fmt.Fprintf(w, `{"head_event_seq":1,"head_event_hash":"abc","state":{"nodes":{},"tasks":{},"active_locks":{},"settled_tasks":{},"territory_history":[],"territory_verifications":[]},"state_digest":"bad-digest"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := joinlog.NewSituatedSubscriber(joinlog.SubscriberConfig{
		CoordinatorURL: ts.URL,
		HTTPClient:     ts.Client(),
	})

	_, err := sub.CatchUp(ctx)
	if err == nil {
		t.Fatal("expected error on tampered snapshot, got nil")
	}
	if !errors.Is(err, joinlog.ErrInvalidSnapshotDigest) {
		t.Fatalf("expected ErrInvalidSnapshotDigest, got: %v", err)
	}
}

// TestSituatedSubscriber_DivergentChain409Abort verifies that if the server responds
// with HTTP 409 Conflict (chain divergence detected), SubscribeEvents aborts immediately
// with ErrChainDivergence instead of continuing silently.
func TestSituatedSubscriber_DivergentChain409Abort(t *testing.T) {
	l, p, ts := setupTestServer(t)

	appendTestEvent(t, l, p, joinlog.EventTypeNodeJoined, "coord", joinlog.NodeJoinedPayload{
		NodeID:   "coord",
		Role:     "coordinator",
		Endpoint: ts.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := joinlog.NewSituatedSubscriber(joinlog.SubscriberConfig{
		CoordinatorURL: ts.URL,
		HTTPClient:     ts.Client(),
	})

	// Pass a bogus hash at seq 1 to trigger 409 Conflict from the server
	err := sub.SubscribeEvents(ctx, 1, "divergent-nonexistent-hash", func(evt joinlog.Event) {})
	if err == nil {
		t.Fatal("expected error on 409 divergence, got nil")
	}
	if !errors.Is(err, joinlog.ErrChainDivergence) {
		t.Fatalf("expected ErrChainDivergence, got: %v", err)
	}
}
