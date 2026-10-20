package agent

// (g) Renewal race: a lease renewed between the reaper's snapshot and its
// receipt write must NOT receive a SETTLEMENT_TIMEOUT receipt. The purge is
// conditioned (compare-and-delete): the confirmation phase re-checks that the
// lease still carries the exact ExpiresAt seen in the snapshot; a renewal
// (new future ExpiresAt under the same leaseID) keeps the lease alive and
// suppresses the emission — aborting legitimately extended work is exactly
// the failure mode R2 must not introduce.

import (
	"context"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
)

func TestR2_RenewedLeaseGetsNoTimeoutReceipt(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 0) // reaper disabled: drive phases manually

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2renew-001",
		[]envelope.Assertion{{ID: "noop", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "true"}}}, 30)
	leaseID, accepted := r2Submit(t, ts, envJSON)
	if !accepted {
		t.Fatal("lease not accepted")
	}

	// The lease expires.
	srv.leasesMu.Lock()
	srv.leases[leaseID].ExpiresAt = time.Now().Add(-1 * time.Second)
	srv.leasesMu.Unlock()

	// Reaper phase 1: snapshot sees the expired lease.
	snaps := srv.snapshotExpiredLeases(time.Now())
	if len(snaps) != 1 {
		t.Fatalf("snapshot found %d expired leases, want 1", len(snaps))
	}

	// RACE WINDOW: the owner legitimately renews the lease (new future
	// ExpiresAt under the same leaseID).
	srv.leasesMu.Lock()
	srv.leases[leaseID].ExpiresAt = time.Now().Add(30 * time.Second)
	srv.leasesMu.Unlock()

	// Reaper phase 2: confirmation + emission. The renewed lease must be
	// skipped entirely: no timeout receipt, no purge.
	_, emitted := srv.confirmAndEmit(snaps, time.Now())
	if emitted != 0 {
		t.Fatalf("reaper emitted %d SETTLEMENT_TIMEOUT receipts for a renewed lease, want 0", emitted)
	}

	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("expected empty chain for a renewed lease, got %d receipts", len(chain))
	}
	if _, ok := srv.GetLease(leaseID); !ok {
		t.Fatal("renewed lease was purged: the reaper aborted legitimately extended work")
	}
}
