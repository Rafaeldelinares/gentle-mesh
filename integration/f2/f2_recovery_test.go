//go:build f2real

// F2-2: worker crash mid-execution — recovery semantics of the full stack.
//
// Escenario (roadmap Fase 2, tercera pata de la pregunta A2A: reparto,
// territorios, recuperación): un worker con el runner gentleshell REAL cae
// (kill -9) mientras pi está ejecutando el trabajo, antes de completar y
// commitear.
//
// Lo que este test demuestra con evidencia (no narrado):
//
//  1. Lease/territorio del worker caído: el coordinador marca la tarea en
//     vuelo como failed con NODE_LOST y evicta al worker del registro
//     (heartbeat timeout); el lease RFC-002 vence y el reaper R2 emite el
//     recibo SETTLEMENT_TIMEOUT — el contrato queda liquidado como expirado.
//  2. Re-dispatch: el coordinador NO re-despacha automáticamente (la tarea
//     muerta queda failed/NODE_LOST); el re-dispatch es del EMISOR y debe ser
//     un contrato NUEVO (nuevo EnvelopeID): el contract_id del intento A ya
//     tiene su recibo TIMEOUT — la idempotencia R1 impediría liquidar un
//     segundo intento del mismo contrato como CLEAN. El intento B completa
//     limpio: branch proposal/<taskB> distinta de la del intento muerto, sin
//     recibo fantasma.
//  3. Residuos del worker caído: procesos huérfanos (bwrap/pi del sandbox),
//     worktree a medias en el worktrees root y branch proposal/<taskA> sin
//     commit en el repo cache — observados y logueados como evidencia, con
//     limpieza responsable al final del test (los procesos del propio test).
package f2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/integration/agent"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/receipt"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

const (
	f2CrashEnvelopeA = "01923456-789a-7bc0-8123-f2rec-00001"
	f2CrashEnvelopeB = "01923456-789a-7bc0-8123-f2rec-00002"
	f2CrashPrompt    = "Create a file named hello-f2.txt in the repository root containing exactly one line: RFC-002-F2-OK. Do not create or modify anything else. When the file is created, commit the change with git (git add hello-f2.txt && git commit -m 'f2: add hello file')."
)

func TestF2_WorkerCrashRecovery(t *testing.T) {
	for _, dep := range []string{"gentle-shell", "bwrap", "pi", "git", "pgrep"} {
		if _, err := exec.LookPath(dep); err != nil {
			t.Skipf("dependency %q not found in PATH: %v", dep, err)
		}
	}

	repo := repoRoot(t)
	work := t.TempDir()
	t.Cleanup(f2KillAll)

	// ── Build + seed (same topology as F2-1) ──
	meshBin := filepath.Join(work, "gentle-mesh")
	harnessBin := filepath.Join(work, "test-harness")
	f2Build(t, repo, meshBin, "./cmd/gentle-mesh")
	f2Build(t, repo, harnessBin, "./cmd/test-harness", "-tags", "testharness")
	repoCache := f2SeedBareRepo(t, work)
	worktrees := filepath.Join(work, "worktrees")
	t.Log("ok: binaries built + bare repo cache seeded")

	// ── Coordinator ──
	coordPort := f2FreePort(t)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)
	f2Start(t, work, "coordinator", meshBin, "server",
		"-addr", fmt.Sprintf("127.0.0.1:%d", coordPort),
		"-tasks-dir", filepath.Join(work, "tasks"),
		"-db-path", filepath.Join(work, "mesh.db"),
		"-token", "A",
		"-heartbeat-timeout", "5s",
	)
	f2WaitFor(t, "coordinator up", 30*time.Second, func() bool {
		resp, err := http.Get(coordURL + "/healthz")
		return err == nil && resp.StatusCode == 200
	})

	// ── Worker A with the real gentleshell runner ──
	workerPort := f2FreePort(t)
	workerA := f2Start(t, work, "worker-w-f2", meshBin, "worker",
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
		"-tasks-dir", filepath.Join(work, "worker-tasks"),
	)
	f2WaitFor(t, "worker w-f2 joined the mesh", 60*time.Second, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		return strings.Contains(body, "w-f2")
	})

	// ── RFC-002 hardened agent (executor) with a fast reaper ──
	agentPort := f2FreePort(t)
	agentURL := fmt.Sprintf("http://127.0.0.1:%d", agentPort)
	f2Start(t, work, "agent", harnessBin, "serve",
		"--agent-id", "executor-f2",
		"--mesh-id", "mesh-f2",
		"--role", "executor",
		"--chain-db", filepath.Join(work, "chain.db"),
		"--workspace", work,
		"--port", fmt.Sprintf("%d", agentPort),
		"--lease-reaper-interval", "5s",
	)
	f2WaitFor(t, "agent executor up", 30*time.Second, func() bool {
		resp, err := http.Get(agentURL + "/health")
		return err == nil && resp.StatusCode == 200
	})

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

	// ── Contract A: submitted, lease issued (expires in 12s) ──
	envA := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-f2",
		EnvelopeID:      f2CrashEnvelopeA,
		EmitterAgentID:  "emitter-f2",
		ExecutorAgentID: "executor-f2",
		TimeoutSeconds:  12,
		MaxRemediations: 0,
		Territory: envelope.Territory{
			Repository:    "https://github.com/Rafaeldelinares/gentle-mesh",
			Branch:        "main",
			WorkspacePath: work,
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
	leaseA := f2SubmitContract(t, agentURL, emitterSigner, envA)
	t.Logf("ok: contract A submitted, lease=%s (expires in 12s)", leaseA)

	// ── Dispatch the real task to worker A ──
	taskA := f2DispatchTask(t, coordURL, f2CrashPrompt)
	t.Logf("ok: task A dispatched, task_id=%s (proposal/<taskID>)", taskA)

	// pi is now really working inside the sandbox; kill the worker mid-run.
	time.Sleep(10 * time.Second)
	if workerA.Process == nil {
		t.Fatal("worker A has no process to kill")
	}
	if err := workerA.Process.Kill(); err != nil {
		t.Fatalf("kill -9 worker A: %v", err)
	}
	_, _ = workerA.Process.Wait()
	t.Logf("ok: worker A (w-f2) killed mid-execution, pid=%d", workerA.Process.Pid)

	// ── (1) Coordinador: tarea en vuelo → NODE_LOST, worker evictado ──
	var taskAState string
	f2WaitForDiag(t, "task A failed with NODE_LOST after worker death", 90*time.Second, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, taskA), "")
		taskAState = body
		return strings.Contains(taskAState, `"failed"`) && strings.Contains(taskAState, "NODE_LOST")
	}, func() string {
		return "task A state:\n" + taskAState
	})
	t.Logf("task A final: failed with NODE_LOST (in-flight work liquidated by the coordinator)")
	// Registry semantics: PruneStaleNodes marks a node Offline after 3x
	// heartbeatTimeout — it does NOT delete it. An Offline node never receives
	// work (registry.go), which is the real "territory released".
	var nodesBody string
	f2WaitForDiag(t, "w-f2 marked offline by the registry (territory released)", 30*time.Second, func() bool {
		_, nodesBody = f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		var nodes struct {
			Nodes []struct {
				NodeID string `json:"node_id"`
				Status string `json:"status"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(nodesBody), &nodes); err != nil {
			return false
		}
		for _, n := range nodes.Nodes {
			if n.NodeID == "w-f2" {
				return n.Status == "offline"
			}
		}
		return true // fully evicted also counts as released
	}, func() string {
		return "nodes state:\n" + nodesBody
	})
	t.Logf("ok: w-f2 offline in the registry — its territory is no longer dispatchable")

	// ── (1) Lease RFC-002: reaper emits SETTLEMENT_TIMEOUT for contract A ──
	f2WaitForVerdictContract(t, agentURL, f2CrashEnvelopeA, receipt.VerdictTimeout, 90*time.Second)
	t.Logf("ok: reaper emitted SETTLEMENT_TIMEOUT for contract A (%s) — contract liquidated as expired", f2CrashEnvelopeA)

	// ── (3) Residuos del worker muerto: evidencia observada ──
	// Sandbox orphans ONLY: processes whose command line references the dead
	// attempt's worktree (the bwrap sandbox and whatever it wrapped). A naive
	// `pgrep -f $work` would also match the still-running coordinator/agent
	// (their flags embed $WORK paths) — killing them destroys the rest of the
	// experiment.
	orphanPattern := filepath.Join(worktrees, taskA)
	orphans := map[string]bool{}
	var frontier []string
	if out, err := exec.Command("pgrep", "-f", orphanPattern).Output(); err == nil {
		for _, pid := range strings.Fields(string(out)) {
			if !orphans[pid] {
				orphans[pid] = true
				frontier = append(frontier, pid)
			}
		}
	}
	// Walk the process tree under every matched pid (bwrap → node → pi): child
	// command lines do not always mention the worktree path.
	for round := 0; round < 3 && len(frontier) > 0; round++ {
		var next []string
		for _, pid := range frontier {
			out, err := exec.Command("pgrep", "-P", pid).Output()
			if err != nil {
				continue
			}
			for _, child := range strings.Fields(string(out)) {
				if !orphans[child] {
					orphans[child] = true
					next = append(next, child)
				}
			}
		}
		frontier = next
	}
	if len(orphans) > 0 {
		pids := make([]string, 0, len(orphans))
		for pid := range orphans {
			pids = append(pids, pid)
		}
		t.Logf("FINDING (orphan processes): %d processes of the dead sandbox survived the kill -9 of worker A: %s",
			len(orphans), strings.Join(pids, " "))
		// Responsible cleanup: they belong to THIS test's crashed worker —
		// otherwise pi keeps running inside a headless sandbox after the crash.
		for _, pid := range pids {
			_ = exec.Command("kill", "-9", pid).Run()
		}
		t.Log("ok: orphan processes killed responsibly (they belonged to this test's crashed worker)")
	} else {
		t.Log("FINDING (orphan processes): none — the kill -9 took down the whole process tree")
	}
	if entries, err := os.ReadDir(worktrees); err == nil {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) > 0 {
			t.Logf("FINDING (leftover worktrees after crash): %v — the next attempt must not collide with them", names)
		} else {
			t.Log("FINDING (leftover worktrees): none")
		}
	}
	if out, err := exec.Command("git", "-C", repoCache, "branch", "--list", "proposal/*").Output(); err == nil {
		t.Logf("FINDING (proposal branches in bare cache): %s", strings.TrimSpace(string(out)))
	}

	// ── (2) Re-dispatch: NEW worker + NEW contract (re-dispatch semantics) ──
	workerPortB := f2FreePort(t)
	f2Start(t, work, "worker-w-f2b", meshBin, "worker",
		"-coordinator", coordURL,
		"-token", "A",
		"-node-id", "w-f2b",
		"-endpoint", fmt.Sprintf("http://127.0.0.1:%d", workerPortB),
		"-addr", fmt.Sprintf("127.0.0.1:%d", workerPortB),
		"-heartbeat-interval", "1s",
		"-runner", "gentle-shell",
		"-gentle-shell-bin", "gentle-shell",
		"-bwrap-bin", "bwrap",
		"-gentle-shell-repo-cache", repoCache,
		"-gentle-shell-worktrees", worktrees,
		"-gentle-shell-base-ref", "main",
		"-gentle-shell-total-timeout", "8m",
		"-tasks-dir", filepath.Join(work, "worker-tasks"),
	)
	f2WaitFor(t, "worker w-f2b joined the mesh", 60*time.Second, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		return strings.Contains(body, "w-f2b")
	})

	// The assertions evaluate pi's REAL output: they must reference the stable
	// clone of the proposal branch by ABSOLUTE path — the agent server's
	// WorkspaceDir is `work`, not the clone (relative paths never find it).
	workspaceB := filepath.Join(work, "workspace-b")
	helloPathB := filepath.Join(workspaceB, "hello-f2.txt")
	envB := &envelope.CognitiveTaskEnvelope{
		ProtocolVersion: envelope.CurrentProtocolVersion,
		MeshID:          "mesh-f2",
		EnvelopeID:      f2CrashEnvelopeB,
		EmitterAgentID:  "emitter-f2",
		ExecutorAgentID: "executor-f2",
		TimeoutSeconds:  120,
		MaxRemediations: 0,
		Territory: envelope.Territory{
			Repository:    "https://github.com/Rafaeldelinares/gentle-mesh",
			Branch:        "main",
			WorkspacePath: work,
		},
		Assertions: []envelope.Assertion{{
			ID:   "hello-file-contains-f2-ok",
			Type: envelope.AssertionCommandExitCode,
			Params: envelope.AssertionParams{
				Command:          "grep -q RFC-002-F2-OK " + helloPathB,
				ExpectedExitCode: 0,
			},
		}},
		CreatedAt: time.Now().UTC(),
	}
	leaseB := f2SubmitContract(t, agentURL, emitterSigner, envB)
	t.Logf("ok: contract B (NEW contract for the re-dispatch) submitted, lease=%s", leaseB)

	taskB := f2DispatchTask(t, coordURL, f2CrashPrompt)
	t.Logf("ok: task B dispatched, task_id=%s", taskB)
	var taskBState string
	f2WaitForDiag(t, "task B completed by the recovered worker", 6*time.Minute, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, taskB), "")
		taskBState = body
		return strings.Contains(taskBState, `"completed"`)
	}, func() string {
		return fmt.Sprintf("task B state:\n%s\n--- worker B log tail ---\n%s", taskBState, f2Tail(filepath.Join(work, "worker-w-f2b.log")))
	})

	branchB := "proposal/" + taskB
	if out, err := exec.Command("git", "clone", "--branch", branchB, repoCache, workspaceB).CombinedOutput(); err != nil {
		t.Fatalf("clone proposal branch B %s: %v: %s", branchB, err, out)
	}
	helloB, err := os.ReadFile(filepath.Join(workspaceB, "hello-f2.txt"))
	if err != nil || !strings.Contains(string(helloB), "RFC-002-F2-OK") {
		t.Fatalf("task B did not produce the expected work (workspaceB=%s): %v", workspaceB, err)
	}
	t.Logf("ok: second attempt completed cleanly on its own branch (%s), no interference from the dead attempt", branchB)

	// Settle contract B (its lease is still alive).
	envBJSON, err := json.Marshal(envB)
	if err != nil {
		t.Fatalf("marshal envB: %v", err)
	}
	settleReq, err := json.Marshal(&agent.SettleRequest{EnvelopeJSON: envBJSON, LeaseID: leaseB})
	if err != nil {
		t.Fatalf("marshal settle: %v", err)
	}
	resp, err = http.Post(agentURL+"/settle", "application/json", bytes.NewReader(settleReq))
	if err != nil {
		t.Fatalf("settle B: %v", err)
	}
	var srB agent.SettleResponse
	if err := json.NewDecoder(resp.Body).Decode(&srB); err != nil {
		t.Fatalf("decode settle B response: %v", err)
	}
	resp.Body.Close()
	if srB.ReceiptID == "" {
		t.Fatalf("settle B did not return a receipt")
	}
	f2WaitForVerdictContract(t, agentURL, f2CrashEnvelopeB, receipt.VerdictSettledClean, 30*time.Second)
	t.Logf("ok: contract B settled SETTLED_CLEAN, receipt=%s", srB.ReceiptID)

	// ── Final chain invariants: exactly [TIMEOUT(A), CLEAN(B)] ──
	_, chainBody := f2CoordHTTP(t, http.MethodGet, agentURL+"/chain?emitter=emitter-f2&executor=executor-f2", "")
	var chain agent.ChainResponse
	if err := json.Unmarshal([]byte(chainBody), &chain); err != nil {
		t.Fatalf("decode chain: %v: %s", err, chainBody)
	}
	if len(chain.Receipts) != 2 {
		t.Fatalf("expected exactly 2 receipts in chain (TIMEOUT for A + CLEAN for B), got %d: %s", len(chain.Receipts), chainBody)
	}
	verdicts := map[string]string{}
	for _, r := range chain.Receipts {
		verdicts[r.ContractID] = r.Verdict
	}
	if v, ok := verdicts[f2CrashEnvelopeA]; !ok || v != string(receipt.VerdictTimeout) {
		t.Fatalf("contract A must be liquidated as SETTLEMENT_TIMEOUT, got %q (present=%v)", v, ok)
	}
	if v, ok := verdicts[f2CrashEnvelopeB]; !ok || v != string(receipt.VerdictSettledClean) {
		t.Fatalf("contract B must be liquidated as SETTLED_CLEAN, got %q (present=%v)", v, ok)
	}
	t.Logf("ok: final chain = [SETTLEMENT_TIMEOUT(%s), SETTLED_CLEAN(%s)] — no ghost receipts, no competing branches liquidated twice",
		f2CrashEnvelopeA, f2CrashEnvelopeB)
}

// ─────────────────────────────────────────────────────────────────
// F2-2 helpers
// ─────────────────────────────────────────────────────────────────

// f2SubmitContract signs + submits a contract and returns the lease ID.
func f2SubmitContract(t *testing.T, agentURL string, signer signing.Signer, env *envelope.CognitiveTaskEnvelope) string {
	t.Helper()
	if err := envelope.SignEnvelope(env, signer); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	reqBody, err := json.Marshal(&agent.EnvelopeRequest{EnvelopeJSON: envJSON}) // []byte → base64, the agent's wire format
	if err != nil {
		t.Fatalf("marshal submit: %v", err)
	}
	resp, err := http.Post(agentURL+"/envelopes", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("submit envelope: %v", err)
	}
	defer resp.Body.Close()
	var er agent.EnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatalf("decode envelope response: %v", err)
	}
	if !er.Accepted || er.LeaseID == "" {
		t.Fatalf("lease not accepted: %+v", er)
	}
	return er.LeaseID
}

func mustJSONString(t *testing.T, b []byte) string {
	t.Helper()
	out, err := json.Marshal(string(b))
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(out)
}

func f2DispatchTask(t *testing.T, coordURL, prompt string) string {
	t.Helper()
	body := fmt.Sprintf(`{"agent":"worker","task":%s,"timeout_seconds":300}`, mustJSONString(t, []byte(prompt)))
	status, respBody := f2CoordHTTP(t, http.MethodPost, coordURL+"/v1/tasks", body)
	if status != 200 && status != 201 {
		t.Fatalf("dispatch task status = %d: %s", status, respBody)
	}
	var tr struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(strings.NewReader(respBody)).Decode(&tr); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if tr.TaskID == "" {
		t.Fatalf("empty task_id: %s", respBody)
	}
	return tr.TaskID
}

func f2WaitForVerdictContract(t *testing.T, agentURL, contractID string, verdict receipt.Verdict, deadline time.Duration) {
	t.Helper()
	deadlineTime := time.Now().Add(deadline)
	for {
		_, body := f2CoordHTTP(t, http.MethodGet, agentURL+"/chain?emitter=emitter-f2&executor=executor-f2", "")
		var chain agent.ChainResponse
		if err := json.Unmarshal([]byte(body), &chain); err != nil {
			t.Fatalf("decode chain: %v: %s", err, body)
		}
		for _, r := range chain.Receipts {
			if r.ContractID == contractID && r.Verdict == string(verdict) {
				return
			}
		}
		if time.Now().After(deadlineTime) {
			t.Fatalf("no receipt with verdict %s for contract %s within %v; chain: %s", verdict, contractID, deadline, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
