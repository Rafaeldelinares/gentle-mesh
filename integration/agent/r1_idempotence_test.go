package agent

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

	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

func setupR1Server(t *testing.T) (*Server, *httptest.Server, signing.Signer, signing.Signer) {
	t.Helper()
	tmpDir := t.TempDir()
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	emitterSigner, err := signing.GenerateSigner("agent-emitter")
	if err != nil {
		t.Fatalf("GenerateSigner emitter: %v", err)
	}
	executorSigner, err := signing.GenerateSigner("test-executor")
	if err != nil {
		t.Fatalf("GenerateSigner executor: %v", err)
	}

	cfg := Config{
		AgentID:      "test-executor",
		MeshID:       "mesh-r1-test",
		Role:         RoleExecutor,
		ChainDBPath:  filepath.Join(tmpDir, "chain.db"),
		WorkspaceDir: workspaceDir,
		EvalTimeout:  5 * time.Second,
		MaxRemed:     1,
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

	return srv, ts, emitterSigner, executorSigner
}

// TestR1_ConcurrentSettlement_ReturnsSameReceipt tests RFC-002 R1:
// Two concurrent goroutines send POST /settle with the exact same envelope_id (contract_id).
// Both must receive 200 OK with the identical receipt, and only 1 receipt must be persisted.
func TestR1_ConcurrentSettlement_ReturnsSameReceipt(t *testing.T) {
	srv, ts, emitterSigner, _ := setupR1Server(t)
	client := ts.Client()

	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-r1-test",
		EnvelopeID:      "01923456-789a-7bc0-8123-idempotent-001",
		EmitterAgentID:  "agent-emitter",
		ExecutorAgentID: "test-executor",
		TimeoutSeconds:  30,
		Territory: envelope.Territory{
			Repository:    "https://github.com/example/repo",
			Branch:        "main",
			WorkspacePath: "/tmp/workspace",
		},
		Assertions: []envelope.Assertion{
			{ID: "check-exit", Type: envelope.AssertionCommandExitCode, Params: envelope.AssertionParams{Command: "sleep 0.1"}},
		},
		CreatedAt: time.Now().UTC(),
	}

	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	settleReq := &SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:      "lease-r1",
	}

	const concurrency = 2
	type result struct {
		statusCode int
		receiptID  string
		body       string
		err        error
	}

	results := make([]result, concurrency)
	var wg sync.WaitGroup
	startGate := make(chan struct{})

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			<-startGate // Synchronize start for maximum contention

			reqBytes, _ := json.Marshal(settleReq)
			resp, err := client.Post(ts.URL+"/settle", "application/json", bytes.NewReader(reqBytes))
			if err != nil {
				results[idx] = result{err: err}
				return
			}
			defer resp.Body.Close()

			var buf bytes.Buffer
			_, _ = buf.ReadFrom(resp.Body)
			res := result{
				statusCode: resp.StatusCode,
				body:       buf.String(),
			}

			if resp.StatusCode == http.StatusOK {
				var sr SettleResponse
				if err := json.Unmarshal(buf.Bytes(), &sr); err == nil {
					res.receiptID = sr.ReceiptID
				}
			}
			results[idx] = res
		}(i)
	}

	// Release both goroutines at the exact same moment
	close(startGate)
	wg.Wait()

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("goroutine %d request error: %v", i, res.err)
		}
		if res.statusCode != http.StatusOK {
			t.Errorf("goroutine %d got status %d, want 200 OK. Body: %s", i, res.statusCode, res.body)
		}
		if res.receiptID == "" {
			t.Errorf("goroutine %d received empty receiptID", i)
		}
	}

	if t.Failed() {
		t.FailNow()
	}

	// Both goroutines must have received the EXACT same receipt ID
	if results[0].receiptID != results[1].receiptID {
		t.Errorf("idempotency violated: goroutine 0 receiptID=%q != goroutine 1 receiptID=%q",
			results[0].receiptID, results[1].receiptID)
	}

	// The 100ms executor window forces both requests through the pre-check
	// before either INSERT commits, so the loser must reach the
	// ErrDuplicateContract recovery branch — not just the easy pre-check path.
	if got := srv.engine.DuplicateRecoveries(); got < 1 {
		t.Errorf("duplicate-contract recovery branch never taken (recoveries=%d): the race window did not open", got)
	}
	t.Logf("duplicate-contract recoveries observed: %d", srv.engine.DuplicateRecoveries())

	// Check chain store: must contain exactly 1 receipt for this agent pair
	chain, err := srv.ChainStore().GetChain(context.Background(), "agent-emitter", "test-executor")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected exactly 1 receipt in chain, got %d", len(chain))
	}
	if chain[0].ReceiptID != results[0].receiptID {
		t.Errorf("chain receipt ID %q != returned receipt ID %q", chain[0].ReceiptID, results[0].receiptID)
	}
}
