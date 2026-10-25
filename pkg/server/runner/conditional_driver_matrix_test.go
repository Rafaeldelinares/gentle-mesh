package runner_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// cdmEnv is a cache prepared for one conditional-driver variant.
type cdmEnv struct {
	cache, worktreesRoot, marker, payload string
}

func cdmSeed(t *testing.T) *cdmEnv {
	t.Helper()
	cache, root := seedBareCache(t)
	e := &cdmEnv{cache: cache, worktreesRoot: root,
		marker: filepath.Join(t.TempDir(), "marker"), payload: filepath.Join(cache, "payload.sh")}
	body := "#!/bin/sh\nprintf 'HOST-EXEC\\n' >> " + shellQuote(e.marker) + "\ncat\nexit 0\n"
	if err := os.WriteFile(e.payload, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *cdmEnv) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(e.cache, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *cdmEnv) appendConfig(t *testing.T, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(e.cache, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func (e *cdmEnv) execs(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(e.marker)
	if err != nil {
		return 0
	}
	return bytes.Count(data, []byte("HOST-EXEC"))
}

func (e *cdmEnv) git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// teeth proves plain git executes the driver for this variant, using the same
// branch name and worktree basename the runner will use. withCheckout selects
// the trigger: a plain checkout (worktree add) or a plain read-tree inside a
// --no-checkout worktree (for conditions only live inside the new worktree).
func (e *cdmEnv) teeth(t *testing.T, branch string, withCheckout bool) bool {
	t.Helper()
	wt := filepath.Join(t.TempDir(), filepath.Base(branch))
	addArgs := []string{"-C", e.cache, "worktree", "add", "-f", "-b", branch, wt, "main"}
	if !withCheckout {
		addArgs = []string{"-C", e.cache, "worktree", "add", "-f", "--no-checkout", "-b", branch, wt, "main"}
	}
	if out, err := e.git(t, "", addArgs...); err != nil {
		t.Logf("teeth worktree add failed: %v: %s", err, out)
		return false
	}
	if !withCheckout {
		if out, err := e.git(t, wt, "read-tree", "-m", "-u", "HEAD"); err != nil {
			t.Logf("teeth read-tree failed: %v: %s", err, out)
		}
	}
	n := e.execs(t)
	_ = os.Remove(e.marker)
	_, _ = e.git(t, "", "-C", e.cache, "worktree", "remove", "--force", wt)
	_, _ = e.git(t, "", "-C", e.cache, "worktree", "prune")
	_, _ = e.git(t, "", "-C", e.cache, "branch", "-D", branch)
	return n > 0
}

func (e *cdmEnv) run(t *testing.T) error {
	t.Helper()
	bwrap := writeFakeBwrap(t, sandboxedGitPrefix+passBwrapScript)
	gs := writeFakeGentleShell(t, fakeGentleShellScript(
		filepath.Join(t.TempDir(), "gs-argv.txt"), gentleShellHappyStream, "", 0))
	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap: bwrap, GentleShell: gs, RepoCache: e.cache, WorktreesRoot: e.worktreesRoot,
		ThinkingLevel: "minimal", TotalTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second,
	}))
	sink := newMockSink(context.Background())
	return r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-cdm", Agent: "worker", Task: "conditional driver must not reach the host"}, sink)
}

// TestGentleShellRunner_HostSideGitIgnoresConditionalDriverVariants is the
// regression test for the enumeration/execution mismatch class: whatever makes
// the driver visible only after HEAD moves (includeIf onbranch / gitdir /
// hasconfig, or an attribute source that only the checkout reads) must not be
// able to run a program on the host. Every variant carries teeth: plain git
// really executes the driver first, so a green result is not vacuous.
func TestGentleShellRunner_HostSideGitIgnoresConditionalDriverVariants(t *testing.T) {
	branch := "proposal/task-cdm"
	cases := []struct {
		name, attrs, cfg string
		teethViaCheckout bool
	}{
		{"onbranch exact", "* filter=pwn\n",
			"\n[includeIf \"onbranch:proposal/task-cdm\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"onbranch wildcard", "* filter=pwn\n",
			"\n[includeIf \"onbranch:proposal/*\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"onbranch doublestar", "* filter=pwn\n",
			"\n[includeIf \"onbranch:proposal/**\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"gitdir worktrees glob", "* filter=pwn\n",
			"\n[includeIf \"gitdir:**/worktrees/**\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"gitdir exact admin dir", "* filter=pwn\n",
			"\n[includeIf \"gitdir:CACHE/worktrees/task-cdm\"]\n\tpath = CACHE/evil.cfg\n", false},
		{"hasconfig remote url", "* filter=pwn\n",
			"\n[remote \"origin\"]\n\turl = https://example.invalid/x.git\n" +
				"[includeIf \"hasconfig:remote.*.url:https://example.invalid/**\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"core.attributesFile", "",
			"\n[core]\n\tattributesFile = CACHE/attrs\n" +
				"\n[includeIf \"onbranch:proposal/*\"]\n\tpath = CACHE/evil.cfg\n", true},
		{"tree gitattributes", "",
			"\n[includeIf \"onbranch:proposal/*\"]\n\tpath = CACHE/evil.cfg\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := cdmSeed(t)
			e.write(t, "evil.cfg", "[filter \"pwn\"]\n\tsmudge = "+e.payload+"\n\trequired = true\n")
			e.appendConfig(t, strings.ReplaceAll(tc.cfg, "CACHE", e.cache))
			if tc.attrs != "" {
				e.write(t, "info/attributes", tc.attrs)
			}
			if tc.name == "core.attributesFile" {
				e.write(t, "attrs", "* filter=pwn\n")
			}
			if tc.name == "tree gitattributes" {
				cdmCommitGitattributes(t, e.cache)
			}
			teeth := e.teeth(t, branch, tc.teethViaCheckout)
			err := e.run(t)
			if n := e.execs(t); n > 0 {
				t.Fatalf("ESCAPE (%s): driver ran on the host %d time(s); Run err=%v", tc.name, n, err)
			}
			if !teeth {
				t.Errorf("variant %q has no teeth on this git build: plain git did not run the driver, so this subtest proves nothing", tc.name)
			}
		})
	}
}

// cdmCommitGitattributes moves the cache's main branch to a commit whose tree
// carries .gitattributes selecting the driver, so the attribute only exists in
// the checked-out tree, never in info/attributes.
func cdmCommitGitattributes(t *testing.T, cache string) {
	t.Helper()
	work := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cdm", "GIT_AUTHOR_EMAIL=cdm@example.com",
			"GIT_COMMITTER_NAME=cdm", "GIT_COMMITTER_EMAIL=cdm@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", cache)
	git("fetch", "-q", "origin", "main")
	git("checkout", "-q", "-b", "work", "origin/main")
	if err := os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("* filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitattributes")
	git("commit", "-qm", "hostile gitattributes")
	git("tag", "cdmbase")
	git("push", "-q", "origin", "refs/tags/cdmbase:refs/heads/main")
}
