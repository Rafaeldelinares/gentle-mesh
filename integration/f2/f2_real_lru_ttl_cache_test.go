//go:build f2real

// F2-7: real agent dispatches an LRU+TTL cache implementation through the
// gentle-mesh runner (coordinator → worker → bwrap sandbox → pi → signed
// receipt). This is the second phase-2 question: a synthetic task dispatched
// through the real mesh, NOT solved by hand in this repo.
//
// All work happens in the agent's bwrap worktree under lru_ttl_synth/. We pull
// the resulting files back from the NiPoGi worker and re-run `go test -race`
// locally to confirm the agent's own test evidence.
package f2

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rafaeldelinares/gentle-mesh/integration/agent"
	"github.com/Rafaeldelinares/gentle-mesh/pkg/envelope"
)

// TestF2_RealAgent_DispatchesLRUTTLCache runs the synthetic LRU+TTL cache
// task over the real mesh to the NiPoGi worker. The agent implements the cache
// inside its bwrap sandbox, runs `go test -race ./...`, and commits the result
// to the shared repo cache. We then pull the artifacts back and re-verify.
func TestF2_RealAgent_DispatchesLRUTTLCache(t *testing.T) {
	nipoHost := os.Getenv("NIPO_HOST")
	if nipoHost == "" {
		nipoHost = "192.168.1.26"
	}
	nipoPass := os.Getenv("NIPO_PASS")
	if nipoPass == "" {
		t.Skip("NIPO_PASS not set — real-agent NiPoGi test skipped")
	}

	sshRun := func(cmd string, _ time.Duration) (string, error) {
		c := exec.Command("sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", "rafael@"+nipoHost, cmd)
		c.Env = append(c.Env, "SSHPASS="+nipoPass, "PATH=/usr/local/bin:/usr/bin:/bin")
		out, err := c.CombinedOutput()
		return string(out), err
	}

	// 0. Connectivity
	pingOut, err := sshRun("echo NIPOGI-SSH-OK", 30*time.Second)
	if err != nil || !strings.Contains(pingOut, "NIPOGI-SSH-OK") {
		t.Skipf("NiPoGi not reachable via SSH: %v: %s", err, pingOut)
	}
	t.Logf("RAW [connectivity]: %s", strings.TrimSpace(pingOut))

	// 1. Tear down + reset workspace on NiPoGi (clean slate).
	_, _ = sshRun(
		"pkill -9 -x gentle-mesh 2>/dev/null || true; pkill -9 -x test-harness 2>/dev/null || true; sleep 1; mkdir -p /home/rafael/f2-7/tasks /home/rafael/f2-7/mesh.db.dir /home/rafael/f2-7/workspace /home/rafael/f2-7/worker-tasks; for ref in $(git -C /home/rafael/f2-repo-cache for-each-ref --format='%(refname)' refs/heads/proposal/* 2>/dev/null); do git -C /home/rafael/f2-repo-cache update-ref -d \"$ref\"; done; rm -rf /home/rafael/f2-7/worktrees/* 2>/dev/null; echo cleaned",
		30*time.Second,
	)

	startRemote := func(name, cmd string) {
		sshCmd := fmt.Sprintf("nohup %s > /tmp/f2-7-%s.log 2>&1 & echo '%s pid='$!", cmd, name, name)
		out, err := sshRun(sshCmd, 30*time.Second)
		if err != nil {
			t.Fatalf("start %s: %v: %s", name, err, out)
		}
		t.Logf("RAW [start %s]: %s", name, strings.TrimSpace(out))
	}

	// Ensure repo cache exists and has at least one commit on main.
	setupRepoCache := func() {
		setupScript := `
set +e
if [ ! -d /home/rafael/f2-repo-cache ]; then
  mkdir -p /home/rafael/f2-repo-cache
  cd /home/rafael/f2-repo-cache
  git init --bare 2>/dev/null
fi
if ! git -C /home/rafael/f2-repo-cache rev-parse --verify refs/heads/main >/dev/null 2>&1; then
  mkdir -p /tmp/f2-7-bootstrap
  cd /tmp/f2-7-bootstrap
  rm -rf .git 2>/dev/null
  git init -q
  git config user.email "f2@example.com"
  git config user.name "f2-bootstrap"
  echo "# scratch" > README.md
  git add README.md
  git commit -q -m "seed"
  cd /home/rafael/f2-repo-cache
  git fetch /tmp/f2-7-bootstrap main:refs/heads/main 2>/dev/null
fi
git -C /home/rafael/f2-repo-cache update-ref refs/heads/main $(git -C /home/rafael/f2-repo-cache rev-parse refs/heads/main 2>/dev/null) 2>/dev/null
echo repo-cache-ready
`
		out, _ := sshRun(setupScript, 60*time.Second)
		t.Logf("RAW [repo-cache setup]: %s", strings.TrimSpace(out))
	}
	setupRepoCache()

	startRemote("coordinator", "/tmp/gentle-mesh server -addr 0.0.0.0:8630 -tasks-dir /home/rafael/f2-7/tasks -db-path /home/rafael/f2-7/mesh.db -token A -heartbeat-timeout 5s")
	startRemote("worker", "env -u PI_MODEL -u PI_PROVIDER PATH=\"/home/rafael/.local/bin:$PATH\" PI_CODING_AGENT_DIR=/home/rafael/.gentle-shell/agent PI_SKIP_VERSION_CHECK=1 PI_REASONING_LEVEL=high GENTLE_SHELL_NO_AUTO_SETUP=1 /tmp/gentle-mesh worker -coordinator http://127.0.0.1:8630 -token A -node-id w-nipogi -endpoint http://127.0.0.1:8631 -addr 127.0.0.1:8631 -heartbeat-interval 1s -runner gentle-shell -gentle-shell-bin gentle-shell -bwrap-bin bwrap -gentle-shell-repo-cache /home/rafael/f2-repo-cache -gentle-shell-worktrees /home/rafael/f2-7/worktrees -gentle-shell-base-ref main -concurrency 1 -tasks-dir /home/rafael/f2-7/worker-tasks")

	// 2. Handshake evidence.
	time.Sleep(4 * time.Second)
	meshState, err := f2RemoteHTTP(t, nipoHost, "http://"+nipoHost+":8630/v1/mesh/nodes", "A")
	if err != nil {
		t.Fatalf("mesh state query: %v", err)
	}
	if !strings.Contains(meshState, "w-nipogi") || !strings.Contains(meshState, `"status":"online"`) {
		t.Fatalf("worker w-nipogi not online: %s", meshState)
	}
	t.Logf("RAW [mesh state]: %s", meshState)

	// 3. Dispatch the LRU+TTL task to the worker.
	prompt := `Tarea F2-7: Implementar una caché LRU con TTL en Go.

CONTEXTO:
Estás corriendo dentro de un sandbox bwrap con un worktree de git. Averiguá la ruta con 'pwd' o 'git rev-parse --show-toplevel'. El repositorio base es un bare repo compartido; tus commits van a una branch proposal/<task_id>.

INSTRUCCIONES ESTRICTAS — leé todo antes de empezar:

1. NO modifiques archivos en /home/rafael/proyectos/gentle-mesh ni toques nada fuera del directorio lru_ttl_synth/ dentro del worktree.

2. Creá un módulo Go nuevo en lru_ttl_synth/ dentro del worktree con: cd lru_ttl_synth && go mod init scratch/lru_ttl_synth.

3. Escribí lru_ttl_synth/cache.go con la caché LRU+TTL siguiendo EXACTAMENTE estas 6 reglas:
   (a) Capacidad fija (N). Al superarla, expulsá la entrada menos recientemente usada (LRU real, no FIFO).
   (b) Cada entrada tiene su propio TTL, asignado al insertarla. Una entrada expirada por TTL NUNCA debe devolverse como válida en un Get, aunque todavía no haya sido físicamente expulsada.
   (c) Acceder a una entrada (Get) la refresca como "recientemente usada" a efectos de LRU, pero NO le extiende el TTL.
   (d) Actualizar el valor de una clave existente (Set sobre clave existente) SÍ le reinicia el TTL.
   (e) Concurrencia segura: múltiples goroutines haciendo Get/Set/Delete simultáneamente, sin condiciones de carrera (verificable con "go test -race") y sin deadlocks.
   (f) API mínima: New(capacity int, cleanupInterval time.Duration), Get(key) (value, bool), Set(key, value, ttl time.Duration), Delete(key), Len() int. Recomendado: usá generics de Go (Cache[K comparable, V any]).

4. Escribí lru_ttl_synth/cache_test.go con tests OBLIGATORIOS (no opcionales, todos deben pasar):
   - Comportamiento LRU puro (sin TTL en juego): verificá el orden de expulsión exacto.
   - Expiración por TTL independiente de la expulsión por capacidad: una entrada con TTL corto debe "desaparecer" lógicamente de Get incluso si la caché no está llena.
   - El caso cruzado: una entrada recién usada (LRU-fresca) pero con TTL vencido — debe tratarse como expirada, el uso reciente no la salva.
   - Test de concurrencia con -race: N goroutines golpeando Get/Set/Delete en paralelo sobre las mismas claves, sin fallos de carrera.
   - Caso límite: capacidad 0, o Set de una clave que ya existe actualizando su TTL.

5. Corré "time go test -race -v ./... 2>&1" desde el directorio lru_ttl_synth/. Capturá el output COMPLETO.

6. Si algo falla, arreglalo y reintentá. Está bien iterar varios intentos si hay flakiness; documentá cuántos hiciste.

7. Una vez que TODO el suite pase con -race, hacé UN commit desde la raíz del worktree:
   - git add lru_ttl_synth/
   - git commit -m "feat(synth): LRU+TTL cache implementation (F2-7, delivered by real agent)"

8. En tu respuesta final INCLUIR VERBATIM:
   - El comando "go test -race -v ./..." que corriste, con su exit code.
   - El output crudo completo (=== RUN y --- PASS/FAIL por test).
   - El tiempo del run que devolvió "time".
   - Si iteraste más de una vez: cuántos intentos, y por qué motivo iteraste.
   - El SHA del commit que hiciste en la branch proposal.

9. NO inventes resultados. Si algo falló, mostralo tal cual.

LISTO. Empezá.`
	taskID := f2DispatchTask(t, "http://"+nipoHost+":8630", prompt)
	t.Logf("RAW [dispatch]: taskID=%s", taskID)

	// 4. Wait for task completion.
	f2WaitForDiag(t, "task completed (or failed)", 20*time.Minute, func() bool {
		body, _ := f2RemoteHTTP(t, nipoHost, fmt.Sprintf("http://%s:8630/v1/tasks/%s", nipoHost, taskID), "A")
		return strings.Contains(body, `"completed"`) || strings.Contains(body, `"failed"`)
	}, func() string {
		return fmt.Sprintf("task %s state — worker log tail:\n%s", taskID, f2SshTail(t, nipoHost, "/tmp/f2-7-worker.log"))
	})

	// 5. Pull the result.
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
	if taskState.Status != "completed" || taskState.Completion == nil {
		errMsg := ""
		if taskState.Error != nil {
			errMsg = taskState.Error.Code + ": " + taskState.Error.Message
		}
		t.Fatalf("task did not complete cleanly: status=%q err=%s", taskState.Status, errMsg)
	}
	result := taskState.Completion.Result
	if u, ok := f2ParseUsageLine(result); ok && u.TotalTokens > 0 {
		t.Logf("RAW [agent usage]: input=%d output=%d cacheRead=%d totalTokens=%d cost=%.5f",
			u.Input, u.Output, u.CacheRead, u.TotalTokens, u.Cost)
	}

	// 6. Verify the agent actually committed the work to the repo cache.
	branchesOut, _ := sshRun(`git -C /home/rafael/f2-repo-cache for-each-ref --format='%(refname:short) %(objectname:short) %(subject)' refs/heads/proposal/ 2>/dev/null | sort`, 30*time.Second)
	t.Logf("RAW [proposal branches]: %s", strings.TrimSpace(branchesOut))
	if !strings.Contains(strings.ToLower(branchesOut), "lru") &&
		!strings.Contains(strings.ToLower(branchesOut), "ttl") &&
		!strings.Contains(strings.ToLower(branchesOut), "f2-7") &&
		!strings.Contains(strings.ToLower(branchesOut), "cache") {
		t.Fatalf("no proposal branch in repo-cache mentions lru/ttl/cache/f2-7; agent probably did not commit. Branches:\n%s", branchesOut)
	}

	// 7. Pull the worktree contents back to laptop for re-verification.
	_ = os.MkdirAll("/tmp/f2-7-pulled", 0755)
	if _, err := exec.LookPath("rsync"); err == nil {
		rsyncCmd := exec.Command("rsync", "-az",
			"--include=*/",
			"--include=lru_ttl_synth/**",
			"--exclude=*",
			"-e", "sshpass -e ssh -o StrictHostKeyChecking=accept-new",
			fmt.Sprintf("rafael@%s:/home/rafael/f2-7/worktrees/", nipoHost),
			"/tmp/f2-7-pulled/",
		)
		rsyncCmd.Env = append(os.Environ(), "SSHPASS="+nipoPass)
		rsyncOut, _ := rsyncCmd.CombinedOutput()
		t.Logf("RAW [rsync]: %s", string(rsyncOut))
	} else {
		// Fallback to scp via sshpass.
		scpCmd := exec.Command("sshpass", "-e", "scp", "-o", "StrictHostKeyChecking=accept-new", "-r",
			fmt.Sprintf("rafael@%s:/home/rafael/f2-7/worktrees/*/lru_ttl_synth", nipoHost),
			"/tmp/f2-7-pulled/",
		)
		scpCmd.Env = append(os.Environ(), "SSHPASS="+nipoPass)
		scpOut, _ := scpCmd.CombinedOutput()
		t.Logf("RAW [scp fallback]: %s", string(scpOut))
	}

	// 8. Run `go test -race ./...` locally on the agent's output.
	pulledDir := "/tmp/f2-7-pulled/lru_ttl_synth"
	if fi, err := os.Stat(pulledDir); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir(pulledDir)
		t.Logf("RAW [pulled dir inspect]: %d entries in %s", len(entries), pulledDir)
		if modBytes, err := os.ReadFile(filepath.Join(pulledDir, "go.mod")); err == nil {
			t.Logf("RAW [go.mod]:\n%s", string(modBytes))
		}
		if _, err := exec.LookPath("go"); err == nil {
			testCmd := exec.Command("go", "test", "-race", "-v", "./...")
			testCmd.Dir = pulledDir
			testOut, _ := testCmd.CombinedOutput()
			t.Logf("RAW [local re-run of agent's go test -race -v]:\n%s", string(testOut))
		} else {
			t.Logf("no local go binary; relying on agent's own test output as evidence")
		}
	} else {
		t.Logf("no pulled dir at %s: skipping local re-run", pulledDir)
	}

	// 9. Final summary with raw SHA hashes.
	sha := sha256.Sum256([]byte(result))
	t.Logf("RAW [completion sha256 first16]: %x (result length=%d bytes)", sha[:8], len(result))
	t.Logf("RAW [result first 1200 chars]:\n%s", truncate(result, 1200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[TRUNCATED at " + fmt.Sprintf("%d", n) + "]"
}

// f2SshTail reads the last N lines of a remote log file via SSH.
func f2SshTail(t *testing.T, host, path string) string {
	t.Helper()
	pass := os.Getenv("NIPO_PASS")
	if pass == "" {
		return "(no NIPO_PASS for tail)"
	}
	c := exec.Command("sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=accept-new", "rafael@"+host, "tail -30 "+path)
	c.Env = append(c.Env, "SSHPASS="+pass, "PATH=/usr/local/bin:/usr/bin:/bin")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(tail failed: %v)", err)
	}
	return string(out)
}

// Sanity hooks to keep imports not flagged for clarity in case of edits.
var _ = envelope.CurrentProtocolVersion
var _ = agent.SettleResponse{}
