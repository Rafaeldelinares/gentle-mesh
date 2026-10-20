//go:build f2real

// RFC-002 Phase 2 (F2-1b): controlled end-to-end validation of the full A2A
// cycle with REAL execution.
//
// Criterio (roadmap Fase 2): validate that the A2A protocol works with real
// agents — the cognitive contract (RFC-002: submit → lease → settle → receipt,
// hardened by S1/S2/R1/R2) wrapped around REAL work performed by a real pi
// agent inside the gentleshell runner (bwrap sandbox + ephemeral worktree).
//
// Topology (all processes real, no mocks):
//
//	coordinator (gentle-mesh server) ── dispatch ──▶ worker (-runner gentle-shell)
//	                                                    │ bwrap sandbox + pi real
//	                                                    ▼
//	                                          proposal/<taskID> branch (bare repo cache)
//	                                                    │ git clone (stable checkout)
//	                                                    ▼
//	agent executor (test-harness serve, RFC-002 hardened) ── assertions → settle → receipt chain
//
// Worktree lifecycle note: the gentleshell runner removes the ephemeral
// worktree on completion but preserves proposal/<taskID> in the bare repo
// cache — the clone in step 9 gives the RFC-002 assertions a stable view of
// pi's real output.
package f2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/integration/agent"
	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// ─────────────────────────────────────────────────────────────────
// Process scaffolding
// ─────────────────────────────────────────────────────────────────

var f2Procs []*exec.Cmd

func f2KillAll() {
	for _, cmd := range f2Procs {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	f2Procs = nil
}

// f2Start launches a long-lived process with its output in a log file and
// registers it for bulk cleanup. Returns the command for granular control
// (e.g. killing a single worker mid-run).
func f2Start(t *testing.T, work, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = work
	logPath := filepath.Join(work, name+".log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s log: %v", name, err)
	}
	defer out.Close()
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	f2Procs = append(f2Procs, cmd)
	t.Logf("[%s] pid=%d log=%s", name, cmd.Process.Pid, logPath)
	return cmd
}

// f2CoordHTTP performs an authorized request against the coordinator
// (RFC-001 transport requires the bearer token on every route).
func f2CoordHTTP(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer A")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// f2Tail returns the last 1200 chars of a log file for failure diagnostics.
func f2Tail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(no log %s: %v)", path, err)
	}
	if len(data) > 1200 {
		data = data[len(data)-1200:]
	}
	return string(data)
}

// f2WaitFor polls until predicate returns true or the deadline hits.
func f2WaitFor(t *testing.T, what string, deadline time.Duration, predicate func() bool) {
	t.Helper()
	f2WaitForDiag(t, what, deadline, predicate, nil)
}

// f2WaitForDiag includes diagnostic output (e.g. a log tail) in the failure.
func f2WaitForDiag(t *testing.T, what string, deadline time.Duration, predicate func() bool, diag func() string) {
	t.Helper()
	deadlineTime := time.Now().Add(deadline)
	for !predicate() {
		if time.Now().After(deadlineTime) {
			if diag != nil {
				t.Fatalf("timed out waiting for: %s\n--- diag ---\n%s", what, diag())
			}
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("ok: %s", what)
}

func f2FreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// ─────────────────────────────────────────────────────────────────
// The test
// ─────────────────────────────────────────────────────────────────

func TestF2_A2ARealExecution(t *testing.T) {
	for _, dep := range []string{"gentle-shell", "bwrap", "pi", "git", "python3"} {
		if _, err := exec.LookPath(dep); err != nil {
			t.Skipf("dependency %q not found in PATH: %v", dep, err)
		}
	}

	repo := repoRoot(t)
	work := t.TempDir()
	t.Cleanup(f2KillAll)

	// ── Build both binaries from the current tree ──
	meshBin := filepath.Join(work, "gentle-mesh")
	harnessBin := filepath.Join(work, "test-harness")
	f2Build(t, repo, meshBin, "./cmd/gentle-mesh")
	f2Build(t, repo, harnessBin, "./cmd/test-harness", "-tags", "testharness")
	t.Log("ok: binaries built (gentle-mesh + test-harness)")

	// ── Bare repo cache with a seeded main branch ──
	repoCache := f2SeedBareRepo(t, work)
	worktrees := filepath.Join(work, "worktrees")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", repoCache).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	seed := filepath.Join(work, "seed")
	if out, err := exec.Command("git", "clone", repoCache, seed).CombinedOutput(); err != nil {
		t.Fatalf("git clone (seed): %v: %s", err, out)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = seed
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("git", "-C", seed, "config", "user.email", "f2@test.local"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if _, err := run("git", "-C", seed, "config", "user.name", "F2 Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "BASE.md"), []byte("F2 seed\n"), 0o644); err != nil {
		t.Fatalf("write BASE.md: %v", err)
	}
	if _, err := run("git", "-C", seed, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := run("git", "-C", seed, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := run("git", "-C", seed, "push", "origin", "main"); err != nil {
		t.Fatalf("git push seed: %v", err)
	}
	t.Log("ok: bare repo cache seeded with main")

	// ── Coordinator ──
	coordPort := f2FreePort(t)
	f2Start(t, work, "coordinator", meshBin, "server",
		"-addr", fmt.Sprintf("127.0.0.1:%d", coordPort),
		"-tasks-dir", filepath.Join(work, "tasks"),
		"-db-path", filepath.Join(work, "mesh.db"),
		"-token", "A",
		"-heartbeat-timeout", "5s",
	)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)
	f2WaitFor(t, "coordinator up", 30*time.Second, func() bool {
		resp, err := http.Get(coordURL + "/healthz")
		return err == nil && resp.StatusCode == 200
	})

	// ── Worker with the REAL gentleshell runner (bwrap + pi) ──
	workerPort := f2FreePort(t)
	f2Start(t, work, "worker", meshBin, "worker",
		"-coordinator", coordURL,
		"-token", "A",
		"-node-id", "w-f2",
		"-endpoint", fmt.Sprintf("http://127.0.0.1:%d", workerPort),
		"-addr", fmt.Sprintf("127.0.0.1:%d", workerPort),
		"-heartbeat-interval", "1s",
		"-runner", "gentle-shell",
		"-gentle-shell-bin", "gentle-shell",
		"-bwrap-bin", "bwrap",
		"-gentle-shell-repo-cache", repoCache,
		"-gentle-shell-worktrees", worktrees,
		"-gentle-shell-base-ref", "main",
		"-gentle-shell-total-timeout", "8m",
	)
	f2WaitForDiag(t, "worker joined the mesh", 60*time.Second, func() bool {
		status, body := f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		return status == 200 && strings.Contains(body, "w-f2")
	}, func() string {
		return "worker log tail:\n" + f2Tail(filepath.Join(work, "worker.log"))
	})

	// ── Dispatch the real task: pi does the work inside the sandbox ──
	taskBody := `{"agent":"worker","task":"Create a file named hello-f2.txt in the repository root containing exactly one line: RFC-002-F2-OK. Do not create or modify anything else. When the file is created, commit the change with git (git add hello-f2.txt && git commit -m 'f2: add hello file').","timeout_seconds":300}`
	status, body := f2CoordHTTP(t, http.MethodPost, coordURL+"/v1/tasks", taskBody)
	if status != 200 && status != 201 { // 201 Created is the normal dispatch response
		t.Fatalf("dispatch task status = %d: %s", status, body)
	}
	var taskResp struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&taskResp); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if taskResp.TaskID == "" {
		t.Fatal("empty task_id from coordinator")
	}
	t.Logf("ok: task dispatched, task_id=%s (proposal branch will be proposal/%s)", taskResp.TaskID, taskResp.TaskID)

	// ── Wait for real completion ──
	var taskStatus string
	f2WaitForDiag(t, "task completed by real pi execution", 6*time.Minute, func() bool {
		status, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, taskResp.TaskID), "")
		taskStatus = body
		_ = status
		return strings.Contains(taskStatus, `"completed"`)
	}, func() string {
		return fmt.Sprintf("last task state:\n%s\n--- worker log tail ---\n%s", taskStatus, f2Tail(filepath.Join(work, "worker.log")))
	})
	if !strings.Contains(taskStatus, "completed") {
		t.Fatalf("task final state does not include completed: %s", taskStatus)
	}
	t.Logf("task state: %s", taskStatus)

	// ── Stable checkout of pi's real output (proposal branch) ──
	branch := "proposal/" + taskResp.TaskID
	workspace := filepath.Join(work, "workspace")
	if out, err := exec.Command("git", "clone", "--branch", branch, repoCache, workspace).CombinedOutput(); err != nil {
		t.Fatalf("clone proposal branch %s: %v: %s", branch, err, out)
	}
	helloBytes, err := os.ReadFile(filepath.Join(workspace, "hello-f2.txt"))
	if err != nil {
		wd, _ := os.Getwd()
		t.Fatalf("pi's real output missing hello-f2.txt in %s (cwd %s): %v — worker log: %s/worker.log", workspace, wd, err, work)
	}
	if !strings.Contains(string(helloBytes), "RFC-002-F2-OK") {
		t.Fatalf("pi wrote wrong content to hello-f2.txt: %q", string(helloBytes))
	}
	t.Logf("ok: REAL pi execution produced hello-f2.txt with expected content (branch %s)", branch)

	// ── RFC-002 hardened agent (executor side) ──
	agentPort := f2FreePort(t)
	chainDB := filepath.Join(work, "chain.db")
	f2Start(t, work, "agent", harnessBin, "serve",
		"--agent-id", "executor-f2",
		"--mesh-id", "mesh-f2",
		"--role", "executor",
		"--chain-db", chainDB,
		"--workspace", workspace,
		"--port", fmt.Sprintf("%d", agentPort),
	)
	agentURL := fmt.Sprintf("http://127.0.0.1:%d", agentPort)
	f2WaitFor(t, "agent executor up", 30*time.Second, func() bool {
		resp, err := http.Get(agentURL + "/health")
		return err == nil && resp.StatusCode == 200
	})

	// ── Register the emitter's public key (S2) ──
	emitterSigner, err := signing.GenerateSigner("emitter-f2")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}
	regBody, err := json.Marshal(&agent.RegisterKeyRequest{
		AgentID:      "emitter-f2",
		PublicKeyHex: emitterSigner.PublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("marshal register-key: %v", err)
	}
	resp, err := http.Post(agentURL+"/register-key", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatalf("register-key: %v", err)
	}
	resp.Body.Close()
	t.Log("ok: emitter key registered (S2)")

	// ── Submit envelope + lease ──
	env := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-f2",
		EnvelopeID:      "01923456-789a-7bc0-8123-f2a2a-00001",
		EmitterAgentID:  "emitter-f2",
		ExecutorAgentID: "executor-f2",
		TimeoutSeconds:  300,
		MaxRemediations: 0,
		Territory: envelope.Territory{
			Repository:    "https://github.com/gentleman-programming/gentle-mesh", // validator requires a URL-shaped repo, not a local path
			Branch:        branch,
			WorkspacePath: workspace,
		},
		Assertions: []envelope.Assertion{{
			ID:   "hello-file-contains-f2-ok",
			Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{
				Command:          "grep -q RFC-002-F2-OK hello-f2.txt",
				ExpectedExitCode: 0,
			},
		}},
		CreatedAt: time.Now().UTC(),
	}
	if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Wire format: EnvelopeRequest serializes []byte as base64 — the same
	// format the R1/R2 tests use, so the hardened server parses it natively.
	envelopeReq, err := json.Marshal(&agent.EnvelopeRequest{EnvelopeJSON: envJSON})
	if err != nil {
		t.Fatalf("marshal envelope request: %v", err)
	}
	resp, err = http.Post(agentURL+"/envelopes", "application/json", bytes.NewReader(envelopeReq))
	if err != nil {
		t.Fatalf("submit envelope: %v", err)
	}
	var er agent.EnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatalf("decode envelope response: %v", err)
	}
	resp.Body.Close()
	if !er.Accepted || er.LeaseID == "" {
		t.Fatalf("lease not accepted: %+v", er)
	}
	t.Logf("ok: envelope submitted, lease=%s accepted", er.LeaseID)

	// ── Settle (assertions evaluate pi's real work) ──
	settleReq, err := json.Marshal(&agent.SettleRequest{EnvelopeJSON: envJSON, LeaseID: er.LeaseID})
	if err != nil {
		t.Fatalf("marshal settle request: %v", err)
	}
	resp, err = http.Post(agentURL+"/settle", "application/json", bytes.NewReader(settleReq))
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	var sr agent.SettleResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode settle response: %v", err)
	}
	resp.Body.Close()
	if sr.ReceiptID == "" {
		t.Fatalf("settle did not return a receipt (status %d)", resp.StatusCode)
	}
	t.Logf("ok: settled, receipt=%s", sr.ReceiptID)

	// ── The receipt is in the chain ──
	resp, err = http.Get(agentURL + "/chain?emitter=emitter-f2&executor=executor-f2")
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	var chain agent.ChainResponse
	if err := json.NewDecoder(resp.Body).Decode(&chain); err != nil {
		t.Fatalf("decode chain: %v", err)
	}
	resp.Body.Close()
	if len(chain.Receipts) != 1 {
		t.Fatalf("expected exactly 1 receipt in chain, got %d", len(chain.Receipts))
	}
	r := chain.Receipts[0]
	if r.ReceiptID != sr.ReceiptID {
		t.Fatalf("chain receipt %q != settled receipt %q", r.ReceiptID, sr.ReceiptID)
	}
	if r.Verdict != string(receipt.VerdictSettledClean) {
		t.Fatalf("verdict = %s, want SETTLED_CLEAN", r.Verdict)
	}
	if r.ContractID != env.EnvelopeID {
		t.Fatalf("contract_id = %q, want %q", r.ContractID, env.EnvelopeID)
	}
	t.Logf("ok: receipt %s verdict=%s contract=%s persisted in the chain — FULL A2A CYCLE COMPLETE", r.ReceiptID, r.Verdict, r.ContractID)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// f2Build builds one binary from the repo root (relative package paths need it).
func f2Build(t *testing.T, repo, dst, pkg string, extra ...string) {
	t.Helper()
	args := append([]string{"build"}, extra...)
	args = append(args, "-o", dst, pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", pkg, err, out)
	}
}

// f2SeedBareRepo creates a bare repo cache with a seeded main branch and
// returns its path.
func f2SeedBareRepo(t *testing.T, work string) string {
	t.Helper()
	repoCache := filepath.Join(work, "repo-cache")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", repoCache).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	seed := filepath.Join(work, "seed")
	if out, err := exec.Command("git", "clone", repoCache, seed).CombinedOutput(); err != nil {
		t.Fatalf("git clone (seed): %v: %s", err, out)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = seed
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("git", "-C", seed, "config", "user.email", "f2@test.local"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if _, err := run("git", "-C", seed, "config", "user.name", "F2 Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "BASE.md"), []byte("F2 seed\n"), 0o644); err != nil {
		t.Fatalf("write BASE.md: %v", err)
	}
	if _, err := run("git", "-C", seed, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := run("git", "-C", seed, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := run("git", "-C", seed, "push", "origin", "main"); err != nil {
		t.Fatalf("git push seed: %v", err)
	}
	return repoCache
}
