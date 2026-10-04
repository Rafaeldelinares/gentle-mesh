package receipt

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

const (
	// DefaultAppendMaxAttempts bounds how many times SignAndSaveReceipt retries a
	// transient chain-contention failure before returning an explicit error.
	DefaultAppendMaxAttempts = 10

	// DefaultAppendBackoff is the base retry delay. Attempt N waits
	// N*DefaultAppendBackoff plus a random jitter of up to DefaultAppendBackoff.
	DefaultAppendBackoff = 10 * time.Millisecond
)

// AppendConfig configures SignAndSaveReceipt.
type AppendConfig struct {
	// EmitterAgentID and ExecutorAgentID identify the chain this receipt belongs to.
	EmitterAgentID  string
	ExecutorAgentID string

	// Signer signs every attempt (Ed25519).
	Signer signing.Signer

	// Lock, when non-nil, is held for exactly one append attempt (reload the head,
	// sign, save) and released during backoff. It bounds in-process contention.
	Lock sync.Locker

	// MaxAttempts bounds the attempts; <= 0 uses DefaultAppendMaxAttempts.
	MaxAttempts int

	// BaseBackoff is the base retry delay; <= 0 uses DefaultAppendBackoff.
	BaseBackoff time.Duration
}

// SignAndSaveReceipt appends one receipt to the (emitter, executor) chain with
// optimistic retry against concurrent writers.
//
// The builder returns a fresh receipt carrying only the caller-specific fields
// (mesh, contract, envelope hash, verdict, assertions, territory).
// SignAndSaveReceipt owns the chain-dependent fields, recomputed on every attempt:
//   - previous_receipt_hash, derived from the current chain head;
//   - receipt_id, a fresh crypto/rand identifier (never a timestamp, so two
//     concurrent writers cannot collide on the same id);
//   - sequence_number, assigned by SaveReceipt;
//   - executor_signature, recomputed after the fields above are set.
//
// Retry policy: ONLY ErrSequenceConflict and ErrInvalidPreviousHash are retried —
// they mean another writer advanced the chain between the read and the write.
// Every other error (generic ErrChainBroken, signing failures, disk or SQLite
// errors, an exhausted SQLITE_BUSY) is permanent and returned immediately.
//
// Locking hierarchy, outermost to innermost:
//  1. AppendConfig.Lock (caller-owned, e.g. Engine.chainMu): serializes append
//     attempts inside one process. It IS held while the receipt is signed, which
//     bounds intra-process retry storms at the cost of serializing the (fast)
//     Ed25519 signature; empirically this costs little and removing it only moves
//     the same contention into wasted attempts. It is NEVER held during backoff.
//  2. ChainStore.mu: serializes SaveReceipt inside one ChainStore instance.
//  3. The SQLite write transaction (BEGIN IMMEDIATE on a dedicated connection):
//     serializes writers ACROSS processes and is the layer that actually rejects a
//     stale previous_receipt_hash. Correctness does not depend on layers 1 and 2.
func SignAndSaveReceipt(
	ctx context.Context,
	cs *ChainStore,
	cfg AppendConfig,
	build func() (*SettlementReceipt, error),
) (*SettlementReceipt, error) {
	if cs == nil {
		return nil, errors.New("chain store is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("signer is required")
	}
	if build == nil {
		return nil, errors.New("receipt builder is required")
	}

	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultAppendMaxAttempts
	}
	base := cfg.BaseBackoff
	if base <= 0 {
		base = DefaultAppendBackoff
	}

	var (
		saved   *SettlementReceipt
		lastErr error
	)

	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		saved, lastErr = nil, nil

		// One attempt: the caller lock is held only here, never during backoff.
		func() {
			if cfg.Lock != nil {
				cfg.Lock.Lock()
				defer cfg.Lock.Unlock()
			}

			rec, err := build()
			if err != nil {
				lastErr = fmt.Errorf("build receipt: %w", err)
				return
			}

			last, err := cs.GetLastReceipt(ctx, cfg.EmitterAgentID, cfg.ExecutorAgentID)
			switch {
			case err == nil:
				h := sha256.Sum256([]byte(last.ExecutorSignature))
				rec.PreviousReceiptHash = hex.EncodeToString(h[:])
			case errors.Is(err, ErrReceiptNotFound):
				rec.PreviousReceiptHash = ""
			default:
				lastErr = fmt.Errorf("get last receipt: %w", err)
				return
			}

			rec.ReceiptID = newReceiptID()
			rec.EmitterAgentID = cfg.EmitterAgentID
			rec.ExecutorAgentID = cfg.ExecutorAgentID
			rec.SequenceNumber = 0
			rec.ExecutorSignature = ""

			if err := SignReceipt(rec, cfg.Signer); err != nil {
				lastErr = fmt.Errorf("sign receipt: %w", err)
				return
			}

			lastErr = cs.SaveReceipt(ctx, rec)
			if lastErr == nil {
				saved = rec
			}
		}()

		if lastErr == nil {
			return saved, nil
		}
		if !isTransientChainContention(lastErr) {
			return nil, lastErr
		}
		if attempt == attempts-1 {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(jitteredBackoff(base, attempt)):
		}
	}

	return nil, fmt.Errorf("append receipt for pair (%s, %s): exceeded max retry attempts due to concurrent chain modifications (%d attempts): %w",
		cfg.EmitterAgentID, cfg.ExecutorAgentID, attempts, lastErr)
}

// isTransientChainContention reports whether err means "another writer advanced
// the chain" and is therefore worth retrying.
//
// ErrInvalidPreviousHash wraps ErrChainBroken, so it must be checked first and
// explicitly: a generic ErrChainBroken (a genuinely broken/ambiguous chain) is
// NOT retried.
func isTransientChainContention(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSequenceConflict) {
		return true
	}
	if errors.Is(err, ErrInvalidPreviousHash) {
		return true
	}
	return false
}

// jitteredBackoff returns the delay before the next attempt: a linearly growing
// base plus random jitter of up to one base interval, so independent writers do
// not retry in lockstep.
//
// The jitter only needs to decorrelate timing, not to be unpredictable, so it
// uses math/rand instead of crypto/rand (gosec G404 is annotated with that reason).
func jitteredBackoff(base time.Duration, attempt int) time.Duration {
	delay := base * time.Duration(attempt+1)
	jitter := time.Duration(rand.Int63n(int64(base) + 1)) // #nosec G404 -- retry-timing jitter only; unpredictability is not a security property here.
	return delay + jitter
}

// newReceiptID returns a fresh 128-bit receipt identifier from crypto/rand.
func newReceiptID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		// crypto/rand.Read never fails in practice; fall back to a
		// nanosecond-derived value so an append is never blocked.
		ts := time.Now().UTC().UnixNano()
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", ts)))
		return hex.EncodeToString(h[:16])
	}
	return hex.EncodeToString(b)
}
