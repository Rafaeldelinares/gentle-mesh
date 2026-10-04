package receipt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// flakySigner delegates to a real signer and fails the first failFirst calls
// with failWith, then succeeds. It counts calls so tests can assert the retry
// policy.
type flakySigner struct {
	inner     *signing.BasicSigner
	failFirst int
	failWith  error

	mu    sync.Mutex
	calls int
}

func (f *flakySigner) Sign(data []byte) (string, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()

	if f.failWith != nil && call <= f.failFirst {
		return "", f.failWith
	}
	return f.inner.Sign(data)
}

func (f *flakySigner) PublicKey() []byte { return f.inner.PublicKey() }
func (f *flakySigner) AgentID() string   { return f.inner.AgentID() }

func (f *flakySigner) callsMade() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func appendTestBuilder() func() (*SettlementReceipt, error) {
	return func() (*SettlementReceipt, error) {
		return &SettlementReceipt{
			ProtocolVersion: CurrentProtocolVersion,
			MeshID:          "test-mesh",
			ContractID:      "contract-append-test",
			EnvelopeHash:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Verdict:         VerdictSettledClean,
		}, nil
	}
}

func appendTestConfig(sg signing.Signer) AppendConfig {
	return AppendConfig{
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Signer:          sg,
		BaseBackoff:     time.Millisecond,
	}
}

// A permanent signing failure must not be retried.
func TestSignAndSaveReceipt_PermanentSigningError_NotRetried(t *testing.T) {
	cs, _ := setupChain(t)
	_, base := makeTestSigners(t)
	sg := &flakySigner{inner: base, failFirst: 1, failWith: errors.New("signer unavailable")}

	_, err := SignAndSaveReceipt(context.Background(), cs, appendTestConfig(sg), appendTestBuilder())
	if err == nil {
		t.Fatal("expected error on permanent signing failure")
	}
	if sg.callsMade() != 1 {
		t.Fatalf("signer called %d times, want 1: a permanent error must not be retried", sg.callsMade())
	}
}

// A generic ErrChainBroken (not ErrInvalidPreviousHash) is permanent and must not
// be retried: only stale-prev-hash/sequence conflicts are transient.
func TestSignAndSaveReceipt_GenericChainBroken_NotRetried(t *testing.T) {
	cs, _ := setupChain(t)
	_, base := makeTestSigners(t)
	sg := &flakySigner{
		inner:     base,
		failFirst: 1000,
		failWith:  fmt.Errorf("%w: genuinely ambiguous chain, not contention", ErrChainBroken),
	}

	_, err := SignAndSaveReceipt(context.Background(), cs, appendTestConfig(sg), appendTestBuilder())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrInvalidPreviousHash) {
		t.Fatalf("plain ErrChainBroken must not be treated as ErrInvalidPreviousHash: %v", err)
	}
	if sg.callsMade() != 1 {
		t.Fatalf("signer called %d times, want 1: generic ErrChainBroken must not be retried", sg.callsMade())
	}
}

// Transient contention exhausts the attempt budget and returns an explicit error.
func TestSignAndSaveReceipt_ExhaustsAttempts_ExplicitError(t *testing.T) {
	cs, _ := setupChain(t)
	_, base := makeTestSigners(t)
	sg := &flakySigner{
		inner:     base,
		failFirst: 1000,
		failWith:  fmt.Errorf("%w: stale head", ErrInvalidPreviousHash),
	}

	cfg := appendTestConfig(sg)
	cfg.MaxAttempts = 3

	_, err := SignAndSaveReceipt(context.Background(), cs, cfg, appendTestBuilder())
	if err == nil {
		t.Fatal("expected an explicit exhaustion error")
	}
	if !errors.Is(err, ErrInvalidPreviousHash) {
		t.Fatalf("exhaustion error should wrap the last transient cause, got: %v", err)
	}
	if !strings.Contains(err.Error(), "exceeded max retry attempts due to concurrent chain modifications") {
		t.Fatalf("exhaustion error is not explicit: %v", err)
	}
	if sg.callsMade() != 3 {
		t.Fatalf("signer called %d times, want 3 (MaxAttempts)", sg.callsMade())
	}
	// Nothing must have been persisted after exhausting attempts.
	if n, err := cs.Count(context.Background(), "agent-a", "agent-b"); err != nil || n != 0 {
		t.Fatalf("chain mutated by a failed append: count=%d err=%v", n, err)
	}
}

// Transient contention is retried and eventually succeeds; the persisted receipt
// verifies against the signer's public key.
func TestSignAndSaveReceipt_RetriesTransient_ThenSucceeds(t *testing.T) {
	cs, _ := setupChain(t)
	_, base := makeTestSigners(t)
	sg := &flakySigner{
		inner:     base,
		failFirst: 2,
		failWith:  fmt.Errorf("%w: stale head", ErrSequenceConflict),
	}

	rec, err := SignAndSaveReceipt(context.Background(), cs, appendTestConfig(sg), appendTestBuilder())
	if err != nil {
		t.Fatalf("expected eventual success, got: %v", err)
	}
	if sg.callsMade() != 3 {
		t.Fatalf("signer called %d times, want 3 (2 transient failures + 1 success)", sg.callsMade())
	}
	if rec.ReceiptID == "" {
		t.Fatal("saved receipt has an empty receipt_id")
	}
	if err := VerifyExecutorSignature(rec, base.PublicKey()); err != nil {
		t.Fatalf("saved receipt signature does not verify: %v", err)
	}
	if n, err := cs.Count(context.Background(), "agent-a", "agent-b"); err != nil || n != 1 {
		t.Fatalf("expected exactly one persisted receipt, got count=%d err=%v", n, err)
	}
}

// A cancelled context aborts the append instead of burning every attempt.
func TestSignAndSaveReceipt_CancelledContext_Aborts(t *testing.T) {
	cs, _ := setupChain(t)
	_, base := makeTestSigners(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := SignAndSaveReceipt(ctx, cs, appendTestConfig(base), appendTestBuilder())
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// Receipt IDs are 128-bit crypto/rand values, not clock-derived: 1000 calls must
// be unique and 32 hex characters. This is what removes the timestamp-collision
// class (R4-ReceiptIDCollisionUnderMultiWriter) from the shell callers.
func TestNewReceiptID_UniqueAndRandom(t *testing.T) {
	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := newReceiptID()
		if len(id) != 32 {
			t.Fatalf("receipt id %q has length %d, want 32 hex chars (128 bits)", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate receipt id generated: %s", id)
		}
		seen[id] = true
	}
}
