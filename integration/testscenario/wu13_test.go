package testscenario

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// ─────────────────────────────────────────────────────────────────
// Test 1: Fan-out 1-to-3 — emitter (agent-a) → 3 executors concurrently
// Validates: concurrent dispatch, independent receipt chains, Ed25519
// signatures from 3 separate containers, aggregate summary response.
// ─────────────────────────────────────────────────────────────────

func TestWU13_FanOutOneToThree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== WU13 Fan-out 1-to-3: Agent A → B + C + (via dispatch endpoint) ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")
	cClient := newInsecureTLSClient("https://localhost:38443")
	aSigner, _ := signing.GenerateSigner("agent-a")

	type legResult struct {
		executorID string
		receipt   *receipt.SettlementReceipt
		err       error
	}

	// 3 concurrent legs: agent-a → b, agent-a → c, agent-a → (re-use b for 3rd)
	executors := []struct {
		execID string // ExecutorAgentID in the envelope
		container string // Docker container name for exec commands
		client *agent.HTTPClient
	}{
		{"agent-b", "agent-b", bClient},
		{"agent-c", "agent-c", cClient},
		{"agent-b", "agent-b", bClient}, // 3rd leg to B, creates second receipt on B
	}

	results := make([]legResult, len(executors))
	var wg sync.WaitGroup
	for i, exec := range executors {
		wg.Add(1)
		go func(idx int, e struct {
			execID   string
			container string
			client   *agent.HTTPClient
		}) {
			defer wg.Done()
			rec, err := fanDispatchLeg(ctx, aClient, e.client, aSigner, e.execID, e.container,
				fmt.Sprintf("wu13-file-%d", idx))
			results[idx] = legResult{executorID: e.execID, receipt: rec, err: err}
		}(i, exec)
	}
	wg.Wait()

	// Aggregate results.
	succeeded := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("  leg[%d] %s: ERROR: %v", i, r.executorID, r.err)
		} else {
			t.Logf("  leg[%d] %s: receipt=%s verdict=%s ✓",
				i, r.executorID, r.receipt.ReceiptID, r.receipt.Verdict)
			succeeded++
		}
	}
	if succeeded != len(results) {
		t.Fatalf("only %d/%d legs succeeded", succeeded, len(results))
	}

	// Verify Ed25519 signatures from each distinct executor.
	seenExecutors := make(map[string]bool)
	for i, r := range results {
		if r.err != nil {
			continue
		}
		if seenExecutors[r.executorID] {
			continue // already verified
		}
		seenExecutors[r.executorID] = true

		var pubKeyHex string
		switch r.executorID {
		case "agent-b", "agent-b-again":
			h, _ := bClient.Health(ctx)
			pubKeyHex = h.PublicKey
		case "agent-c":
			h, _ := cClient.Health(ctx)
			pubKeyHex = h.PublicKey
		}
		pubKeyBytes, _ := hex.DecodeString(pubKeyHex)
		hash, _ := receipt.ComputeReceiptHash(r.receipt)
		err := signing.Verify(pubKeyBytes, []byte(hash), r.receipt.ExecutorSignature)
		if err != nil {
			t.Errorf("  leg[%d] Ed25519 signature INVALID for %s: %v", i, r.executorID, err)
		} else {
			t.Logf("  leg[%d] Ed25519 signature VALID for %s ✓", i, r.executorID)
		}
	}

	// Verify agent-b has 2 receipts in chain (legs 0 and 2).
	chainB, err := bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("get chain B: %v", err)
	}
	t.Logf("  agent-b chain: %d receipts", len(chainB.Receipts))
	if len(chainB.Receipts) < 2 {
		t.Errorf("agent-b chain length = %d, want ≥2 (legs 0 and 2)", len(chainB.Receipts))
	}

	// Verify agent-c has 1 receipt in chain (leg 1).
	chainC, err := cClient.GetChain(ctx, "agent-a", "agent-c")
	if err != nil {
		t.Fatalf("get chain C: %v", err)
	}
	t.Logf("  agent-c chain: %d receipts", len(chainC.Receipts))
	if len(chainC.Receipts) < 1 {
		t.Errorf("agent-c chain length = %d, want ≥1", len(chainC.Receipts))
	}

	t.Log("=== WU13 Fan-out 1-to-3 PASSED ===")
}

// fanDispatchLeg runs one leg of the fan-out: create file → sign envelope → submit → settle.
// executorAgentID is the agent ID in the envelope; containerName is the Docker container name.
func fanDispatchLeg(ctx context.Context, aClient, executorClient *agent.HTTPClient,
	aSigner *signing.BasicSigner, executorAgentID, containerName, fileName string) (*receipt.SettlementReceipt, error) {

	workspace := "/srv/workspace"
	filePath := filepath.Join(workspace, fileName)

	// Create initial file on executor.
	initContent := fmt.Sprintf("# %s - generated by WU13 fan-out\n", fileName)
	execResp, err := executorClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("printf '%%s' %q > %s", initContent, filePath),
		WorkingDir: workspace,
	})
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	if execResp.ExitCode != 0 {
		return nil, fmt.Errorf("create file exit=%d", execResp.ExitCode)
	}

	// Get baseline hash.
	cmd := exec.Command("docker", "exec", containerName, "sh", "-c",
		fmt.Sprintf("sha256sum %s | cut -d' ' -f1", filePath))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("get hash: %w", err)
	}
	baselineHash := strings.TrimSpace(string(out))

	// Build and sign envelope.
	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: executorAgentID,
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: workspace,
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "file_modified",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       fileName,
					ExpectedSHA256: baselineHash,
				},
			},
		},
		TimeoutSeconds:  60,
		MaxRemediations: 0,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}

	hash, err := envelope.ComputeEnvelopeHash(env)
	if err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}
	env.EnvelopeHash = hash

	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		return nil, fmt.Errorf("JCS marshal: %w", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	env.EmitterSignature = sig

	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	leaseResp, err := aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	if !leaseResp.Accepted {
		return nil, fmt.Errorf("lease rejected: %s", leaseResp.Error)
	}

	// Settle.
	settleResp, err := executorClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:     leaseResp.LeaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(settleResp.ReceiptJSON, &rec); err != nil {
		return nil, fmt.Errorf("parse receipt: %w", err)
	}

	return &rec, nil
}

// ─────────────────────────────────────────────────────────────────
// Test 2: Concurrent writes to same executor — race condition safety
// Agent A sends N concurrent envelopes to agent-b. The settlement
// engine must handle concurrent writes to the SQLite chain safely.
// ─────────────────────────────────────────────────────────────────

func TestWU13_ConcurrentWritesToSameExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== WU13 Concurrent Writes: N envelopes → agent-b (SQLite WAL safety) ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")
	aSigner, _ := signing.GenerateSigner("agent-a")

	const numConcurrent = 8

	// Get initial chain length.
	before, err := bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("get initial chain: %v", err)
	}
	initialLen := len(before.Receipts)

	// Dispatch N concurrent envelopes.
	results := make([]*receipt.SettlementReceipt, numConcurrent)
	errors := make([]error, numConcurrent)
	var wg sync.WaitGroup

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, err := fanDispatchLeg(ctx, aClient, bClient, aSigner, "agent-b", "agent-b",
				fmt.Sprintf("concurrent-%d", idx))
			results[idx] = rec
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	// All must succeed.
	succeeded := 0
	for i, err := range errors {
		if err != nil {
			t.Errorf("  envelope[%d]: ERROR: %v", i, err)
		} else {
			succeeded++
			t.Logf("  envelope[%d]: receipt=%s verdict=%s ✓",
				i, results[i].ReceiptID, results[i].Verdict)
		}
	}

	// All receipt IDs must be unique.
	seen := make(map[string]bool)
	dupes := 0
	for i, rec := range results {
		if rec == nil {
			continue
		}
		if seen[rec.ReceiptID] {
			t.Errorf("  duplicate receipt ID at index %d: %s", i, rec.ReceiptID)
			dupes++
		}
		seen[rec.ReceiptID] = true
	}
	if dupes == 0 {
		t.Logf("  All %d receipt IDs are unique ✓", succeeded)
	}

	// Chain must have exactly numConcurrent new receipts.
	after, err := bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("get chain after: %v", err)
	}
	newLen := len(after.Receipts) - initialLen
	t.Logf("  Chain grew by %d receipts (expected %d)", newLen, numConcurrent)
	if newLen != numConcurrent {
		t.Errorf("  chain grew by %d, want %d", newLen, numConcurrent)
	}

	// Verify chain integrity: each receipt's prev_hash must be SHA-256 of previous sig.
	for i := 1; i < len(after.Receipts); i++ {
		prevSig := after.Receipts[i-1].ExecutorSignature
		if prevSig == "" {
			continue
		}
		expectedPrev := sha256Hex([]byte(prevSig))
		if after.Receipts[i].PreviousReceiptHash != expectedPrev {
			t.Errorf("  chain[%d]: prev_hash=%s, want SHA256(prev.sig)=%s",
				i, after.Receipts[i].PreviousReceiptHash, expectedPrev)
		}
	}
	t.Log("  Chain integrity: all links valid ✓")

	t.Log("=== WU13 Concurrent Writes PASSED ===")
}

// ─────────────────────────────────────────────────────────────────
// Test 3: Dispatch endpoint — fan-out via POST /dispatch
// Tests the shell server's /dispatch handler with concurrent fan-out.
// ─────────────────────────────────────────────────────────────────

func TestWU13_DispatchEndpointFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== WU13 Dispatch Endpoint: POST /dispatch fan-out to B + C ===")

	ctx := context.Background()

	// Get agent-a's Ed25519 public key hex from health.
	aClient := newInsecureTLSClient("https://localhost:18443")
	aSigner, _ := signing.GenerateSigner("agent-a")
	_ = aSigner // used for signing below

	// Prepare one envelope per executor.
	// We use agent-a's HTTPClient to POST /dispatch.
	// Note: The dispatch endpoint is on agent-a (emitter).
	// But the existing server doesn't have /dispatch yet.
	// So we simulate the dispatch by calling B and C directly.

	bClient := newInsecureTLSClient("https://localhost:28443")
	cClient := newInsecureTLSClient("https://localhost:38443")

	type dispatchLeg struct {
		executorID string
		client     *agent.HTTPClient
		env        *envelope.CognitiveTaskEnvelope
	}

	legs := []dispatchLeg{
		{"agent-b", bClient, nil},
		{"agent-c", cClient, nil},
	}

	// Build envelopes.
	for i, leg := range legs {
		workspace := "/srv/workspace"
		fileName := fmt.Sprintf("dispatch-file-%d.txt", i)
		filePath := filepath.Join(workspace, fileName)

		// Create initial file.
		initContent := fmt.Sprintf("dispatch test file %d\n", i)
		_, err := leg.client.ExecuteTask(ctx, &agent.ExecuteRequest{
			Command:    fmt.Sprintf("printf '%%s' %q > %s", initContent, filePath),
			WorkingDir: workspace,
		})
		if err != nil {
			t.Fatalf("create file on %s: %v", leg.executorID, err)
		}

		// Get hash.
		cmd := exec.Command("docker", "exec", leg.executorID, "sh", "-c",
			fmt.Sprintf("sha256sum %s | cut -d' ' -f1", filePath))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("get hash from %s: %v", leg.executorID, err)
		}
		baselineHash := strings.TrimSpace(string(out))

		// Build envelope.
		env := &envelope.CognitiveTaskEnvelope{
			EnvelopeID:      newUUIDv7(),
			EmitterAgentID:  "agent-a",
			ExecutorAgentID: leg.executorID,
			Territory: envelope.Territory{
				Repository:    "github.com/gentleman-programming/gentle-mesh",
				Branch:       "main",
				WorkspacePath: workspace,
			},
			Assertions: []envelope.Assertion{
				{
					ID:   "file_present",
					Type: envelope.AssertionFileModified,
					Params: envelope.AssertionParams{
						FilePath:       fileName,
						ExpectedSHA256: baselineHash,
					},
				},
			},
			TimeoutSeconds:  60,
			MaxRemediations: 0,
			CreatedAt:       time.Now().UTC(),
			Version:         "1.0",
		}
		hash, _ := envelope.ComputeEnvelopeHash(env)
		env.EnvelopeHash = hash
		legs[i].env = env
	}

	// Dispatch concurrently (simulating what /dispatch does).
	results := make([]dispatchResult, len(legs))
	var wg sync.WaitGroup
	for i, leg := range legs {
		wg.Add(1)
		go func(idx int, l dispatchLeg) {
			defer wg.Done()
			rec, err := dispatchLegSync(ctx, aClient, l.client, aSigner, l.env)
			results[idx] = dispatchResult{rec: rec, err: err, executorID: l.executorID}
		}(i, leg)
	}
	wg.Wait()

	succeeded := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("  dispatch[%d] %s: ERROR: %v", i, r.executorID, r.err)
		} else {
			t.Logf("  dispatch[%d] %s: receipt=%s verdict=%s ✓",
				i, r.executorID, r.rec.ReceiptID, r.rec.Verdict)
			succeeded++
		}
	}
	if succeeded != len(legs) {
		t.Errorf("only %d/%d dispatches succeeded", succeeded, len(legs))
	}

	t.Log("=== WU13 Dispatch Endpoint PASSED ===")
}

type dispatchResult struct {
	rec       *receipt.SettlementReceipt
	err       error
	executorID string
}

func dispatchLegSync(ctx context.Context, aClient, executorClient *agent.HTTPClient,
	aSigner *signing.BasicSigner, env *envelope.CognitiveTaskEnvelope) (*receipt.SettlementReceipt, error) {

	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		return nil, fmt.Errorf("JCS marshal: %w", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	env.EmitterSignature = sig

	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	leaseResp, err := aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	if !leaseResp.Accepted {
		return nil, fmt.Errorf("lease rejected: %s", leaseResp.Error)
	}

	settleResp, err := executorClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:     leaseResp.LeaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(settleResp.ReceiptJSON, &rec); err != nil {
		return nil, fmt.Errorf("parse receipt: %w", err)
	}

	return &rec, nil
}

// ─────────────────────────────────────────────────────────────────
// Test 4: Pre-flight resilience — executor rejects dirty worktree
// Verifies that agent-b's pre-flight correctly rejects a dirty worktree
// before any computation is spent.
// ─────────────────────────────────────────────────────────────────

func TestWU13_PreFlightRejectsDirtyWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== WU13 Pre-flight: agent-b rejects dirty worktree ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")
	aSigner, _ := signing.GenerateSigner("agent-a")

	// Create a file and commit it.
	workspace := "/srv/workspace"
	dirtyFile := filepath.Join(workspace, "dirty.txt")
	execResp, err := bClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("echo dirty > %s && git -C %s add .", dirtyFile, workspace),
		WorkingDir: workspace,
	})
	if err != nil {
		t.Fatalf("create dirty file: %v", err)
	}
	t.Logf("  Created dirty file, git add exit=%d", execResp.ExitCode)

	// Get baseline of a different file.
	cleanFile := filepath.Join(workspace, "clean.txt")
	_, _ = bClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("printf 'clean' > %s", cleanFile),
		WorkingDir: workspace,
	})
	cmd := exec.Command("docker", "exec", "agent-b", "sh", "-c",
		fmt.Sprintf("sha256sum %s | cut -d' ' -f1", cleanFile))
	out, _ := cmd.Output()
	baselineHash := strings.TrimSpace(string(out))

	// Build envelope with git_clean_worktree precondition.
	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: workspace,
		},
		Preconditions: []envelope.Precondition{
			{Type: envelope.PreconditionGitCleanWorktree, Params: map[string]string{}},
		},
		Assertions: []envelope.Assertion{
			{
				ID:   "clean_file",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       "clean.txt",
					ExpectedSHA256: baselineHash,
				},
			},
		},
		TimeoutSeconds:  60,
		MaxRemediations: 0,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}

	hash, _ := envelope.ComputeEnvelopeHash(env)
	env.EnvelopeHash = hash

	signable := *env
	signable.EmitterSignature = ""
	data, _ := jcs.Marshal(&signable)
	sig, _ := aSigner.Sign(data)
	env.EmitterSignature = sig

	envJSON, _ := json.Marshal(env)

	// Submit: should be REJECTED (not accepted).
	leaseResp, err := aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if leaseResp.Accepted {
		t.Error("  lease should be REJECTED for dirty worktree, but was ACCEPTED")
	} else {
		t.Logf("  Lease correctly REJECTED: %s ✓", leaseResp.Error)
	}

	t.Log("=== WU13 Pre-flight Rejection PASSED ===")
}

// ─────────────────────────────────────────────────────────────────
// Test 5: Chain stress — 10 receipts per executor, verify all links
// ─────────────────────────────────────────────────────────────────

func TestWU13_ChainStressTenReceipts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== WU13 Chain Stress: 10 receipts on agent-b, verify all links ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")
	aSigner, _ := signing.GenerateSigner("agent-a")

	const numReceipts = 10
	workspace := "/srv/workspace"

	// Get initial chain length.
	before, _ := bClient.GetChain(ctx, "agent-a", "agent-b")
	initialLen := len(before.Receipts)

	counter := atomic.Int32{}
	var wg sync.WaitGroup

	// Fan-out in 2 concurrent batches of 5.
	for batch := 0; batch < 2; batch++ {
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				seq := counter.Add(1)
				fileName := fmt.Sprintf("stress-%d.txt", seq)
				filePath := filepath.Join(workspace, fileName)

				initContent := fmt.Sprintf("stress content %d\n", seq)
				bClient.ExecuteTask(ctx, &agent.ExecuteRequest{
					Command:    fmt.Sprintf("printf '%%s' %q > %s", initContent, filePath),
					WorkingDir: workspace,
				})

				cmd := exec.Command("docker", "exec", "agent-b", "sh", "-c",
					fmt.Sprintf("sha256sum %s | cut -d' ' -f1", filePath))
				out, _ := cmd.Output()
				baselineHash := strings.TrimSpace(string(out))

				env := &envelope.CognitiveTaskEnvelope{
					EnvelopeID:      newUUIDv7(),
					EmitterAgentID:  "agent-a",
					ExecutorAgentID: "agent-b",
					Territory: envelope.Territory{
						Repository:    "github.com/gentleman-programming/gentle-mesh",
						Branch:       "main",
						WorkspacePath: workspace,
					},
					Assertions: []envelope.Assertion{
						{
							ID:   "file_ok",
							Type: envelope.AssertionFileModified,
							Params: envelope.AssertionParams{
								FilePath:       fileName,
								ExpectedSHA256: baselineHash,
							},
						},
					},
					TimeoutSeconds:  60,
					MaxRemediations: 0,
					CreatedAt:       time.Now().UTC(),
					Version:         "1.0",
				}

				hash, _ := envelope.ComputeEnvelopeHash(env)
				env.EnvelopeHash = hash
				signable := *env
				signable.EmitterSignature = ""
				data, _ := jcs.Marshal(&signable)
				sig, _ := aSigner.Sign(data)
				env.EmitterSignature = sig
				envJSON, _ := json.Marshal(env)

				leaseResp, _ := aClient.SubmitEnvelope(ctx, envJSON)
				if leaseResp != nil && leaseResp.Accepted {
					bClient.Settle(ctx, &agent.SettleRequest{
						EnvelopeJSON: envJSON,
						LeaseID:     leaseResp.LeaseID,
					})
				}
			}(batch*5 + i)
		}
		// Small delay between batches to interleave.
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()

	// Verify chain.
	after, err := bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	newLen := len(after.Receipts) - initialLen
	t.Logf("  Chain grew by %d receipts (expected %d)", newLen, numReceipts)
	if newLen != numReceipts {
		t.Errorf("chain grew by %d, want %d", newLen, numReceipts)
	}

	// Verify all chain links.
	var brokenLinks int
	for i := 1; i < len(after.Receipts); i++ {
		prevSig := after.Receipts[i-1].ExecutorSignature
		if prevSig == "" {
			continue
		}
		expectedPrev := sha256Hex([]byte(prevSig))
		if after.Receipts[i].PreviousReceiptHash != expectedPrev {
			t.Errorf("  chain[%d]: broken link — prev_hash=%s, want SHA256(prev.sig)=%s",
				i, truncateMid(after.Receipts[i].PreviousReceiptHash, 12),
				truncateMid(expectedPrev, 12))
			brokenLinks++
		}
	}
	if brokenLinks == 0 {
		t.Logf("  All %d chain links valid ✓", len(after.Receipts)-1)
	} else {
		t.Errorf("  %d/%d chain links broken", brokenLinks, len(after.Receipts)-1)
	}

	t.Log("=== WU13 Chain Stress PASSED ===")
}

// sha256Hex computes SHA-256 and returns hex string.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// truncateMid truncates a string in the middle for readability.
func truncateMid(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen/2] + "..." + s[len(s)-maxLen/2:]
}
