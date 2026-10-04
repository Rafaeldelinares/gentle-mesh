package shell

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
)

// Shell receipts must be signed with the receipt signing scheme (SignReceipt over
// ComputeReceiptHash) so VerifyChainIntegrity accepts them. Before the unified
// append helper, Shell.Execute signed the raw 32-byte hash of a different
// canonical JSON (one that still contained previous_receipt_hash), so every shell
// receipt failed chain verification — including the first one.
func TestShell_ReceiptsVerifyWithChainIntegrity(t *testing.T) {
	chainDB := filepath.Join(t.TempDir(), "chain.db")

	s, err := New(Config{
		MeshID:       "mesh-sign",
		AgentID:      "agent-sign",
		WorkspaceDir: t.TempDir(),
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	assertions := []envelope.Assertion{{
		ID:     "ok",
		Type:   envelope.AssertionCommandExitCode,
		Params: envelope.AssertionParams{Command: "true", ExpectedExitCode: 0},
	}}

	for i := 0; i < 3; i++ {
		if _, err := s.Execute(context.Background(), assertions); err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
	}

	chain, err := s.GetChain("agent-sign", "agent-sign")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(chain))
	}

	results, err := receipt.VerifyChainIntegrity(chain, s.Signer().PublicKey(), nil)
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
