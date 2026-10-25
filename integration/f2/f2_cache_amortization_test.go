//go:build f2real

// F2-3b: cache amortization with a PERSISTENT worker.
//
// F2-3 measured the worst case for the parallel mode's cost reading: provider
// prompt-cache state varies per worker startup (cold cache pays the full
// fixed context; F2-3 run 2 saw input=25276 cold vs run 1 input=2114 warm).
//
// This variant isolates the amortization curve: ONE persistent worker
// (-concurrency 1) processes the same 4 trivial tasks in STRICT sequence,
// with the gap between tasks far below the provider cache TTL (~5 min).
//
// Expected shape if the cache amortizes: task 1 pays cold-cache cost, tasks
// 2-4 serve the fixed sandbox context from cache (cheaper per task). The
// totalTokens count stays structural (~25k/task vs ~10.9k direct pi) but the
// USD cost per task should converge toward the direct-call floor.
package f2

import (
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestF2_PersistentWorkerCacheAmortization(t *testing.T) {
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

	// ── ONE persistent worker, strict serial (concurrency 1) ──
	workerPort := f2FreePort(t)
	f2Start(t, work, "worker-w-persist", meshBin, "worker",
		"-coordinator", coordURL,
		"-token", "A",
		"-node-id", "w-persist",
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
		"-concurrency", "1",
		"-tasks-dir", filepath.Join(work, "worker-tasks"),
	)
	f2WaitFor(t, "persistent worker joined", 60*time.Second, func() bool {
		_, body := f2CoordHTTP(t, http.MethodGet, coordURL+"/v1/mesh/nodes", "")
		return strings.Contains(body, "w-persist")
	})

	// ── 4 tasks in strict sequence; the degradé IS the result ──
	const tasks = 4
	var totalCost, totalTokens float64
	for i := 1; i <= tasks; i++ {
		prompt := fmt.Sprintf("Create a file named amort-%d.txt containing exactly one line: AMORT-%d-OK. Do not create or modify anything else. Commit the change with git (git add amort-%d.txt && git commit -m 'f2b: task %d').", i, i, i, i)
		taskID := f2DispatchTask(t, coordURL, prompt)
		id := taskID
		f2WaitForDiag(t, fmt.Sprintf("persistent task %d completed", i), 6*time.Minute, func() bool {
			_, body := f2CoordHTTP(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s", coordURL, id), "")
			return strings.Contains(body, `"completed"`)
		}, func() string {
			return f2Tail(filepath.Join(work, "worker-w-persist.log"))
		})
		u := f2TaskUsage(t, coordURL, taskID)
		totalTokens += float64(u.TotalTokens)
		totalCost += u.Cost
		t.Logf("TASK %d: totalTokens=%d cost=%.5f (input=%d output=%d cacheRead=%d) — gap since previous task: <5min",
			i, u.TotalTokens, u.Cost, u.Input, u.Output, u.CacheRead)
	}
	t.Logf("PERSISTENT WORKER TOTAL: %d tasks, totalTokens=%.0f cost=%.5f — avg cost/task=%.5f",
		tasks, totalTokens, totalCost, totalCost/float64(tasks))
	t.Logf("REFERENCE: direct pi call floor ~10.9k tokens ~$0.00075/task; F2-3 parallel cold-ish avg ~$0.0019-0.0031/task")
}
