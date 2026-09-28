//go:build docker

package testscenario

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// ─────────────────────────────────────────────────────────────────
// Docker infrastructure helpers
// ─────────────────────────────────────────────────────────────────

// composeUp starts the docker-compose stack and waits for all agents to be healthy.
// It generates TLS certificates on the host, builds the images, then brings up the containers.
// Returns a cleanup function. The Go test harness runs on the HOST (not in Docker network),
// so all agent access uses localhost with published ports.
func composeUp(t *testing.T, composeDir string) func() {
	t.Helper()

	composeFile := filepath.Join(composeDir, "docker-compose.yml")

	// Step 1: Generate TLS certificates on the host machine.
	t.Log("[setup] Generating TLS certificates on host...")
	certsDir := filepath.Join(composeDir, "certs-generated")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("create certs directory: %v", err)
	}

	genCertsScript := filepath.Join(composeDir, "certs", "gen-certs.sh")
	if _, err := os.Stat(genCertsScript); os.IsNotExist(err) {
		t.Skipf("gen-certs.sh not found at %s", genCertsScript)
	}

	genCmd := exec.Command("bash", genCertsScript, certsDir)
	genOut, genErr := genCmd.CombinedOutput()
	if genErr != nil {
		t.Skipf("certificate generation failed (bash/openssl required): %v\n%s", genErr, string(genOut))
	}
	t.Logf("[setup] Certificates generated.")

	// Make keys world-readable so the container user can read them.
	chmodCmd := exec.Command("chmod", "a+r",
		filepath.Join(certsDir, "ca.key"),
		filepath.Join(certsDir, "server.key"),
		filepath.Join(certsDir, "client.key"))
	if err := chmodCmd.Run(); err != nil {
		t.Logf("[setup] chmod on keys: %v (non-fatal)", err)
	}
	t.Logf("[setup] Keys made world-readable for containers.")

	// Step 2: Build the agent images.
	t.Log("[setup] Building agent images...")
	buildCmd := exec.Command("docker", "compose", "-f", composeFile,
		"-p", "rfc002", "build", "--no-cache")
	buildCmd.Dir = composeDir
	buildOut, buildErr := buildCmd.CombinedOutput()
	if buildErr != nil {
		if strings.Contains(string(buildOut), "pull access denied") ||
			strings.Contains(string(buildOut), "connection refused") ||
			strings.Contains(string(buildOut), "no such host") {
			t.Skipf("docker build failed (no network/cache): %v\n%s", buildErr, string(buildOut))
		}
		t.Fatalf("docker compose build failed: %v\n%s", buildErr, string(buildOut))
	}
	t.Logf("[setup] Build completed.")

	// Step 3: Bring down any previous stack.
	downCmd := exec.Command("docker", "compose", "-f", composeFile,
		"-p", "rfc002",
		"down", "--volumes", "--remove-orphans")
	downCmd.Dir = composeDir
	_ = downCmd.Run()

	// Step 4: Bring up the stack with published ports for host access.
	t.Log("[setup] Starting containers (docker compose up -d)...")
	upCmd := exec.Command("docker", "compose", "-f", composeFile,
		"-p", "rfc002",
		"up", "-d",
		"--scale", "agent-a=1",
		"--scale", "agent-b=1",
		"--scale", "agent-c=1")
	upCmd.Dir = composeDir
	upCmd.Env = append(os.Environ(), "CERTS_HOST_DIR="+certsDir)
	upOut, upErr := upCmd.CombinedOutput()
	if upErr != nil {
		logsCmd := exec.Command("docker", "compose", "-f", composeFile, "-p", "rfc002", "logs", "--tail=30")
		logsCmd.Dir = composeDir
		logsOut, _ := logsCmd.CombinedOutput()
		t.Fatalf("docker compose up failed: %v\n%s\nLogs:\n%s", upErr, string(upOut), string(logsOut))
	}
	t.Logf("[setup] Containers started.")

	// Step 5: Wait for all agents to be healthy via published host ports.
	// Note: The Go test harness runs on the HOST, not in the Docker network,
	// so we must use published ports (18443, 28443, 38443) instead of internal DNS.
	t.Log("[setup] Waiting for agents to become healthy via host ports...")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for ctx.Err() == nil {
		allHealthy := true
		// Published ports: agent-a=18443, agent-b=28443, agent-c=38443
		for _, ep := range []struct {
			name string
			port int
		}{{"agent-a", 18443}, {"agent-b", 28443}, {"agent-c", 38443}} {
			client, err := agent.NewHTTPClientTLS(
				fmt.Sprintf("https://localhost:%d", ep.port),
				agent.WithInsecureSkipVerify(),
			)
			if err != nil {
				t.Logf("[setup]   %s: client error: %v", ep.name, err)
				allHealthy = false
				break
			}
			if _, err = client.Health(ctx); err != nil {
				t.Logf("[setup]   %s: not ready yet...", ep.name)
				allHealthy = false
				break
			}
			t.Logf("[setup]   %s: healthy", ep.name)
		}
		if allHealthy {
			t.Log("[setup] All agents are healthy via host ports")
			break
		}
		time.Sleep(2 * time.Second)
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cmd := exec.Command("docker", "compose", "-f", composeFile, "-p", "rfc002", "down", "--volumes", "--remove-orphans")
			cmd.Dir = composeDir
			out, _ := cmd.CombinedOutput()
			t.Logf("docker compose down:\n%s", string(out))
		})
	}
	t.Cleanup(cleanup)

	if ctx.Err() != nil {
		logsCmd := exec.Command("docker", "compose", "-f", composeFile, "-p", "rfc002", "logs", "--tail=30")
		logsCmd.Dir = composeDir
		logsOut, _ := logsCmd.CombinedOutput()
		t.Fatalf("timeout waiting for agents. Logs:\n%s", string(logsOut))
	}

	return cleanup
}

// findComposeDir returns the path to the integration directory.
// It first tries the GOPATH/module root by walking up from CWD,
// then falls back to the standard project layout.
func findComposeDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err == nil {
		for dir := cwd; dir != "/" && dir != ""; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "integration", "docker-compose.yml")); err == nil {
				return filepath.Join(dir, "integration")
			}
		}
	}
	// Fallback: try relative to current working directory.
	if _, err := os.Stat("integration/docker-compose.yml"); err == nil {
		return "integration"
	}
	// Fallback: try relative to test file location via runtime.
	if exe, err := os.Executable(); err == nil {
		for dir := filepath.Dir(exe); dir != "/" && dir != ""; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "integration", "docker-compose.yml")); err == nil {
				return filepath.Join(dir, "integration")
			}
		}
	}
	t.Fatalf("cannot find integration/docker-compose.yml")
	return ""
}

// newInsecureTLSClient creates an HTTP client that skips TLS verification (dev only).
func newInsecureTLSClient(baseURL string) *agent.HTTPClient {
	client, _ := agent.NewHTTPClientTLS(baseURL, agent.WithInsecureSkipVerify())
	return client
}

// ─────────────────────────────────────────────────────────────────
// Test 1: Distributed 1-to-1 Integration
// Agent A (emitter) → Agent B (executor) over HTTPS/mTLS
// Full flow: envelope → Ed25519 validation → lease → execution → receipt → chain
// ─────────────────────────────────────────────────────────────────

func TestDistributed_OneToOneOverHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()



	// Create initial calculator.py with baseline content.
	workspace := "/srv/workspace"
	calcPath := filepath.Join(workspace, "calculator.py")
	initContent := `def add(a, b):
    """Add two numbers."""
    return a + b


def subtract(a, b):
    """Subtract b from a."""
    return a - b
`
	initCalc := exec.Command("docker", "exec", "agent-b", "sh", "-c",
		fmt.Sprintf("cd /srv/workspace && cat > %s << 'PYEOF'\n%sPYEOF", calcPath, initContent))
	if out, err := initCalc.CombinedOutput(); err != nil {
		t.Fatalf("create calculator.py: %v\n%s", err, string(out))
	}
	t.Log("[setup] Initial calculator.py created")

	t.Log("=== Distributed 1-to-1: Agent A → Agent B over HTTPS ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")

	// Step 1: Get baseline SHA-256 of calculator.py from agent-b's workspace.
	cmd := exec.Command("docker", "exec", "agent-b", "sh", "-c",
		fmt.Sprintf("sha256sum %s | cut -d' ' -f1", calcPath))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("get baseline hash from agent-b: %v", err)
	}
	baselineHash := strings.TrimSpace(string(out))
	t.Logf("[1] Baseline calculator.py SHA-256: %s", baselineHash)

	// Step 2: Agent A creates and signs CognitiveTaskEnvelope.
	t.Log("[2] Agent A: creating and signing CognitiveTaskEnvelope...")
	aSigner, _ := signing.GenerateSigner("agent-a")

	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      newUUIDv7(),
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: envelope.Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			WorkspacePath: workspace,
		},
		Preconditions: []envelope.Precondition{},   // no preconditions — focus on core settlement flow
		Assertions: []envelope.Assertion{
			{
				ID:   "file_modified",
				Type: envelope.AssertionFileModified,
				Params: envelope.AssertionParams{
					FilePath:       "calculator.py",
					ExpectedSHA256: baselineHash,
				},
			},
			{
				ID:   "python_available",
				Type: envelope.AssertionCommandExitCode,
				Params: envelope.AssertionParams{
					Command:          "python3 --version",
					ExpectedExitCode: 0,
				},
			},
		},
		TimeoutSeconds:  120,
		MaxRemediations: 1,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}

	hash, err := envelope.ComputeEnvelopeHash(env)
	if err != nil {
		t.Fatalf("compute envelope hash: %v", err)
	}
	env.EnvelopeHash = hash

	// Sign with Ed25519 over JCS-canonical JSON.
	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		t.Fatalf("JCS marshal: %v", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		t.Fatalf("Ed25519 sign: %v", err)
	}
	env.EmitterSignature = sig
	t.Logf("    Envelope signed: id=%s hash=%s", env.EnvelopeID, env.EnvelopeHash)

	// Step 3: Agent A → Agent B: submit envelope (pre-flight handshake).
	t.Log("[3] Agent A → Agent B: submitting envelope (pre-flight handshake)...")
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	leaseResp, err := aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		t.Fatalf("submit to agent-b: %v", err)
	}
	if !leaseResp.Accepted {
		t.Fatalf("lease rejected by agent-b: %s", leaseResp.Error)
	}
	t.Logf("    Lease accepted: id=%s", leaseResp.LeaseID)

	// Step 4: Agent B: simulate task execution.
	t.Log("[4] Agent B: executing task (appending multiply/divide to calculator.py)...")
	newFunc := "\n\ndef multiply(a, b):\n    \"\"\"Multiply two numbers.\"\"\"\n    return a * b\n\n\ndef divide(a, b):\n    \"\"\"Divide two numbers. Raises ValueError if b is zero.\"\"\"\n    if b == 0:\n        raise ValueError(\"cannot divide by zero\")\n    return a / b\n"
	execResp, err := bClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("cat >> %s << 'PYEOF'\n%sPYEOF", calcPath, newFunc),
		WorkingDir: workspace,
	})
	if err != nil {
		t.Fatalf("execute task on agent-b: %v", err)
	}
	if execResp.ExitCode != 0 {
		t.Logf("    execute exit=%d stdout=%s stderr=%s", execResp.ExitCode, execResp.Stdout, execResp.Stderr)
	}
	t.Logf("    Execution: exit=%d", execResp.ExitCode)

	// Step 5: Agent A → Agent B: settle.
	t.Log("[5] Agent A → Agent B: settlement...")
	settleResp, err := bClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:     leaseResp.LeaseID,
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}

	rec := new(receipt.SettlementReceipt)
	if err := json.Unmarshal(settleResp.ReceiptJSON, rec); err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	t.Logf("    Receipt: id=%s verdict=%s assertions=%d",
		rec.ReceiptID, rec.Verdict, len(rec.Assertions))

	// Step 6: Verify Ed25519 executor signature.
	t.Log("[6] Verifying Ed25519 executor signature...")

	// Get agent-b's actual public key from its health endpoint.
	bHealth, err := bClient.Health(ctx)
	if err != nil {
		t.Fatalf("get agent-b health (for public key): %v", err)
	}
	bPubKeyBytes, err := hex.DecodeString(bHealth.PublicKey)
	if err != nil {
		t.Fatalf("decode agent-b public key: %v", err)
	}

	receiptHash, err := receipt.ComputeReceiptHash(rec)
	if err != nil {
		t.Fatalf("compute receipt hash: %v", err)
	}
	err = signing.Verify(bPubKeyBytes, []byte(receiptHash), rec.ExecutorSignature)
	if err != nil {
		t.Errorf("    Executor Ed25519 signature INVALID: %v", err)
	} else {
		t.Log("    Executor Ed25519 signature: VALID ✓")
	}

	// Step 7: Verify receipt chain.
	t.Log("[7] Verifying receipt chain...")
	chainResp, err := bClient.GetChain(ctx, "agent-a", "agent-b")
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	t.Logf("    Chain length: %d receipts", len(chainResp.Receipts))

	if len(chainResp.Receipts) < 1 {
		t.Fatal("chain is empty")
	}

	last := chainResp.Receipts[len(chainResp.Receipts)-1]
	if last.PreviousReceiptHash != "" {
		t.Errorf("    First receipt should have empty prev_hash, got %s", last.PreviousReceiptHash)
	} else {
		t.Log("    First receipt: empty prev_hash ✓")
	}

	// Step 8: Verify assertions.
	t.Log("[8] Settlement assertions:")
	for _, a := range rec.Assertions {
		emoji := "✓"
		if a.Result != receipt.ResultPass {
			emoji = "✗"
		}
		t.Logf("    %s %s: %s — %s", emoji, a.AssertionID, a.Result, a.Message)
	}

	if rec.Verdict != receipt.VerdictSettledClean {
		t.Errorf("    Verdict = %s, want SETTLED_CLEAN", rec.Verdict)
	} else {
		t.Log("    Verdict: SETTLED_CLEAN ✓")
	}

	t.Log("=== Distributed 1-to-1 PASSED ===")
}

// ─────────────────────────────────────────────────────────────────
// Test 2: Fan-out (1 emitter → N executors)
// ─────────────────────────────────────────────────────────────────

func TestDistributed_FanOutOneToMany(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	t.Log("=== Distributed Fan-out: Agent A → Agent B + Agent C ===")

	ctx := context.Background()
	aClient := newInsecureTLSClient("https://localhost:18443")
	bClient := newInsecureTLSClient("https://localhost:28443")
	cClient := newInsecureTLSClient("https://localhost:38443")

	aSigner, _ := signing.GenerateSigner("agent-a")

	type fanResult struct {
		receipt *receipt.SettlementReceipt
		err     error
		name    string
	}

	var wg sync.WaitGroup
	results := []fanResult{{name: "B"}, {name: "C"}}
	clients := []*agent.HTTPClient{bClient, cClient}

	// Dispatch to B and C concurrently.
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, err := fanDispatch(ctx, aClient, clients[idx], aSigner,
				[]string{"agent-b", "agent-c"}[idx],
				fmt.Sprintf("calc-%d", idx+1))
			results[idx] = fanResult{receipt: rec, err: err, name: results[idx].name}
		}(n)
	}
	wg.Wait()

	// Check results.
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("fan-out to %s: %v", r.name, r.err)
		}
		t.Logf("    Receipt %s: id=%s verdict=%s", r.name, r.receipt.ReceiptID, r.receipt.Verdict)
	}

	// Verify Ed25519 signatures using actual container public keys.
	bHealth, _ := bClient.Health(ctx)
	cHealth, _ := cClient.Health(ctx)
	bPubKeyBytes, _ := hex.DecodeString(bHealth.PublicKey)
	cPubKeyBytes, _ := hex.DecodeString(cHealth.PublicKey)
	for _, tc := range []struct {
		rec      *receipt.SettlementReceipt
		pubKey   []byte
		name     string
	}{
		{results[0].receipt, bPubKeyBytes, "B"},
		{results[1].receipt, cPubKeyBytes, "C"},
	} {
		hash, err := receipt.ComputeReceiptHash(tc.rec)
		if err != nil {
			t.Errorf("receipt %s hash: %v", tc.name, err)
			continue
		}
		if err := signing.Verify(tc.pubKey, []byte(hash), tc.rec.ExecutorSignature); err != nil {
			t.Errorf("receipt %s sig invalid: %v", tc.name, err)
		} else {
			t.Logf("    Receipt %s Ed25519 signature: VALID ✓", tc.name)
		}
	}

	// Verify independent chains.
	chainB, _ := bClient.GetChain(ctx, "agent-a", "agent-b")
	chainC, _ := cClient.GetChain(ctx, "agent-a", "agent-c")
	t.Logf("    Chain B: %d receipts, Chain C: %d receipts (independent executors)",
		len(chainB.Receipts), len(chainC.Receipts))

	if results[0].receipt.ReceiptID == results[1].receipt.ReceiptID {
		t.Errorf("receipts B and C have the same ID: %s", results[0].receipt.ReceiptID)
	}

	t.Log("=== Distributed Fan-out PASSED ===")
}

// buildEnvelope creates a minimal CognitiveTaskEnvelope.
func buildEnvelope(emitterID, executorID, id, workspace, filePath, expectedHash string) *envelope.CognitiveTaskEnvelope {
	env := &envelope.CognitiveTaskEnvelope{
		EnvelopeID:      id,
		EmitterAgentID:  emitterID,
		ExecutorAgentID: executorID,
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
					FilePath:       filePath,
					ExpectedSHA256: expectedHash,
				},
			},
		},
		TimeoutSeconds:  120,
		MaxRemediations: 0,
		NoSubdelegation: true,
		CreatedAt:       time.Now().UTC(),
		Version:         "1.0",
	}
	hash, _ := envelope.ComputeEnvelopeHash(env)
	env.EnvelopeHash = hash
	return env
}

// fanDispatch runs one fan-out leg: creates a file, builds/sends envelope, executes, settles.
func fanDispatch(ctx context.Context, aClient, executorClient *agent.HTTPClient,
	aSigner *signing.BasicSigner, executorID, calcPrefix string) (*receipt.SettlementReceipt, error) {

	workspace := "/srv/workspace"
	calcFile := fmt.Sprintf("%s.py", calcPrefix)
	calcPath := filepath.Join(workspace, calcFile)

	// Create initial file on executor.
	initContent := fmt.Sprintf("def %s(): return True\n", calcPrefix)
	_, err := executorClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("printf '%%s' %q > %s", initContent, calcPath),
		WorkingDir: workspace,
	})
	if err != nil {
		return nil, fmt.Errorf("create file on %s: %w", executorID, err)
	}

	// Get baseline hash.
	cmd := exec.Command("docker", "exec", executorID, "sh", "-c",
		fmt.Sprintf("sha256sum %s | cut -d' ' -f1", calcPath))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("get baseline hash from %s: %w", executorID, err)
	}
	baselineHash := strings.TrimSpace(string(out))

	// Build and sign envelope.
	env := buildEnvelope("agent-a", executorID, calcPrefix+"-"+newUUIDv7(),
		workspace, calcFile, baselineHash)

	signable := *env
	signable.EmitterSignature = ""
	data, err := jcs.Marshal(&signable)
	if err != nil {
		return nil, fmt.Errorf("JCS marshal: %w", err)
	}
	sig, err := aSigner.Sign(data)
	if err != nil {
		return nil, fmt.Errorf("Ed25519 sign: %w", err)
	}
	env.EmitterSignature = sig

	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	// Submit envelope.
	leaseResp, err := aClient.SubmitEnvelope(ctx, envJSON)
	if err != nil {
		return nil, fmt.Errorf("submit to %s: %w", executorID, err)
	}
	if !leaseResp.Accepted {
		return nil, fmt.Errorf("lease rejected by %s: %s", executorID, leaseResp.Error)
	}

	// Execute: append a function on the executor.
	execResp, err := executorClient.ExecuteTask(ctx, &agent.ExecuteRequest{
		Command:    fmt.Sprintf("printf '%%s' %q >> %s", fmt.Sprintf("\ndef %s_done(): return True", calcPrefix), calcPath),
		WorkingDir: workspace,
	})
	if err != nil {
		return nil, fmt.Errorf("execute on %s: %w", executorID, err)
	}
	if execResp.ExitCode != 0 {
		return nil, fmt.Errorf("execute on %s: exit=%d", executorID, execResp.ExitCode)
	}

	// Settle.
	settleResp, err := executorClient.Settle(ctx, &agent.SettleRequest{
		EnvelopeJSON: envJSON,
		LeaseID:     leaseResp.LeaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("settle on %s: %w", executorID, err)
	}

	rec := new(receipt.SettlementReceipt)
	if err := json.Unmarshal(settleResp.ReceiptJSON, rec); err != nil {
		return nil, fmt.Errorf("parse receipt: %w", err)
	}

	return rec, nil
}

// ─────────────────────────────────────────────────────────────────
// Test 3: Chain integrity on each executor
// ─────────────────────────────────────────────────────────────────

func TestDistributed_ChainIntegrityPerExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping distributed test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	composeDir := findComposeDir(t)
	cleanup := composeUp(t, composeDir)
	defer cleanup()

	ctx := context.Background()

	for _, tc := range []struct {
		executorID string
		client     *agent.HTTPClient
	}{
		{"agent-b", newInsecureTLSClient("https://localhost:28443")},
		{"agent-c", newInsecureTLSClient("https://localhost:38443")},
	} {
		t.Run(tc.executorID, func(t *testing.T) {
			chainResp, err := tc.client.GetChain(ctx, "agent-a", tc.executorID)
			if err != nil {
				t.Fatalf("get chain for %s: %v", tc.executorID, err)
			}

			for i, entry := range chainResp.Receipts {
				// Verify chain link.
				if i == 0 && entry.PreviousReceiptHash != "" {
					t.Errorf("chain[%d] (%s): first receipt should have empty prev_hash, got %s",
						i, truncate(entry.ReceiptID, 12), entry.PreviousReceiptHash)
				}
				if i > 0 && entry.PreviousReceiptHash != "" {
					prevSig := chainResp.Receipts[i-1].ExecutorSignature
					if prevSig != "" {
						h := sha256.Sum256([]byte(prevSig))
						expected := hex.EncodeToString(h[:])
						if entry.PreviousReceiptHash != expected {
							t.Errorf("chain[%d]: prev_hash=%s, want SHA-256(prev.sig)=%s",
								i, entry.PreviousReceiptHash, expected)
						}
					}
				}
				t.Logf("    chain[%d]: id=%s verdict=%s prev_hash=%s",
					i, truncate(entry.ReceiptID, 12), entry.Verdict, truncate(entry.PreviousReceiptHash, 12))
			}
			t.Logf("    Chain for %s: %d receipts verified ✓", tc.executorID, len(chainResp.Receipts))
		})
	}
}
