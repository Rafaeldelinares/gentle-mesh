package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
)

// Two independent Shell instances (separate *sql.DB handles over the same
// SQLite file, WAL, one pooled connection each — same topology as production)
// appending concurrently to the same (emitter, executor) pair.
//
// Today a contended append escapes as ErrInvalidPreviousHash/ErrSequenceConflict
// because the last receipt is read, signed and saved without any retry inside
// Shell.Execute.
func TestShell_Execute_TwoInstances_Contention(t *testing.T) {
	chainDB := filepath.Join(t.TempDir(), "chain.db")
	workspace := t.TempDir()

	newShell := func() *Shell {
		s, err := New(Config{
			MeshID:       "mesh-contention",
			AgentID:      "shared-agent",
			WorkspaceDir: workspace,
			ChainDBPath:  chainDB,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	}

	s1 := newShell()
	defer s1.Close()
	s2 := newShell()
	defer s2.Close()

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

	for _, s := range []*Shell{s1, s2} {
		for i := 0; i < perInstance; i++ {
			wg.Add(1)
			go func(s *Shell) {
				defer wg.Done()
				<-start
				if _, err := s.Execute(context.Background(), assertions); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}(s)
		}
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("Shell.Execute lost %d/%d concurrent appends to chain contention; first error: %v",
			len(errs), total, errs[0])
	}

	chain, err := s1.GetChain("shared-agent", "shared-agent")
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
