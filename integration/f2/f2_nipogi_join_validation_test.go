//go:build f2real

// F2-5: join the NiPoGi production worker to the mesh + validation batch
// over REAL network (LAN) with REAL mTLS (PKI regenerated with the NiPoGi's
// IP in the server SAN).
//
// Topology:
//
//	NiPoGi (192.168.1.26)                    Laptop (emitter)
//	  coordinator :8630  ◀── join/heartbeat ──┐
//	  worker gentle-shell                      │ (LAN real network)
//	  agent RFC-002 :18443 ◀── mTLS ───────────┘
//	  (bwrap + pi real)
//
// Every phase emits RAW output (join log, mesh state, receipt hashes, usage
// numbers) — not summaries. Same trivial tasks as F2-1/F2-2/F2-3.
//
// Retry semantics (R2, demonstrated in F2-2): a failed attempt
// (NO_COMMIT_PRODUCED) is re-dispatched by the emitter with a NEW contract
// (new EnvelopeID) — the failed contract is liquidated as SETTLEMENT_FAILED
// by its own settle. Up to 2 retries per task.
package f2

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/integration/agent"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/signing"
)

func TestF2_NiPoGiJoinAndValidation(t *testing.T) {
	nipoHost := os.Getenv("NIPO_HOST")
	if nipoHost == "" {
		nipoHost = "192.168.1.26"
	}
	nipoPass := os.Getenv("NIPO_PASS")
	if nipoPass == "" {
		t.Skip("NIPO_PASS not set — remote NiPoGi test skipped")
	}

	sshRun := func(cmd string, timeout time.Duration) (string, error) {
		cmdExec := exec.Command("sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", "rafael@"+nipoHost, cmd)
		cmdExec.Env = append(os.Environ(), "SSHPASS="+nipoPass)
		out, err := cmdExec.CombinedOutput()
		_ = timeout
		return string(out), err
	}

	// ── 0. Connectivity ──
	pingOut, err := sshRun("echo NIPOGI-SSH-OK", 30*time.Second)
	if err != nil || !strings.Contains(pingOut, "NIPOGI-SSH-OK") {
		t.Skipf("NiPoGi not reachable via SSH: %v: %s", err, pingOut)
	}
	t.Logf("RAW [connectivity]: %s", strings.TrimSpace(pingOut))

	// ── 1. Coordinator + worker + agent server (all remote, real processes) ──
	_, err = sshRun("pkill -9 -x gentle-mesh 2>/dev/null || true; pkill -9 -x test-harness 2>/dev/null || true; sleep 1; echo cleaned", 30*time.Second)
	_ = err
	_, _ = sshRun("mkdir -p /home/rafael/f2-workspace; rm -f ~/f2-chain.db; rm -f ~/f2-mesh.db; rm -f /tmp/f2-*.log; rm -rf /tmp/f2-worktrees/*; for ref in $(git -C /home/rafael/f2-repo-cache for-each-ref --format='%(refname)' refs/heads/proposal/* 2>/dev/null); do git -C /home/rafael/f2-repo-cache update-ref -d \"$ref\"; done; git -C /home/rafael/f2-repo-cache update-ref refs/heads/main b44ef63", 30*time.Second)

	startRemote := func(name, cmd string) {
		sshCmd := fmt.Sprintf("nohup %s > /tmp/f2-%s.log 2>&1 & echo '%s pid='$!", cmd, name, name)
		out, err := sshRun(sshCmd, 30*time.Second)
		if err != nil {
			t.Fatalf("start %s: %v: %s", name, err, out)
		}
		t.Logf("RAW [start %s]: %s", name, strings.TrimSpace(out))
	}

	startRemote("coordinator", "/tmp/gentle-mesh server -addr 0.0.0.0:8630 -tasks-dir ~/f2-tasks -db-path ~/f2-mesh.db -token A -heartbeat-timeout 5s")
	startRemote("worker", "env -u PI_MODEL -u PI_PROVIDER PATH=\"/home/rafael/.local/bin:$PATH\" PI_CODING_AGENT_DIR=/home/rafael/.gentle-shell/agent PI_SKIP_VERSION_CHECK=1 PI_REASONING_LEVEL=high GENTLE_SHELL_NO_AUTO_SETUP=1 /tmp/gentle-mesh worker -coordinator http://127.0.0.1:8630 -token A -node-id w-nipogi -endpoint http://127.0.0.1:8631 -addr 127.0.0.1:8631 -heartbeat-interval 1s -runner gentle-shell -gentle-shell-bin gentle-shell -bwrap-bin bwrap -gentle-shell-repo-cache ~/f2-repo-cache -gentle-shell-worktrees /tmp/f2-worktrees -gentle-shell-base-ref main -concurrency 1 -tasks-dir ~/f2-worker-tasks")
	startRemote("agent", "/tmp/test-harness serve --agent-id nipogi-prod --mesh-id mesh-nipogi --role executor --chain-db ~/f2-chain.db --workspace ~/f2-workspace --port 18443 --tls-cert ~/f2-certs-v2/server.crt --tls-key ~/f2-certs-v2/server.key --client-ca ~/f2-certs-v2/ca.crt")

	// ── 2. Handshake evidence: worker joined + mesh state listing w-nipogi active ──
	time.Sleep(4 * time.Second)
	meshState, err := f2RemoteHTTP(t, nipoHost, "http://192.168.1.26:8630/v1/mesh/nodes", "A")
	if err != nil {
		t.Fatalf("mesh state query: %v", err)
	}
	if !strings.Contains(meshState, "w-nipogi") {
		t.Fatalf("RAW [mesh state]: worker w-nipogi MISSING from mesh state: %s", meshState)
	}
	if !strings.Contains(meshState, `"status":"online"`) {
		t.Fatalf("RAW [mesh state]: w-nipogi not online: %s", meshState)
	}
	t.Logf("RAW [mesh state — worker active in the mesh]: %s", meshState)

	// Worker join log (raw handshake evidence).
	joinLog, _ := sshRun("grep -iE 'join|heartbeat' /tmp/f2-worker.log | tail -3", 30*time.Second)
	t.Logf("RAW [worker join/heartbeat log]: %s", strings.TrimSpace(joinLog))

	// ── 3. mTLS client (emitter on the laptop) — certs pulled from the NiPoGi ──
	clientCertDir := t.TempDir()
	for _, f := range []string{"emitter.crt", "emitter.key", "ca.crt"} {
		certOut, certErr := sshRun("cat ~/f2-certs-v2/"+f, 30*time.Second)
		if certErr != nil {
			t.Fatalf("pull %s: %v", f, certErr)
		}
		if err := os.WriteFile(filepath.Join(clientCertDir, f), []byte(certOut), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	clientTLSCert, err := tls.LoadX509KeyPair(filepath.Join(clientCertDir, "emitter.crt"), filepath.Join(clientCertDir, "emitter.key"))
	if err != nil {
		t.Fatalf("load client cert: %v", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(clientCertDir, "ca.crt"))
	if err != nil {
		t.Fatalf("read ca.crt: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA certs failed")
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		RootCAs:      caPool,
	}
	agentURL := "https://" + nipoHost + ":18443"
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   30 * time.Second,
	}
	t.Logf("ok: mTLS client ready against %s (server SAN includes the NiPoGi IP)", agentURL)

	// ── 4. Register emitter key (S2, over mTLS) ──
	emitterSigner, err := signing.GenerateSigner("emitter-laptop-f4")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}
	regBody, _ := json.Marshal(map[string]string{
		"agent_id":       "emitter-laptop-f4",
		"public_key_hex": emitterSigner.PublicKeyHex(),
	})
	resp, err := client.Post(agentURL+"/register-key", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatalf("register-key: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("register-key returned %d: %s", resp.StatusCode, body)
	}
	resp.Body.Close()
	t.Log("ok: emitter key registered over mTLS (S2)")

	// ── 5. Validation batch: 3 contracts, the FULL A2A cycle each, over the real network ──
	var totalTokens int64
	var totalCost float64
	for contractNum := 1; contractNum <= 3; contractNum++ {
		var lastReceiptID string
		clean := false
		for attempt := 1; attempt <= 3 && !clean; attempt++ {
			env := &envelope.CognitiveTaskEnvelope{
				ProtocolVersion: envelope.CurrentProtocolVersion,
				MeshID:          "mesh-nipogi",
				EnvelopeID:      fmt.Sprintf("01923456-789a-7bc0-8123-f2nipogi-%03d-r%d", contractNum, attempt),
				EmitterAgentID:  "emitter-laptop-f4",
				ExecutorAgentID: "nipogi-prod",
				TimeoutSeconds:  300,
				MaxRemediations: 0,
				Territory: envelope.Territory{
					Repository:    "https://github.com/Rafaeldelinares/gentle-mesh",
					Branch:        "main",
					WorkspacePath: "/home/rafael/f2-workspace",
				},
				Assertions: []envelope.Assertion{{
					ID:   fmt.Sprintf("pi-commit-landed-%d-r%d", contractNum, attempt),
					Type: envelope.AssertionCommandExitCode,
					Params: envelope.AssertionParams{
						Command:          fmt.Sprintf("sh -c \"git -C /home/rafael/f2-repo-cache log --all --format=%%s | grep -q 'f4: task %d'\"", contractNum),
						ExpectedExitCode: 0,
					},
				}},
				CreatedAt: time.Now().UTC(),
			}
			if err := envelope.SignEnvelope(env, emitterSigner); err != nil {
				t.Fatalf("SignEnvelope contract %d attempt %d: %v", contractNum, attempt, err)
			}
			envJSON, _ := json.Marshal(env)
			reqBody, _ := json.Marshal(map[string]any{"envelope_json": envJSON}) // []byte marshals as base64 — the agent wire format

			// Submit over mTLS (real network).
			resp, err := client.Post(agentURL+"/envelopes", "application/json", bytes.NewReader(reqBody))
			if err != nil {
				t.Fatalf("submit contract %d attempt %d: %v", contractNum, attempt, err)
			}
			var er agent.EnvelopeResponse
			json.NewDecoder(resp.Body).Decode(&er)
			resp.Body.Close()
			if !er.Accepted || er.LeaseID == "" {
				t.Fatalf("contract %d attempt %d: lease not accepted: %+v", contractNum, attempt, er)
			}
			t.Logf("RAW [contract %d attempt %d submit]: accepted=true lease=%s", contractNum, attempt, er.LeaseID)

			// Dispatch the REAL task: pi performs the work inside the sandbox.
			prompt := fmt.Sprintf("Create a file named hello-nipogi-%d.txt containing exactly one line: NIPOGI-%d-OK. Do not create or modify anything else. Commit the change with git (git add hello-nipogi-%d.txt && git commit -m 'f4: task %d').", contractNum, contractNum, contractNum, contractNum)
			taskID := f2DispatchTask(t, "http://"+nipoHost+":8630", prompt)
			f2WaitForDiag(t, fmt.Sprintf("task %d attempt %d finished (completed or failed)", contractNum, attempt), 6*time.Minute, func() bool {
				body, _ := f2RemoteHTTP(t, nipoHost, fmt.Sprintf("http://%s:8630/v1/tasks/%s", nipoHost, taskID), "A")
				return strings.Contains(body, `"completed"`) || strings.Contains(body, `"failed"`)
			}, func() string {
				return fmt.Sprintf("task %s state — worker log tail:\n%s", taskID, f2Tail("/tmp/f2-worker.log"))
			})

			// Usage from the task state (the coordinator carries the completion
			// payload with the runner's "Usage:" line) — only for completed runs.
			taskBody, _ := f2RemoteHTTP(t, nipoHost, fmt.Sprintf("http://%s:8630/v1/tasks/%s", nipoHost, taskID), "A")
			var taskState struct {
				Status string `json:"status"`
				Error  *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error,omitempty"`
				Completion *struct {
					Result string `json:"result"`
				} `json:"completion,omitempty"`
			}
			if err := json.Unmarshal([]byte(taskBody), &taskState); err != nil {
				t.Fatalf("decode task state %s: %v", taskID, err)
			}

			switch {
			case taskState.Status == "completed" && taskState.Completion != nil:
				usage, ok := f2ParseUsageLine(taskState.Completion.Result)
				if !ok {
					t.Fatalf("contract %d attempt %d: completion lacks a parseable usage line: %q", contractNum, attempt, taskState.Completion.Result)
				}
				totalTokens += usage.TotalTokens
				totalCost += usage.Cost

				// ── Settle over mTLS (RFC-002: assertion evaluates pi's real commit in repo cache) ──
				settleReq, err := json.Marshal(&agent.SettleRequest{EnvelopeJSON: envJSON, LeaseID: er.LeaseID})
				if err != nil {
					t.Fatalf("contract %d attempt %d marshal settle request: %v", contractNum, attempt, err)
				}
				sResp, err := client.Post(agentURL+"/settle", "application/json", bytes.NewReader(settleReq))
				if err != nil {
					t.Fatalf("contract %d attempt %d settle: %v", contractNum, attempt, err)
				}
				var sr agent.SettleResponse
				if err := json.NewDecoder(sResp.Body).Decode(&sr); err != nil {
					t.Fatalf("contract %d attempt %d decode settle: %v", contractNum, attempt, err)
				}
				sResp.Body.Close()
				if sr.ReceiptID == "" {
					t.Fatalf("contract %d attempt %d settle did not return a receipt (HTTP %d)", contractNum, attempt, sResp.StatusCode)
				}

				receiptHash := fmt.Sprintf("%x", sha256.Sum256(sr.ReceiptJSON))[:16]

				// Query chain to get receipt details (verdict).
				chainResp, err := client.Get(agentURL + "/chain?emitter=emitter-laptop-f4&executor=nipogi-prod")
				if err != nil {
					t.Fatalf("contract %d attempt %d get chain: %v", contractNum, attempt, err)
				}
				var chain agent.ChainResponse
				json.NewDecoder(chainResp.Body).Decode(&chain)
				chainResp.Body.Close()

				var receiptVerdict string
				for _, r := range chain.Receipts {
					if r.ReceiptID == sr.ReceiptID {
						receiptVerdict = r.Verdict
						break
					}
				}

				t.Logf("RAW [contract %d settled]: receipt_id=%s receipt_hash=%s verdict=%s usage: input=%d output=%d cacheRead=%d totalTokens=%d cost=%.5f",
					contractNum, sr.ReceiptID, receiptHash, receiptVerdict,
					usage.Input, usage.Output, usage.CacheRead, usage.TotalTokens, usage.Cost)

				if receiptVerdict != "SETTLED_CLEAN" {
					t.Fatalf("contract %d attempt %d: expected verdict SETTLED_CLEAN, got %s", contractNum, attempt, receiptVerdict)
				}

				lastReceiptID = sr.ReceiptID
				clean = true
				break // attempt completed and settled cleanly, move to next contract
			case taskState.Status == "failed" && taskState.Error != nil && taskState.Error.Code == "NO_COMMIT_PRODUCED":
				t.Logf("FINDING [contract %d attempt %d]: NO_COMMIT_PRODUCED — re-dispatching with a NEW contract (R2 semantics)", contractNum, attempt)
				continue // next attempt: new contract
			default:
				t.Fatalf("contract %d attempt %d: unexpected status %q: %s", contractNum, attempt, taskState.Status, taskBody)
			}
		}
		if !clean || lastReceiptID == "" {
			t.Fatalf("contract %d: did not reach SETTLED_CLEAN after all attempts", contractNum)
		}
	}

	t.Logf("RAW [batch total]: 3 contracts over REAL network (LAN), totalTokens=%d cost=%.5f", totalTokens, totalCost)
}

// f2RemoteHTTP queries the coordinator mesh state from the laptop over the
// real network (LAN) with the bearer token.
func f2RemoteHTTP(t *testing.T, _, url, token string) (string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.String(), nil
}
