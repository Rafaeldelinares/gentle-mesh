package joinlog

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gentleman-programming/gentle-mesh/pkg/gitsafe"
)

// Surface matching semantics (RFC-003 J5, issue #173)
//
// Declared edit surfaces are matched with a doublestar-style glob, implemented
// here to keep the project dependency-free:
//
//   - `*` matches any run of characters **inside a single path segment**. It
//     never crosses the `/` separator. Consequence, and this is an explicit
//     decision: the surface `pkg/joinlog/*` matches `pkg/joinlog/territory.go`
//     but does **not** match `pkg/joinlog/sub/foo.go`.
//   - `**` as a whole segment matches any number of segments, including zero.
//     Consequence: `pkg/joinlog/**` matches `pkg/joinlog/sub/foo.go`.
//   - `?` matches exactly one character inside a segment.
//   - A surface with no wildcard matches the path exactly, and also every path
//     under it when it names a directory prefix (`pkg/joinlog` covers
//     `pkg/joinlog/sub/foo.go`).
//
// Paths are compared with `/` as separator and are cleaned before matching, so
// `./pkg/joinlog/x.go` and `pkg/joinlog/x.go` are the same path.

// MatchSurface reports whether the observed path falls inside the declared surface.
func MatchSurface(surface, observed string) bool {
	s := normalizeSurfacePath(surface)
	p := normalizeSurfacePath(observed)
	if s == "" || p == "" {
		return false
	}
	if s == p {
		return true
	}

	patternSegments := strings.Split(s, "/")
	pathSegments := strings.Split(p, "/")

	// A wildcard-free surface naming a directory covers everything under it.
	if !strings.ContainsAny(s, "*?[") {
		return strings.HasPrefix(p, s+"/")
	}

	return matchSurfaceSegments(patternSegments, pathSegments)
}

func matchSurfaceSegments(pattern, pathSegments []string) bool {
	if len(pattern) == 0 {
		return len(pathSegments) == 0
	}
	if pattern[0] == "**" {
		// `**` matches zero or more segments.
		for i := 0; i <= len(pathSegments); i++ {
			if matchSurfaceSegments(pattern[1:], pathSegments[i:]) {
				return true
			}
		}
		return false
	}
	if len(pathSegments) == 0 {
		return false
	}
	ok, err := filepath.Match(pattern[0], pathSegments[0])
	if err != nil || !ok {
		return false
	}
	return matchSurfaceSegments(pattern[1:], pathSegments[1:])
}

func normalizeSurfacePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}

// VerifyTerritory contrasts the declared edit surfaces against the observed files,
// determining whether the task remained strictly within its declared territory (RFC-003 J5).
//
// The observation is trusted input from the Go harness running outside the agent
// sandbox; the declaration is the agent's own claim. The result is evidence: it
// never replaces the original territory.claimed / territory.released events.
func VerifyTerritory(taskID, repo, branch string, declaredSurfaces, observedFiles []string) TerritoryVerifiedPayload {
	declared := append([]string(nil), declaredSurfaces...)
	observed := append([]string(nil), observedFiles...)

	discrepancies := make([]string, 0)
	for _, file := range observed {
		if !matchesAnySurface(declared, file) {
			discrepancies = append(discrepancies, file)
		}
	}
	sort.Strings(discrepancies)

	status := TerritoryStatusClean
	if len(discrepancies) > 0 {
		status = TerritoryStatusDiscrepancy
	}

	return TerritoryVerifiedPayload{
		TaskID:           taskID,
		GitRepo:          repo,
		GitBranch:        branch,
		DeclaredSurfaces: declared,
		ObservedFiles:    observed,
		Discrepancies:    discrepancies,
		Status:           status,
	}
}

func matchesAnySurface(surfaces []string, file string) bool {
	for _, surface := range surfaces {
		if MatchSurface(surface, file) {
			return true
		}
	}
	return false
}

// InspectWorktreeAndVerify contrasts a task's declared surfaces against the actual
// git diff in worktreeDir between baseRef and HEAD. It runs on the worker host
// outside the agent sandbox, ensuring the observation is untampered by the agent.
//
// The git invocation is hardened through pkg/gitsafe: repository-controlled
// program paths (external diff, textconv, filter drivers) are neutralized, so the
// observed file list cannot be produced by running something the agent planted.
func InspectWorktreeAndVerify(ctx context.Context, worktreeDir, baseRef, taskID, repo, branch string, declaredSurfaces []string) (TerritoryVerifiedPayload, error) {
	cmd, err := gitsafe.Command(ctx, worktreeDir,
		"diff", "--no-ext-diff", "--no-textconv", "--name-only", baseRef+"..HEAD")
	if err != nil {
		return TerritoryVerifiedPayload{}, fmt.Errorf("build git diff: %w", err)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return TerritoryVerifiedPayload{}, fmt.Errorf("git diff failed: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return TerritoryVerifiedPayload{}, fmt.Errorf("git diff failed: %w", err)
	}

	var observed []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		observed = append(observed, filepath.ToSlash(line))
	}
	sort.Strings(observed)

	return VerifyTerritory(taskID, repo, branch, declaredSurfaces, observed), nil
}
