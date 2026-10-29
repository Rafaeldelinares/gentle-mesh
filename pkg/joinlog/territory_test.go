package joinlog_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
)

// RFC-003 J5 (Territorio contrastado) requires that declared territory is
// opportunistically contrasted against observed activity (git diff / files touched),
// recording the evidence in the join log without replacing the original declaration.

// 1. Test Clean Case: all observed files match declared surfaces
func TestTerritory_CleanVerification(t *testing.T) {
	taskID := "task-clean-1"
	repo := "https://github.com/org/repo"
	branch := "feature/joinlog"

	declaredSurfaces := []string{
		"pkg/joinlog/*",
		"docs/rfcs/**",
		"README.md",
	}

	observedFiles := []string{
		"pkg/joinlog/territory.go",
		"docs/rfcs/003-join-protocol-and-a2a-adapter.md",
		"README.md",
	}

	res := joinlog.VerifyTerritory(taskID, repo, branch, declaredSurfaces, observedFiles)

	if res.Status != joinlog.TerritoryStatusClean {
		t.Fatalf("expected status CLEAN, got %q (discrepancies: %v)", res.Status, res.Discrepancies)
	}
	if len(res.Discrepancies) != 0 {
		t.Fatalf("expected 0 discrepancies for clean verification, got %v", res.Discrepancies)
	}
	if !reflect.DeepEqual(res.DeclaredSurfaces, declaredSurfaces) {
		t.Fatalf("declared surfaces mismatch: got %v, want %v", res.DeclaredSurfaces, declaredSurfaces)
	}
	if !reflect.DeepEqual(res.ObservedFiles, observedFiles) {
		t.Fatalf("observed files mismatch: got %v, want %v", res.ObservedFiles, observedFiles)
	}
}

// 2. Test Discrepancy Case: observed files touch areas outside declared surfaces
func TestTerritory_DiscrepancyVerification(t *testing.T) {
	taskID := "task-discrepancy-1"
	repo := "https://github.com/org/repo"
	branch := "feature/auth"

	declaredSurfaces := []string{
		"pkg/joinlog/*",
	}

	observedFiles := []string{
		"pkg/joinlog/joinlog.go",
		".github/workflows/ci.yml", // OUTSIDE
		"cmd/gentle-mesh/main.go",   // OUTSIDE
	}

	res := joinlog.VerifyTerritory(taskID, repo, branch, declaredSurfaces, observedFiles)

	if res.Status != joinlog.TerritoryStatusDiscrepancy {
		t.Fatalf("expected status DISCREPANCY, got %q", res.Status)
	}

	expectedDiscrepancies := []string{
		".github/workflows/ci.yml",
		"cmd/gentle-mesh/main.go",
	}
	sort.Strings(res.Discrepancies)
	sort.Strings(expectedDiscrepancies)

	if !reflect.DeepEqual(res.Discrepancies, expectedDiscrepancies) {
		t.Fatalf("discrepancies mismatch: got %v, want %v", res.Discrepancies, expectedDiscrepancies)
	}
}

// TestTerritory_SurfaceMatchingBoundary pins the glob semantics explicitly, so
// they are a decision and not an accident of the pattern library used.
//
// The decision: `*` never crosses a path separator, so `pkg/joinlog/*` does NOT
// cover `pkg/joinlog/sub/foo.go`. To cover nested files a surface must use `**`
// (`pkg/joinlog/**`) or name the directory without a wildcard (`pkg/joinlog`).
func TestTerritory_SurfaceMatchingBoundary(t *testing.T) {
	cases := []struct {
		surface string
		path    string
		want    bool
		why     string
	}{
		{"pkg/joinlog/*", "pkg/joinlog/territory.go", true, "single segment under the declared dir"},
		{"pkg/joinlog/*", "pkg/joinlog/sub/foo.go", false, "* does not cross the separator"},
		{"pkg/joinlog/**", "pkg/joinlog/sub/foo.go", true, "** crosses any number of segments"},
		{"pkg/joinlog/**", "pkg/joinlog/territory.go", true, "** also covers the direct children"},
		{"pkg/joinlog", "pkg/joinlog/sub/foo.go", true, "a wildcard-free surface names a directory prefix"},
		{"pkg/joinlog", "pkg/joinlog", true, "exact match"},
		{"README.md", "README.md", true, "exact match"},
		{"README.md", "docs/README.md", false, "exact match does not cover a nested file"},
		{"pkg/*/x.go", "pkg/joinlog/x.go", true, "* matches inside one segment"},
		{"pkg/*/x.go", "pkg/a/b/x.go", false, "* does not cross the separator"},
	}

	for _, tc := range cases {
		got := joinlog.MatchSurface(tc.surface, tc.path)
		if got != tc.want {
			t.Errorf("MatchSurface(%q, %q) = %v, want %v (%s)", tc.surface, tc.path, got, tc.want, tc.why)
		}
	}
}

// TestTerritory_VerifiedEventIsAdditiveEvidence pins that the territory.verified
// event is bound to the same task_id and is additional evidence: it never
// replaces nor undoes the territory.claimed / territory.released events.
func TestTerritory_VerifiedEventIsAdditiveEvidence(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	log, err := joinlog.New(filepath.Join(tempDir, "territory_evidence.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer log.Close()

	projector, err := joinlog.NewProjector(filepath.Join(tempDir, "territory_evidence_proj.db"))
	if err != nil {
		t.Fatalf("open projector: %v", err)
	}
	defer projector.Close()

	appendAndProject := func(eventType, actor string, payload any) {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		evt := joinlog.Event{
			Type:      eventType,
			Actor:     actor,
			Payload:   data,
			CreatedAt: time.Now().UTC(),
		}
		if err := log.Append(ctx, evt); err != nil {
			t.Fatalf("append %s: %v", eventType, err)
		}
		if err := projector.ProjectEvent(ctx, evt); err != nil {
			t.Fatalf("project %s: %v", eventType, err)
		}
	}

	const repo = "https://github.com/org/repo"
	const branch = "feature/auth"
	const taskID = "task-evidence-1"

	appendAndProject(joinlog.EventTypeTerritoryClaimed, taskID, joinlog.TerritoryClaimedPayload{
		TaskID:    taskID,
		GitRepo:   repo,
		GitBranch: branch,
	})
	appendAndProject(joinlog.EventTypeTerritoryReleased, taskID, joinlog.TerritoryReleasedPayload{
		TaskID:    taskID,
		GitRepo:   repo,
		GitBranch: branch,
		Reason:    joinlog.ReasonTaskFinished,
	})

	verification := joinlog.VerifyTerritory(taskID, repo, branch,
		[]string{"pkg/joinlog/*"},
		[]string{"pkg/joinlog/territory.go", "cmd/gentle-mesh/main.go"})
	appendAndProject(joinlog.EventTypeTerritoryVerified, "harness-go", verification)

	state, err := projector.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// The release stands: the verification does not resurrect the lock.
	territoryKey := repo + ":" + branch
	if _, locked := state.ActiveLocks[territoryKey]; locked {
		t.Fatalf("territory.verified must not re-create an active lock, got %+v", state.ActiveLocks)
	}

	// The release history is untouched.
	if len(state.TerritoryHistory) != 1 {
		t.Fatalf("expected 1 territory history entry, got %d: %+v", len(state.TerritoryHistory), state.TerritoryHistory)
	}
	if state.TerritoryHistory[0].Reason != joinlog.ReasonTaskFinished {
		t.Fatalf("expected release reason %q, got %q", joinlog.ReasonTaskFinished, state.TerritoryHistory[0].Reason)
	}
	if state.TerritoryHistory[0].TaskID != taskID {
		t.Fatalf("expected history task_id %q, got %q", taskID, state.TerritoryHistory[0].TaskID)
	}

	// The verification is recorded as additional evidence bound to the same task_id.
	if len(state.TerritoryVerifications) != 1 {
		t.Fatalf("expected 1 territory verification, got %d", len(state.TerritoryVerifications))
	}
	v := state.TerritoryVerifications[0]
	if v.TaskID != taskID {
		t.Fatalf("expected verification bound to task_id %q, got %q", taskID, v.TaskID)
	}
	if v.Status != joinlog.TerritoryStatusDiscrepancy {
		t.Fatalf("expected verification status DISCREPANCY, got %q", v.Status)
	}
	if len(v.Discrepancies) != 1 || v.Discrepancies[0] != "cmd/gentle-mesh/main.go" {
		t.Fatalf("expected discrepancy [cmd/gentle-mesh/main.go], got %v", v.Discrepancies)
	}
	if len(v.ObservedFiles) != 2 {
		t.Fatalf("expected 2 observed files, got %v", v.ObservedFiles)
	}
}

// TestTerritory_RejectsOptionLikeBaseRef proves that a base ref crafted to look
// like a git option cannot turn the observation into an argument-injection
// primitive (same family as #35: attacker-controlled data concatenated into an
// external command without neutralization).
//
// The shape: baseRef = "--output=<path>" becomes the diff operand
// "--output=<path>..HEAD", which git parses as the --output option (write the
// diff to a file), not as a revision. The harness would then write a file at an
// attacker-chosen path, with attacker-influenced content, outside the sandbox.
func TestTerritory_RejectsOptionLikeBaseRef(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, string(out))
		}
	}

	repoDir := filepath.Join(tempDir, "worktree")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(repoDir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit(repoDir, "add", "base.txt")
	runGit(repoDir, "commit", "-m", "initial commit")

	// The attacker chooses where the injected write lands.
	writeDir := t.TempDir()
	maliciousBase := "--output=" + filepath.Join(writeDir, "pwned")

	_, err := joinlog.InspectWorktreeAndVerify(ctx, repoDir, maliciousBase, "task-evil", "repo", "branch", []string{"pkg/*"})
	if err == nil {
		t.Errorf("expected an error for an option-like base ref %q, got nil", maliciousBase)
	}

	// The decisive assertion: nothing was written outside the worktree.
	entries, readErr := os.ReadDir(writeDir)
	if readErr != nil {
		t.Fatalf("read write dir: %v", readErr)
	}
	for _, entry := range entries {
		t.Errorf("option-like base ref %q caused a file write outside the worktree: %s", maliciousBase, entry.Name())
	}
}

// TestTerritory_RejectsOtherOptionLikeBaseRefs covers the rest of the class:
// any operand that git would parse as an option instead of a revision.
func TestTerritory_RejectsOtherOptionLikeBaseRefs(t *testing.T) {
	for _, base := range []string{
		"--output=/tmp/anything",
		"-p",
		"--stat",
		"--no-index",
		"",
		"main..HEAD",
		"refs/heads/main^{}",
	} {
		if err := joinlog.ValidateRevisionName(base); err == nil {
			t.Errorf("expected ValidateRevisionName(%q) to fail, got nil", base)
		}
	}

	for _, base := range []string{"main", "origin/main", "release-1.2", "feature/J5_territory"} {
		if err := joinlog.ValidateRevisionName(base); err != nil {
			t.Errorf("expected ValidateRevisionName(%q) to pass, got %v", base, err)
		}
	}
}

// TestTerritory_InspectWorktree tests the real Git worktree inspection.
func TestTerritory_InspectWorktree(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	// Initialize real git repo
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, string(out))
		}
	}

	repoDir := filepath.Join(tempDir, "worktree")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	runGit(repoDir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit(repoDir, "add", "base.txt")
	runGit(repoDir, "commit", "-m", "initial commit")

	// Create proposal branch and modify files
	runGit(repoDir, "checkout", "-b", "proposal/task-wt")
	if err := os.MkdirAll(filepath.Join(repoDir, "pkg", "joinlog"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "joinlog", "t.go"), []byte("code"), 0644); err != nil {
		t.Fatalf("write t.go: %v", err)
	}
	// Write file outside declared area.
	// The name deliberately avoids common secret extensions (*.key, *.pem):
	// a host-wide gitignore would silently drop them from the commit, and the
	// diff-based observation would never see them.
	if err := os.WriteFile(filepath.Join(repoDir, "out-of-scope.txt"), []byte("leak"), 0644); err != nil {
		t.Fatalf("write out-of-scope.txt: %v", err)
	}
	runGit(repoDir, "add", ".")
	runGit(repoDir, "commit", "-m", "work in progress")

	declaredSurfaces := []string{"pkg/joinlog/*"}

	res, err := joinlog.InspectWorktreeAndVerify(ctx, repoDir, "main", "task-wt", "repo-x", "proposal/task-wt", declaredSurfaces)
	if err != nil {
		t.Fatalf("InspectWorktreeAndVerify failed: %v", err)
	}

	if res.Status != joinlog.TerritoryStatusDiscrepancy {
		t.Fatalf("expected DISCREPANCY, got %q", res.Status)
	}
	if len(res.Discrepancies) != 1 || res.Discrepancies[0] != "out-of-scope.txt" {
		t.Fatalf("expected [out-of-scope.txt] in discrepancies, got %v", res.Discrepancies)
	}
}
