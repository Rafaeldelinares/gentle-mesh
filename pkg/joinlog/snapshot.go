package joinlog

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/jcs"
)

// Snapshot is the verifiable state snapshot at an exact point in the join log (RFC-003 J3).
// It combines both log position (for stream resume) and semantic projection integrity.
type Snapshot struct {
	// HeadEventSeq is the sequence number of the last event applied to this snapshot.
	HeadEventSeq int64 `json:"head_event_seq"`
	// HeadEventHash is the hash of the last event applied to this snapshot.
	HeadEventHash string `json:"head_event_hash"`
	// State is the projected mesh state at HeadEventSeq.
	State MeshState `json:"state"`
	// StateDigest is the SHA-256 of the JCS canonical form (RFC 8785) of State.
	StateDigest string `json:"state_digest"`
}

func normalizeState(s *MeshState) MeshState {
	if s == nil {
		s = &MeshState{}
	}
	norm := *s
	if norm.Nodes == nil {
		norm.Nodes = make(map[string]NodeJoinedPayload)
	}
	if norm.Tasks == nil {
		norm.Tasks = make(map[string]TaskCreatedPayload)
	}
	if norm.ActiveLocks == nil {
		norm.ActiveLocks = make(map[string]ActiveTerritoryLock)
	}
	if norm.SettledTasks == nil {
		norm.SettledTasks = make(map[string]TaskSettledPayload)
	}
	if norm.TerritoryHistory == nil {
		norm.TerritoryHistory = make([]TerritoryHistoryEntry, 0)
	}
	if norm.TerritoryVerifications == nil {
		norm.TerritoryVerifications = make([]TerritoryVerification, 0)
	}
	return norm
}

// ComputeStateDigest returns the hex-encoded SHA-256 digest of the RFC 8785 (JCS)
// canonical representation of state.
func ComputeStateDigest(state *MeshState) (string, error) {
	norm := normalizeState(state)
	return jcs.HashHexString(norm)
}

// CreateSnapshot constructs a verifiable Snapshot from the projector's current tables,
// anchored to the head of log l.
func (p *Projector) CreateSnapshot(ctx context.Context, l *Log) (*Snapshot, error) {
	seq, hash, err := l.HeadPosition(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot head position: %w", err)
	}

	state, err := p.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot extract state: %w", err)
	}

	digest, err := ComputeStateDigest(state)
	if err != nil {
		return nil, fmt.Errorf("snapshot compute digest: %w", err)
	}

	return &Snapshot{
		HeadEventSeq:  seq,
		HeadEventHash: hash,
		State:         *state,
		StateDigest:   digest,
	}, nil
}

// VerifySnapshot checks whether snap.StateDigest matches the freshly recomputed
// JCS digest of snap.State using constant-time comparison.
func VerifySnapshot(snap *Snapshot) (bool, error) {
	if snap == nil {
		return false, fmt.Errorf("snapshot is nil")
	}
	if snap.StateDigest == "" {
		return false, fmt.Errorf("snapshot state digest is empty")
	}
	computed, err := ComputeStateDigest(&snap.State)
	if err != nil {
		return false, fmt.Errorf("compute state digest: %w", err)
	}
	return subtle.ConstantTimeCompare([]byte(computed), []byte(snap.StateDigest)) == 1, nil
}
