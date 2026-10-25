package agent

// RFC-002 R2 (Issue #69): lease expiration.
//
// Criterio (issue #69, verbatim): "Todo lease tiene expiración; al vencer, el
// ejecutor aborta y emite un recibo SETTLEMENT_TIMEOUT."
//
// The guarantee is AUTONOMOUS: the executor's reaper emits the receipt when a
// lease expires, without any incoming request (a crashed or hung agent never
// calls /settle again). handleSettle keeps a reactive check for callers that
// arrive right at the boundary.
//
// Locking contract under test: leasesMu is NEVER held during receipt I/O.
// The reaper snapshots expired (lease, envelope) pairs under leasesMu, drops
// the lock, then persists receipts. No path holds leasesMu and chainMu
// together, so lock-order deadlock is impossible by construction.
//
// Race arbitration: a legitimate engine.Settle finishing at the same instant
// the reaper expires the same contract writes a receipt for the same
// contract_id; the R1 mechanics (UNIQUE index + ErrDuplicateContract
// recovery) must yield exactly ONE receipt, with no duplicate execution.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

func setupR2Server(t *testing.T, reaperInterval time.Duration) (*Server, *httptest.Server, signing.Signer) {
	t.Helper()
	tmpDir := t.TempDir()
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	emitterSigner, err := signing.GenerateSigner("agent-emitter")
	if err != nil {
		t.Fatalf("GenerateSigner emitter: %v", err)
	}
	if _, err := signing.GenerateSigner("test-executor"); err != nil {
		t.Fatalf("GenerateSigner executor: %v", err)
	}

	cfg := Config{
		AgentID:             "test-executor",
		MeshID:              "mesh-r2-test",
		Role:                RoleExecutor,
		ChainDBPath:         filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir:        workspaceDir,
		EvalTimeout:         5 * time.Second,
		MaxRemed:            1,
		LeaseReaperInterval: reaperInterval,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_ = srv.RegisterAgentKey("agent-emitter", emitterSigner.PublicKey())

	mux := http.NewServeMux()
	srv.registerHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return srv, ts, emitterSigner
}

// r2SignedEnvelope builds and signs an emitter envelope with one assertion.
func r2SignedEnvelope(t *testing.T, signer signing.Signer, envelopeID string, assertions []envelope.Assertion, timeoutSeconds int) []byte {
	t.Helper()
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-r2-test",
		EnvelopeID:      envelopeID,
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "test-executor",
		TimeoutSeconds:  timeoutSeconds,
		MaxRemediations: 0,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: assertions,
		CreatedAt:  time.Now().UTC(),
	}
	if err := envelope.SignEnvelope(env, signer); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return envJSON
}

// r2Submit posts the envelope and returns the lease ID.
func r2Submit(t *testing.T, ts *httptest.Server, envJSON []byte) (leaseID string, accepted bool) {
	t.Helper()
	reqBytes, _ := json.Marshal(&EnvelopeRequest{EnvelopeJSON: envJSON})
	resp, err := ts.Client().Post(ts.URL+"/envelopes", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("submit envelope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit envelope status = %d, want 200", resp.StatusCode)
	}
	var er EnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatalf("decode envelope response: %v", err)
	}
	return er.LeaseID, er.Accepted
}

// r2Settle posts /settle and returns status and receipt ID.
func r2Settle(t *testing.T, ts *httptest.Server, envJSON []byte, leaseID string) (int, string) {
	t.Helper()
	reqBytes, _ := json.Marshal(&SettleRequest{EnvelopeJSON: envJSON, LeaseID: leaseID})
	resp, err := ts.Client().Post(ts.URL+"/settle", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	var sr SettleResponse
	_ = json.Unmarshal(buf.Bytes(), &sr)
	return resp.StatusCode, sr.ReceiptID
}

// r2WaitForVerdict polls the chain until a receipt with the given verdict
// appears for the pair, or fails after the deadline.
func r2WaitForVerdict(t *testing.T, srv *Server, verdict receipt.Verdict, deadline time.Duration) *receipt.SettlementReceipt {
	t.Helper()
	timer := time.After(deadline)
	for {
		chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
		if err != nil {
			t.Fatalf("GetChain: %v", err)
		}
		for _, r := range chain {
			if r.Verdict == verdict {
				return r
			}
		}
		select {
		case <-timer:
			t.Fatalf("no receipt with verdict %s within %v; chain has %d receipts", verdict, deadline, len(chain))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// r2HookSigner parks the first Sign call until Release is called: the parked
// caller has already passed every pre-check and is right before its INSERT.
// Release is idempotent — the test defers it so the parked HTTP request
// always completes, even when the test fails (httptest.Server.Close blocks
// on outstanding requests).
type r2HookSigner struct {
	signing.Signer
	parked      chan struct{}
	release     chan struct{}
	parkedOnce  sync.Once
	releaseOnce sync.Once
}

func newR2HookSigner(inner signing.Signer) *r2HookSigner {
	return &r2HookSigner{
		Signer:  inner,
		parked:  make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (h *r2HookSigner) Sign(data []byte) (string, error) {
	h.parkedOnce.Do(func() { close(h.parked) })
	<-h.release
	return h.Signer.Sign(data)
}

// Release unblocks the parked caller (idempotent).
func (h *r2HookSigner) Release() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// ─────────────────────────────────────────────────────────────────
// (a) Reactive path: settle arrives after the lease expired.
// ─────────────────────────────────────────────────────────────────

// TestR2_SettleWithExpiredLease_EmitsTimeoutReceipt: a settle whose lease
// already expired must abort (assertions never run) and emit a
// SETTLEMENT_TIMEOUT receipt persisted in the chain.
func TestR2_SettleWithExpiredLease_EmitsTimeoutReceipt(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 0) // reaper disabled: isolate the reactive path

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2reactive-01",
		[]envelope.Assertion{{ID: "fail-if-run", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "false"}}}, 30)
	leaseID, accepted := r2Submit(t, ts, envJSON)
	if !accepted {
		t.Fatal("lease not accepted")
	}

	// Force expiration behind the server's back.
	srv.leasesMu.Lock()
	if _, ok := srv.leases[leaseID]; !ok {
		srv.leasesMu.Unlock()
		t.Fatalf("lease %s not registered", leaseID)
	}
	srv.leases[leaseID].ExpiresAt = time.Now().Add(-1 * time.Second)
	srv.leasesMu.Unlock()

	status, receiptID := r2Settle(t, ts, envJSON, leaseID)
	if status != http.StatusOK {
		t.Fatalf("settle status = %d, want 200 with SETTLEMENT_TIMEOUT receipt", status)
	}

	r := r2WaitForVerdict(t, srv, receipt.VerdictTimeout, 3*time.Second)
	if receiptID != "" && r.ReceiptID != receiptID {
		t.Fatalf("returned receipt %q != chain receipt %q", receiptID, r.ReceiptID)
	}
	if len(r.Assertions) != 0 {
		t.Fatalf("timeout receipt must carry no executed assertions, got %d", len(r.Assertions))
	}

	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected exactly 1 receipt in chain, got %d", len(chain))
	}
}

// ─────────────────────────────────────────────────────────────────
// (b) Reaper purge: removes only expired leases.
// ─────────────────────────────────────────────────────────────────

func TestR2_PurgeExpiredLeases_RemovesOnlyExpired(t *testing.T) {
	srv, _, _ := setupR2Server(t, 0) // reaper disabled: call the purge directly

	now := time.Now()
	mk := func(id string, expiresAt time.Time) {
		srv.leasesMu.Lock()
		srv.leases[id] = &envelope.Lease{LeaseID: id, EnvelopeID: "env-" + id, ExpiresAt: expiresAt}
		srv.leasesMu.Unlock()
	}
	mk("lease-r2b-expired-1", now.Add(-1*time.Second))
	mk("lease-r2b-expired-2", now.Add(-2*time.Second))
	mk("lease-r2b-alive", now.Add(30*time.Second))

	purged := srv.purgeExpiredLeases(now)
	if purged != 2 {
		t.Fatalf("purged = %d, want 2", purged)
	}
	if _, ok := srv.GetLease("lease-r2b-expired-1"); ok {
		t.Error("expired lease 1 still present after purge")
	}
	if _, ok := srv.GetLease("lease-r2b-expired-2"); ok {
		t.Error("expired lease 2 still present after purge")
	}
	if _, ok := srv.GetLease("lease-r2b-alive"); !ok {
		t.Error("alive lease must survive the purge")
	}
}

// ─────────────────────────────────────────────────────────────────
// (c) Reaper vs concurrent access (race detector).
// ─────────────────────────────────────────────────────────────────

// TestR2_Reaper_ConcurrentExpiryVsAccess: the reaper purges while many
// goroutines insert and read leases. Run under -race. Invariants: every
// unexpired lease stays accessible; every expired lease is eventually gone.
func TestR2_Reaper_ConcurrentExpiryVsAccess(t *testing.T) {
	srv, _, _ := setupR2Server(t, 5*time.Millisecond)

	const workers = 10
	const perWorker = 5
	now := time.Now()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < perWorker; i++ {
				id := "lease-r2c-" + string(rune('a'+g)) + "-" + string(rune('0'+i))
				expired := (g+i)%2 == 0
				var expiresAt time.Time
				if expired {
					expiresAt = now.Add(-1 * time.Second)
				} else {
					expiresAt = now.Add(30 * time.Second)
				}
				srv.leasesMu.Lock()
				srv.leases[id] = &envelope.Lease{LeaseID: id, EnvelopeID: "env-" + id, ExpiresAt: expiresAt}
				srv.leasesMu.Unlock()
				_, _ = srv.GetLease(id)
			}
		}(g)
	}
	close(start)
	wg.Wait()

	// Every expired lease must eventually be purged.
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.leasesMu.RLock()
		leftover := 0
		for id, l := range srv.leases {
			if l.ExpiresAt.Before(now) {
				leftover++
				_ = id
			}
		}
		srv.leasesMu.RUnlock()
		if leftover == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d expired leases survived the reaper", leftover)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Every unexpired lease must still be present.
	srv.leasesMu.RLock()
	alive := 0
	for _, l := range srv.leases {
		if !l.ExpiresAt.Before(now) {
			alive++
		}
	}
	srv.leasesMu.RUnlock()
	if alive != workers*perWorker/2 {
		t.Fatalf("alive leases = %d, want %d: the reaper must only purge expired leases",
			alive, workers*perWorker/2)
	}
}

// ─────────────────────────────────────────────────────────────────
// (d) Clean shutdown: context cancellation, no orphan goroutine.
// ─────────────────────────────────────────────────────────────────

func TestR2_ReaperContextCancel_NoOrphan(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 20*time.Millisecond)

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2shutdown-1",
		[]envelope.Assertion{{ID: "noop", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "true"}}}, 30)
	if _, accepted := r2Submit(t, ts, envJSON); !accepted {
		t.Fatal("lease not accepted")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case <-srv.reaperDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper goroutine did not stop after Shutdown: orphan goroutine")
	}

	// After shutdown the reaper must truly be dead: an expired lease stays.
	srv.leasesMu.Lock()
	srv.leases["lease-r2d-after-shutdown"] = &envelope.Lease{
		LeaseID:    "lease-r2d-after-shutdown",
		EnvelopeID: "env-r2d",
		ExpiresAt:  time.Now().Add(-1 * time.Second),
	}
	srv.leasesMu.Unlock()
	time.Sleep(80 * time.Millisecond) // 4 reaper intervals
	if _, ok := srv.GetLease("lease-r2d-after-shutdown"); !ok {
		t.Error("expired lease was purged after shutdown: reaper still alive")
	}
}

// ─────────────────────────────────────────────────────────────────
// (e) Autonomous reaper: the issue's real acceptance test.
// ─────────────────────────────────────────────────────────────────

// TestR2_ReaperAutonomouslyEmitsTimeoutReceipt: an agent that acquires a
// lease and never calls /settle again (crashed/hung) must still produce a
// SETTLEMENT_TIMEOUT receipt in the chain, emitted by the reaper's own cycle
// with no incoming HTTP request.
func TestR2_ReaperAutonomouslyEmitsTimeoutReceipt(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 50*time.Millisecond)

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2auto-00001",
		[]envelope.Assertion{{ID: "never-run", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "false"}}}, 1) // lease expires in 1s
	if _, accepted := r2Submit(t, ts, envJSON); !accepted {
		t.Fatal("lease not accepted")
	}

	// No settle call. The reaper alone must produce the receipt.
	r := r2WaitForVerdict(t, srv, receipt.VerdictTimeout, 6*time.Second)
	if len(r.Assertions) != 0 {
		t.Fatalf("reaper must not execute assertions, got %d", len(r.Assertions))
	}

	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected exactly 1 receipt (the timeout), got %d", len(chain))
	}
}

// ─────────────────────────────────────────────────────────────────
// (f) Reaper vs legitimate settle: R1 arbitrates, exactly one receipt.
// ─────────────────────────────────────────────────────────────────

// TestR2_ReaperVsLegitimateSettle_ReaperWins: a settle whose lease was still
// valid when it passed the reactive check, parked right before its INSERT,
// while the reaper expires the lease and persists the timeout receipt. When
// released, the settle must lose the UNIQUE race and recover the winner's
// receipt via R1 — one receipt total, no duplicate execution.
func TestR2_ReaperVsLegitimateSettle_ReaperWins(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 10*time.Millisecond)

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2race-a-001",
		[]envelope.Assertion{{ID: "noop", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "true"}}}, 2) // lease expires in 2s
	leaseID, accepted := r2Submit(t, ts, envJSON)
	if !accepted {
		t.Fatal("lease not accepted")
	}

	// Park the settle right before its INSERT.
	hooked := newR2HookSigner(srv.signer)
	defer hooked.Release() // unblock the parked HTTP request even on failure
	reEng, err := settlement.NewEngine(settlement.EngineConfig{
		Evaluator:      srv.evaluator,
		ChainStore:     srv.chainStore,
		ExecutorSigner: hooked,
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	srv.engine = reEng

	settleDone := make(chan struct{})
	var settleStatus int
	go func() {
		defer close(settleDone)
		settleStatus, _ = r2Settle(t, ts, envJSON, leaseID)
	}()

	select {
	case <-hooked.parked:
	case <-time.After(3 * time.Second):
		t.Fatal("settle never reached its insert point")
	}

	// The lease was valid at check time; expire it now, behind the settle's back.
	srv.leasesMu.Lock()
	srv.leases[leaseID].ExpiresAt = time.Now().Add(-1 * time.Second)
	srv.leasesMu.Unlock()

	// The reaper alone must persist the timeout receipt while the settle waits.
	winner := r2WaitForVerdict(t, srv, receipt.VerdictTimeout, 5*time.Second)

	hooked.Release()
	<-settleDone
	if settleStatus != http.StatusOK {
		t.Fatalf("settle status = %d, want 200 (R1 recovery)", settleStatus)
	}

	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected exactly 1 receipt after reaper-vs-settle race, got %d", len(chain))
	}
	if chain[0].ReceiptID != winner.ReceiptID || chain[0].Verdict != receipt.VerdictTimeout {
		t.Fatalf("winner receipt changed: got %s/%s, want %s/%s",
			chain[0].Verdict, chain[0].ReceiptID, receipt.VerdictTimeout, winner.ReceiptID)
	}
}

// TestR2_ReaperVsLegitimateSettle_SettleWins: the settle completes before the
// lease expires. When the reaper later expires the lease it must NOT add a
// timeout receipt for an already-settled contract: its emission loses the
// UNIQUE race, recovers the winner (R1) and discards it. Chain stays at one
// SETTLED_CLEAN receipt.
func TestR2_ReaperVsLegitimateSettle_SettleWins(t *testing.T) {
	srv, ts, emitterSigner := setupR2Server(t, 10*time.Millisecond)

	envJSON := r2SignedEnvelope(t, emitterSigner, "01923456-789a-7bc0-8123-r2race-b-001",
		[]envelope.Assertion{{ID: "noop", Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{Command: "true"}}}, 1) // lease expires in 1s
	leaseID, accepted := r2Submit(t, ts, envJSON)
	if !accepted {
		t.Fatal("lease not accepted")
	}

	status, receiptID := r2Settle(t, ts, envJSON, leaseID)
	if status != http.StatusOK {
		t.Fatalf("settle status = %d, want 200", status)
	}

	// Let the lease expire while the clean receipt already exists.
	time.Sleep(1200 * time.Millisecond)

	// Give the reaper plenty of cycles; it must not add anything.
	time.Sleep(300 * time.Millisecond)

	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected exactly 1 receipt (the clean settle), got %d", len(chain))
	}
	if chain[0].ReceiptID != receiptID {
		t.Fatalf("chain receipt %q != settled receipt %q", chain[0].ReceiptID, receiptID)
	}
	if chain[0].Verdict != receipt.VerdictSettledClean {
		t.Fatalf("chain verdict = %s, want SETTLED_CLEAN (reaper must not overwrite a settled contract)",
			chain[0].Verdict)
	}
}
