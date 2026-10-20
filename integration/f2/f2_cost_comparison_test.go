//go:build f2real

// F2-3: parallel vs serial cost comparison with REAL token accounting.
//
// Pregunta (roadmap Fase 2 / impuesto señalado por Alan): ¿compensa el
// paralelismo el impuesto de coordinación? Métrica DIRECTA (F2-1a + work-unit
// #11): cada tarea delegada expone su uso real en TaskState.Completion.Result
// como "Usage: input=N output=N cacheRead=N totalTokens=N cost=X".
//
// Dos fases, el mismo trabajo (4 tareas triviales con commit):
//
//  1. PARALELO: 2 workers (-runner gentle-shell, -concurrency 2) + las 4
//     tareas despachadas juntas — wall-clock desde el dispatch hasta la última
//     completed, tokens/cost sumados por task.
//  2. SERIE: un solo agente (pi directo local, sin coordinador ni sandbox)
//     ejecutando las 4 tareas una por una — wall-clock acumulado + tokens/cost
//     parseados del stream de cada llamada.
//
// El speedup y el impuesto se REPORTAN (t.Logf) — la conclusión es del
// análisis, no del test. El test aserta que ambos modos completaron el trabajo
// y que la métrica existió en cada ejecución.
package f2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var f2UsageRe = regexp.MustCompile(`Usage: input=(\d+) output=(\d+) cacheRead=(\d+) totalTokens=(\d+) cost=([0-9.e+-]+)`)

type f2Usage struct {
	Input       int64
	Output      int64
	CacheRead   int64
	TotalTokens int64
	Cost        float64
}

func f2ParseUsageLine(s string) (f2Usage, bool) {
	m := f2UsageRe.FindStringSubmatch(s)
	if m == nil {
		return f2Usage{}, false
	}
	in, _ := strconv.ParseInt(m[1], 10, 64)
	out, _ := strconv.ParseInt(m[2], 10, 64)
	cache, _ := strconv.ParseInt(m[3], 10, 64)
	total, _ := strconv.ParseInt(m[4], 10, 64)
	cost, _ := strconv.ParseFloat(m[5], 64)
	return f2Usage{Input: in, Output: out, CacheRead: cache, TotalTokens: total, Cost: cost}, true
}

// f2TaskUsage reads the task state and parses the usage line from the
// completion result.
func f2TaskUsage(t *testing.T, coordURL, taskID string) f2Usage {
	t.Helper()
	_, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, taskID), "")
	var ts struct {
		Completion *struct {
			Result string `json:"result"`
		} `json:"completion"`
	}
	if err := json.Unmarshal([]byte(body), &ts); err != nil {
		t.Fatalf("decode task state %s: %v: %s", taskID, err, body)
	}
	if ts.Completion == nil {
		t.Fatalf("task %s completed without a completion payload", taskID)
	}
	usage, ok := f2ParseUsageLine(ts.Completion.Result)
	if !ok {
		t.Fatalf("task %s completion result lacks a parseable usage line: %q", taskID, ts.Completion.Result)
	}
	return usage
}

// f2SerialPiUsage runs one local pi call (no coordinator, no sandbox) and
// parses the usage from its NDJSON stream: the last non-zero totalTokens
// wins (interim turns observed reporting zeros).
func f2SerialPiUsage(t *testing.T, dir, prompt string) (f2Usage, time.Duration) {
	t.Helper()
	start := time.Now()
	cmd := exec.Command("pi", "--print", "--mode", "json", prompt)
	cmd.Dir = dir
	outBytes, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("serial pi call failed (%v): %s", err, f2Tail(string(outBytes)))
	}
	var usage f2Usage
	found := false
	for _, line := range strings.Split(string(outBytes), "\n") {
		if u, ok := f2ParseUsageLine(line); ok && u.TotalTokens > 0 {
			usage, found = u, true
		}
		if strings.Contains(line, `"usage"`) {
			// Fallback: parse pi's own usage JSON from the stream event.
			var evt struct {
				Message *struct {
					Usage *struct {
						Input       int64 `json:"input"`
						Output      int64 `json:"output"`
						CacheRead   int64 `json:"cacheRead"`
						TotalTokens int64 `json:"totalTokens"`
						Cost        *struct {
							Total float64 `json:"total"`
						} `json:"cost"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &evt) == nil && evt.Message != nil && evt.Message.Usage != nil && evt.Message.Usage.TotalTokens > 0 {
				usage = f2Usage{
					Input:       evt.Message.Usage.Input,
					Output:      evt.Message.Usage.Output,
					CacheRead:   evt.Message.Usage.CacheRead,
					TotalTokens: evt.Message.Usage.TotalTokens,
				}
				if evt.Message.Usage.Cost != nil {
					usage.Cost = evt.Message.Usage.Cost.Total
				}
				found = true
			}
		}
	}
	if !found || usage.TotalTokens == 0 {
		t.Fatalf("serial pi call produced no parseable usage: %s", f2Tail(string(outBytes)))
	}
	return usage, elapsed
}

func TestF2_ParallelVsSerialCost(t *testing.T) {
	for _, dep := range []string{"gentle-shell", "bwrap", "pi", "git"} {
		if _, err := exec.LookPath(dep); err != nil {
			t.Skipf("dependency %q not found in PATH: %v", dep, err)
		}
	}

	repo := repoRoot(t)
	work := t.TempDir()
	t.Cleanup(f2KillAll)

	meshBin := filepath.Join(work, "gentle-mesh")
	f2Build(t, repo, meshBin, "./cmd/gentle-mesh")
	repoCache := f2SeedBareRepo(t, work)
	worktrees := filepath.Join(work, "worktrees")

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

	// ── FASE 1: PARALELO — 2 workers, 4 tasks dispatched together ──
	const tasks = 4
	for _, w := range []string{"w-para-a", "w-para-b"} {
		port := f2FreePort(t)
		f2Start(t, work, "worker-"+w, meshBin, "worker",
			"-coordinator", coordURL,
			"-token", "A",
			"-node-id", w,
			"-endpoint", fmt.Sprintf("http://127.0.0.1:%d", port),
			"-addr", fmt.Sprintf("127.0.0.1:%d", port),
			"-heartbeat-interval", "1s",
			"-runner", "gentle-shell",
			"-gentle-shell-bin", "gentle-shell",
			"-bwrap-bin", "bwrap",
			"-gentle-shell-repo-cache", repoCache,
			"-gentle-shell-worktrees", worktrees,
			"-gentle-shell-base-ref", "main",
			"-gentle-shell-total-timeout", "8m",
			"-concurrency", "2",
			"-tasks-dir", filepath.Join(work, "worker-tasks"),
		)
	}
	f2WaitFor(t, "both parallel workers joined", 60*time.Second, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		return strings.Count(body, "w-para-") >= 2
	})

	parallelStart := time.Now()
	taskIDs := make([]string, tasks)
	for i := 0; i < tasks; i++ {
		prompt := fmt.Sprintf("Create a file named task-%d.txt containing exactly one line: TASK-%d-OK. Do not create or modify anything else. Commit the change with git (git add task-%d.txt && git commit -m 'f2: task %d').", i+1, i+1, i+1, i+1)
		taskIDs[i] = f2DispatchTask(t, coordURL, prompt)
	}
	var parallelUsage f2Usage
	for i, id := range taskIDs {
		id := id
		f2WaitForDiag(t, fmt.Sprintf("parallel task %d completed", i+1), 6*time.Minute, func() bool {
			_, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, id), "")
			return strings.Contains(body, `"completed"`)
		}, func() string {
			return f2Tail(filepath.Join(work, "worker-w-para-a.log")) + "\n---\n" + f2Tail(filepath.Join(work, "worker-w-para-b.log"))
		})
		u := f2TaskUsage(t, coordURL, id)
		parallelUsage.Input += u.Input
		parallelUsage.Output += u.Output
		parallelUsage.CacheRead += u.CacheRead
		parallelUsage.TotalTokens += u.TotalTokens
		parallelUsage.Cost += u.Cost
		t.Logf("  task %d: totalTokens=%d cost=%.5f (input=%d output=%d)", i+1, u.TotalTokens, u.Cost, u.Input, u.Output)
	}
	parallelWall := time.Since(parallelStart)
	t.Logf("PARALLEL: wall=%v totalTokens=%d cost=%.5f (input=%d output=%d) across %d tasks",
		parallelWall, parallelUsage.TotalTokens, parallelUsage.Cost, parallelUsage.Input, parallelUsage.Output, tasks)

	// ── FASE 2: SERIE — one local agent, 4 tasks one at a time ──
	serialDir := filepath.Join(work, "serial")
	if err := os.MkdirAll(serialDir, 0o755); err != nil {
		t.Fatalf("mkdir serial dir: %v", err)
	}
	var serialUsage f2Usage
	serialStart := time.Now()
	for i := 0; i < tasks; i++ {
		prompt := fmt.Sprintf("Create a file named task-s%d.txt containing exactly one line: TASK-%d-OK. Do not create or modify anything else.", i+1, i+1)
		u, elapsed := f2SerialPiUsage(t, serialDir, prompt)
		serialUsage.Input += u.Input
		serialUsage.Output += u.Output
		serialUsage.CacheRead += u.CacheRead
		serialUsage.TotalTokens += u.TotalTokens
		serialUsage.Cost += u.Cost
		t.Logf("  serial call %d: totalTokens=%d cost=%.5f (input=%d output=%d, %v)", i+1, u.TotalTokens, u.Cost, u.Input, u.Output, elapsed)
	}
	serialWall := time.Since(serialStart)
	t.Logf("SERIAL: wall=%v totalTokens=%d cost=%.5f (input=%d output=%d) across %d calls",
		serialWall, serialUsage.TotalTokens, serialUsage.Cost, serialUsage.Input, serialUsage.Output, tasks)

	// ── Work produced in both modes ──
	for i := 1; i <= tasks; i++ {
		if _, err := os.Stat(filepath.Join(serialDir, fmt.Sprintf("task-s%d.txt", i))); err != nil {
			t.Fatalf("serial agent did not produce task-s%d.txt: %v", i, err)
		}
	}

	// ── The verdict, reported ──
	speedup := float64(serialWall) / float64(parallelWall)
	tokenOverhead := float64(parallelUsage.TotalTokens) / float64(serialUsage.TotalTokens)
	t.Logf("VERDICT: parallel=%v serial=%v → speedup=%.2fx | tokens parallel=%d serial=%d → overhead=%.2fx | cost parallel=%.5f serial=%.5f",
		parallelWall, serialWall, speedup, parallelUsage.TotalTokens, serialUsage.TotalTokens, tokenOverhead, parallelUsage.Cost, serialUsage.Cost)
}
