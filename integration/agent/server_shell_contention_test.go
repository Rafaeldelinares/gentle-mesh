package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
)

// Two independent ShellServer instances (separate *sql.DB handles over the same
// SQLite file, WAL, one pooled connection each) appending concurrently to the
// same (emitter, executor) pair via executeLocal.
//
// Today a contended append escapes as ErrInvalidPreviousHash/ErrSequenceConflict
// because the last receipt is read, signed and saved without any retry inside
// ShellServer.executeLocal.
func TestShellServer_ExecuteLocal_TwoInstances_Contention(t *testing.T) {
	dir := t.TempDir()
	chainDB := filepath.Join(dir, "chain.db")

	newServer := func() *ShellServer {
		srv, err := NewShellServer("shared-agent", "mesh-contention", dir, chainDB, 5*time.Second)
		if err != nil {
			t.Fatalf("NewShellServer: %v", err)
		}
		return srv
	}

	srv1 := newServer()
	defer srv1.Close()
	srv2 := newServer()
	defer srv2.Close()

	assertions := []envelope.Assertion{{
		ID:     "ok",
		Type:   envelope.AssertionCommandExitCode,
		Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0},
	}}

	const perInstance = 8
	const total = perInstance * 2

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, srv := range []*ShellServer{srv1, srv2} {
		for i := 0; i < perInstance; i++ {
			wg.Add(1)
			go func(srv *ShellServer) {
				defer wg.Done()
				<-start
				if _, err := srv.executeLocal(context.Background(), assertions); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}(srv)
		}
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("executeLocal lost %d/%d concurrent appends to chain contention; first error: %v",
			len(errs), total, errs[0])
	}

	chain, err := srv1.chainStore.GetChain(context.Background(), "shared-agent", "shared-agent")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != total {
		t.Fatalf("chain length = %d, want %d", len(chain), total)
	}

	seen := make(map[string]bool, len(chain))
	for i, r := range chain {
		if r.SequenceNumber != int64(i+1) {
			t.Errorf("chain[%d] seq = %d, want %d", i, r.SequenceNumber, i+1)
		}
		if seen[r.ReceiptID] {
			t.Errorf("duplicate receipt_id persisted: %s", r.ReceiptID)
		}
		seen[r.ReceiptID] = true
		if i > 0 {
			h := sha256.Sum256([]byte(chain[i-1].ExecutorSignature))
			if want := hex.EncodeToString(h[:]); r.PreviousReceiptHash != want {
				t.Errorf("chain[%d] broken link: got %s want %s", i, r.PreviousReceiptHash, want)
			}
		}
	}
}

// ShellServer receipts must be signed with the receipt signing scheme so
// VerifyChainIntegrity accepts them (same fix as pkg/shell).
func TestShellServer_ReceiptsVerifyWithChainIntegrity(t *testing.T) {
	dir := t.TempDir()
	chainDB := filepath.Join(dir, "chain.db")

	srv, err := NewShellServer("agent-sign", "mesh-sign", dir, chainDB, 5*time.Second)
	if err != nil {
		t.Fatalf("NewShellServer: %v", err)
	}
	defer srv.Close()

	assertions := []envelope.Assertion{{
		ID:     "ok",
		Type:   envelope.AssertionCommandExitCode,
		Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0},
	}}
	for i := 0; i < 3; i++ {
		if _, err := srv.executeLocal(context.Background(), assertions); err != nil {
			t.Fatalf("executeLocal %d: %v", i, err)
		}
	}

	chain, err := srv.chainStore.GetChain(context.Background(), "agent-sign", "agent-sign")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(chain))
	}

	results, err := receipt.VerifyChainIntegrity(chain, srv.signer.PublicKey(), nil)
	if err != nil {
		t.Fatalf("VerifyChainIntegrity: %v", err)
	}
	for i, r := range results {
		if !r.ExecutorSignatureValid {
			t.Errorf("chain[%d] executor signature invalid: %s", i, r.Error)
		}
		if !r.Valid {
			t.Errorf("chain[%d] not valid: %s", i, r.Error)
		}
	}
}
