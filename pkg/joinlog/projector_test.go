package joinlog_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
)

// RFC-003 J6 (El log es la fuente de verdad) requires that the complete state
// of the mesh can be rebuilt from scratch by replaying the event log in order,
// yielding an identical state to the one maintained progressively in real-time.
//
// Crucially, this must include autonomous actions like lease expiration, which
// MUST be recorded as explicit events (e.g. territory.released with reason: "lease_expired")
// and NEVER inferred from wall-clock time during replay (issue #169).
func TestProjector_RebuildFromScratchProducesIdenticalState(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	logDBPath := filepath.Join(tempDir, "source_log.db")
	liveProjPath := filepath.Join(tempDir, "live_proj.db")
	replayProjPath := filepath.Join(tempDir, "replay_proj.db")

	log, err := joinlog.New(logDBPath)
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	liveProjector, err := joinlog.NewProjector(liveProjPath)
	if err != nil {
		t.Fatalf("open live projector: %v", err)
	}
	defer liveProjector.Close()

	// Helper to append an event to the log and immediately apply it to liveProjector.
	appendAndProject := func(eventType, actor string, payload any) {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		event := joinlog.Event{
			Type:      eventType,
			Actor:     actor,
			Payload:   data,
			CreatedAt: time.Now().UTC(),
		}
		if err := log.Append(ctx, event); err != nil {
			t.Fatalf("append event %s: %v", eventType, err)
		}
		// In live execution, the projector processes the event as it happens:
		if err := liveProjector.ProjectEvent(ctx, event); err != nil {
			t.Fatalf("live project event %s: %v", eventType, err)
		}
	}

	// 1. node.joined events
	appendAndProject(joinlog.EventTypeNodeJoined, "node-coord", joinlog.NodeJoinedPayload{
		NodeID:   "node-coord",
		Role:     "coordinator",
		Endpoint: "https://192.168.1.26:8630",
		Tags:     []string{"core", "coordinator"},
	})
	appendAndProject(joinlog.EventTypeNodeJoined, "w-dev-2", joinlog.NodeJoinedPayload{
		NodeID:   "w-dev-2",
		Role:     "worker",
		Endpoint: "https://127.0.0.1:8631",
		Tags:     []string{"go", "linux"},
	})

	// 2. task.created events
	appendAndProject(joinlog.EventTypeTaskCreated, "node-coord", joinlog.TaskCreatedPayload{
		TaskID:    "task-101",
		GitRepo:   "https://github.com/org/repo-a",
		GitBranch: "feature/auth",
		Prompt:    "Implement OAuth2",
	})
	appendAndProject(joinlog.EventTypeTaskCreated, "node-coord", joinlog.TaskCreatedPayload{
		TaskID:    "task-102",
		GitRepo:   "https://github.com/org/repo-b",
		GitBranch: "fix/leak",
		Prompt:    "Fix memory leak",
	})
	appendAndProject(joinlog.EventTypeTaskCreated, "node-coord", joinlog.TaskCreatedPayload{
		TaskID:    "task-103",
		GitRepo:   "https://github.com/org/repo-c",
		GitBranch: "main",
		Prompt:    "Long-running task currently active",
	})

	// 3. territory.claimed events
	appendAndProject(joinlog.EventTypeTerritoryClaimed, "task-101", joinlog.TerritoryClaimedPayload{
		TaskID:    "task-101",
		GitRepo:   "https://github.com/org/repo-a",
		GitBranch: "feature/auth",
	})
	appendAndProject(joinlog.EventTypeTerritoryClaimed, "task-102", joinlog.TerritoryClaimedPayload{
		TaskID:    "task-102",
		GitRepo:   "https://github.com/org/repo-b",
		GitBranch: "fix/leak",
	})
	appendAndProject(joinlog.EventTypeTerritoryClaimed, "task-103", joinlog.TerritoryClaimedPayload{
		TaskID:    "task-103",
		GitRepo:   "https://github.com/org/repo-c",
		GitBranch: "main",
	})

	// 4. territory.released: task-101 released normally upon settlement
	appendAndProject(joinlog.EventTypeTerritoryReleased, "task-101", joinlog.TerritoryReleasedPayload{
		TaskID:    "task-101",
		GitRepo:   "https://github.com/org/repo-a",
		GitBranch: "feature/auth",
		Reason:    joinlog.ReasonTaskFinished,
	})

	// 5. territory.released: task-102 lease expired by autonomous R2 reaper!
	// Emitted as an explicit event with ReasonLeaseExpired, NOT inferred from time.
	appendAndProject(joinlog.EventTypeTerritoryReleased, "reaper-daemon", joinlog.TerritoryReleasedPayload{
		TaskID:    "task-102",
		GitRepo:   "https://github.com/org/repo-b",
		GitBranch: "fix/leak",
		Reason:    joinlog.ReasonLeaseExpired,
	})

	// 6. task.settled event
	appendAndProject(joinlog.EventTypeTaskSettled, "node-coord", joinlog.TaskSettledPayload{
		TaskID:      "task-101",
		Verdict:     "SETTLED_CLEAN",
		ReceiptHash: "sha256-receipt-101-clean",
	})

	// Extract the live state (accumulated step-by-step)
	liveState, err := liveProjector.Snapshot(ctx)
	if err != nil {
		t.Fatalf("get live snapshot: %v", err)
	}

	// -------------------------------------------------------------
	// REPLAY PHASE: Create a fresh, empty projector and replay the log
	// -------------------------------------------------------------
	replayProjector, err := joinlog.NewProjector(replayProjPath)
	if err != nil {
		t.Fatalf("open replay projector: %v", err)
	}
	defer replayProjector.Close()

	if err := replayProjector.Replay(ctx, log); err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	replayState, err := replayProjector.Snapshot(ctx)
	if err != nil {
		t.Fatalf("get replay snapshot: %v", err)
	}

	// -------------------------------------------------------------
	// FIELD-BY-FIELD DETAILED EQUIVALENCE VERIFICATION (J6):
	// Check each table and field explicitly to ensure 100% equivalence.
	// -------------------------------------------------------------

	// A. Check Nodes
	if len(liveState.Nodes) != len(replayState.Nodes) {
		t.Fatalf("nodes count mismatch: live=%d, replay=%d", len(liveState.Nodes), len(replayState.Nodes))
	}
	for id, liveNode := range liveState.Nodes {
		replayNode, ok := replayState.Nodes[id]
		if !ok {
			t.Fatalf("node %q missing in replay", id)
		}
		if !reflect.DeepEqual(liveNode, replayNode) {
			t.Fatalf("node %q field mismatch:\nLive:   %+v\nReplay: %+v", id, liveNode, replayNode)
		}
	}

	// B. Check Tasks
	if len(liveState.Tasks) != len(replayState.Tasks) {
		t.Fatalf("tasks count mismatch: live=%d, replay=%d", len(liveState.Tasks), len(replayState.Tasks))
	}
	for id, liveTask := range liveState.Tasks {
		replayTask, ok := replayState.Tasks[id]
		if !ok {
			t.Fatalf("task %q missing in replay", id)
		}
		if !reflect.DeepEqual(liveTask, replayTask) {
			t.Fatalf("task %q field mismatch:\nLive:   %+v\nReplay: %+v", id, liveTask, replayTask)
		}
	}

	// C. Check Settled Tasks
	if len(liveState.SettledTasks) != len(replayState.SettledTasks) {
		t.Fatalf("settled tasks count mismatch: live=%d, replay=%d", len(liveState.SettledTasks), len(replayState.SettledTasks))
	}
	for id, liveSettled := range liveState.SettledTasks {
		replaySettled, ok := replayState.SettledTasks[id]
		if !ok {
			t.Fatalf("settled task %q missing in replay", id)
		}
		if !reflect.DeepEqual(liveSettled, replaySettled) {
			t.Fatalf("settled task %q field mismatch:\nLive:   %+v\nReplay: %+v", id, liveSettled, replaySettled)
		}
	}

	// D. Check Active Territory Locks (task-103 still held; task-101 & task-102 released)
	if len(liveState.ActiveLocks) != len(replayState.ActiveLocks) {
		t.Fatalf("active locks count mismatch: live=%d, replay=%d", len(liveState.ActiveLocks), len(replayState.ActiveLocks))
	}
	if len(replayState.ActiveLocks) != 1 {
		t.Fatalf("expected exactly 1 active lock (task-103), got %d: %+v", len(replayState.ActiveLocks), replayState.ActiveLocks)
	}
	for key, liveLock := range liveState.ActiveLocks {
		replayLock, ok := replayState.ActiveLocks[key]
		if !ok {
			t.Fatalf("active lock for territory %q missing in replay", key)
		}
		if liveLock.TaskID != replayLock.TaskID {
			t.Fatalf("lock for %q task_id mismatch: live=%q, replay=%q", key, liveLock.TaskID, replayLock.TaskID)
		}
		if !liveLock.ClaimedAt.Equal(replayLock.ClaimedAt) {
			t.Fatalf("lock for %q claimed_at mismatch: live=%v, replay=%v", key, liveLock.ClaimedAt, replayLock.ClaimedAt)
		}
	}

	// E. Check Territory History (must explicitly contain ReasonLeaseExpired)
	if len(liveState.TerritoryHistory) != len(replayState.TerritoryHistory) {
		t.Fatalf("territory history count mismatch: live=%d, replay=%d", len(liveState.TerritoryHistory), len(replayState.TerritoryHistory))
	}
	foundLeaseExpired := false
	for i, liveHist := range liveState.TerritoryHistory {
		replayHist := replayState.TerritoryHistory[i]
		if liveHist.TaskID != replayHist.TaskID {
			t.Fatalf("history[%d] task_id mismatch: live=%q, replay=%q", i, liveHist.TaskID, replayHist.TaskID)
		}
		if liveHist.TerritoryKey != replayHist.TerritoryKey {
			t.Fatalf("history[%d] territory_key mismatch: live=%q, replay=%q", i, liveHist.TerritoryKey, replayHist.TerritoryKey)
		}
		if liveHist.Reason != replayHist.Reason {
			t.Fatalf("history[%d] reason mismatch: live=%q, replay=%q", i, liveHist.Reason, replayHist.Reason)
		}
		if !liveHist.EventTime.Equal(replayHist.EventTime) {
			t.Fatalf("history[%d] event_time mismatch: live=%v, replay=%v", i, liveHist.EventTime, replayHist.EventTime)
		}
		if liveHist.Reason == joinlog.ReasonLeaseExpired {
			foundLeaseExpired = true
		}
	}

	if !foundLeaseExpired {
		t.Fatalf("expected to find ReasonLeaseExpired in territory history, but it was absent")
	}

	// F. Overall deep equality assertion
	if !reflect.DeepEqual(liveState, replayState) {
		t.Fatalf("state mismatch between live execution and full replay!\nLive:   %+v\nReplay: %+v", liveState, replayState)
	}
}
