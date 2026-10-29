package joinlog_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
)

// RFC-003 J3 (Snapshot verificable) requires that:
// 1. The snapshot contains the log anchor: HeadEventSeq and HeadEventHash (for stream alignment).
// 2. The snapshot contains the projected State (MeshState).
// 3. The snapshot contains StateDigest = SHA256(JCS(State)) (for semantic projection integrity).
// 4. Any tampering with State produces a digest mismatch.
func TestSnapshot_VerifiableDigest(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	logDBPath := filepath.Join(tempDir, "test_log.db")
	projDBPath := filepath.Join(tempDir, "test_proj.db")

	log, err := joinlog.New(logDBPath)
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	projector, err := joinlog.NewProjector(projDBPath)
	if err != nil {
		t.Fatalf("open projector: %v", err)
	}
	defer projector.Close()

	// Append and project a few events
	events := []struct {
		eventType string
		actor     string
		payload   any
	}{
		{
			eventType: joinlog.EventTypeNodeJoined,
			actor:     "node-coord",
			payload: joinlog.NodeJoinedPayload{
				NodeID:   "node-coord",
				Role:     "coordinator",
				Endpoint: "https://192.168.1.26:8630",
				Tags:     []string{"core"},
			},
		},
		{
			eventType: joinlog.EventTypeTaskCreated,
			actor:     "node-coord",
			payload: joinlog.TaskCreatedPayload{
				TaskID:    "task-99",
				GitRepo:   "https://github.com/org/repo-snap",
				GitBranch: "main",
				Prompt:    "Snapshot test task",
			},
		},
		{
			eventType: joinlog.EventTypeTerritoryClaimed,
			actor:     "task-99",
			payload: joinlog.TerritoryClaimedPayload{
				TaskID:    "task-99",
				GitRepo:   "https://github.com/org/repo-snap",
				GitBranch: "main",
			},
		},
	}

	for _, tc := range events {
		data, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		evt := joinlog.Event{
			Type:      tc.eventType,
			Actor:     tc.actor,
			Payload:   data,
			CreatedAt: time.Now().UTC(),
		}
		if err := log.Append(ctx, evt); err != nil {
			t.Fatalf("append event: %v", err)
		}
		if err := projector.ProjectEvent(ctx, evt); err != nil {
			t.Fatalf("project event: %v", err)
		}
	}

	headHash, err := log.Head(ctx)
	if err != nil {
		t.Fatalf("get head: %v", err)
	}

	snap, err := projector.CreateSnapshot(ctx, log)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	// 1. Verify log position anchor
	if snap.HeadEventSeq != int64(len(events)) {
		t.Fatalf("expected HeadEventSeq=%d, got %d", len(events), snap.HeadEventSeq)
	}
	if snap.HeadEventHash != headHash {
		t.Fatalf("expected HeadEventHash=%q, got %q", headHash, snap.HeadEventHash)
	}

	// 2. Verify StateDigest calculation and verification
	if snap.StateDigest == "" {
		t.Fatalf("expected non-empty StateDigest")
	}

	valid, err := joinlog.VerifySnapshot(snap)
	if err != nil {
		t.Fatalf("VerifySnapshot failed: %v", err)
	}
	if !valid {
		t.Fatalf("expected snapshot to be valid, but VerifySnapshot returned false")
	}

	recomputedDigest, err := joinlog.ComputeStateDigest(&snap.State)
	if err != nil {
		t.Fatalf("ComputeStateDigest failed: %v", err)
	}
	if recomputedDigest != snap.StateDigest {
		t.Fatalf("digest mismatch: computed %q, snapshot has %q", recomputedDigest, snap.StateDigest)
	}
}

func TestSnapshot_TamperedStateFailsVerification(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	log, err := joinlog.New(filepath.Join(tempDir, "tamper_log.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer log.Close()

	projector, err := joinlog.NewProjector(filepath.Join(tempDir, "tamper_proj.db"))
	if err != nil {
		t.Fatalf("open projector: %v", err)
	}
	defer projector.Close()

	nodeData, _ := json.Marshal(joinlog.NodeJoinedPayload{
		NodeID:   "w-1",
		Role:     "worker",
		Endpoint: "https://10.0.0.1:8631",
	})
	evt := joinlog.Event{
		Type:      joinlog.EventTypeNodeJoined,
		Actor:     "w-1",
		Payload:   nodeData,
		CreatedAt: time.Now().UTC(),
	}
	_ = log.Append(ctx, evt)
	_ = projector.ProjectEvent(ctx, evt)

	snap, err := projector.CreateSnapshot(ctx, log)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	// Tamper 1: change a node's endpoint in the state
	tamperedNode := snap.State.Nodes["w-1"]
	tamperedNode.Endpoint = "https://malicious-host.example:9999"
	snap.State.Nodes["w-1"] = tamperedNode

	valid, err := joinlog.VerifySnapshot(snap)
	if err == nil && valid {
		t.Fatalf("expected VerifySnapshot to reject tampered node endpoint, but it passed")
	}

	// Tamper 2: inject a fake active lock
	delete(snap.State.Nodes, "w-1")
	snap.State.ActiveLocks["stolen:master"] = joinlog.ActiveTerritoryLock{
		TerritoryKey: "stolen:master",
		TaskID:       "evil-task",
		ClaimedAt:    time.Now().UTC(),
	}

	valid, err = joinlog.VerifySnapshot(snap)
	if err == nil && valid {
		t.Fatalf("expected VerifySnapshot to reject injected territory lock, but it passed")
	}
}

func TestSnapshot_EmptyLogGenesisState(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	log, err := joinlog.New(filepath.Join(tempDir, "empty_log.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer log.Close()

	projector, err := joinlog.NewProjector(filepath.Join(tempDir, "empty_proj.db"))
	if err != nil {
		t.Fatalf("open projector: %v", err)
	}
	defer projector.Close()

	snap, err := projector.CreateSnapshot(ctx, log)
	if err != nil {
		t.Fatalf("create snapshot on empty log: %v", err)
	}

	if snap.HeadEventSeq != 0 {
		t.Fatalf("expected HeadEventSeq=0 for empty log, got %d", snap.HeadEventSeq)
	}
	if snap.HeadEventHash != "" {
		t.Fatalf("expected HeadEventHash='' for empty log, got %q", snap.HeadEventHash)
	}

	valid, err := joinlog.VerifySnapshot(snap)
	if err != nil || !valid {
		t.Fatalf("expected empty snapshot to verify cleanly, got valid=%v, err=%v", valid, err)
	}
}
