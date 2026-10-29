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

// ValidateRevisionName rejects a revision that git could parse as an option
// instead of a revision.
//
// This is the guard for the argument-injection class of #35: a base ref that
// starts with "-" (for example "--output=/some/path") would be parsed by git as
// an option, not as a revision, and the concatenated range "<base>..HEAD" would
// carry that option into the diff. The allowed shape is a plain ref name:
// alphanumerics plus ".", "_", "/" and "-", never starting with "-" or ".",
// and never containing "..".
func ValidateRevisionName(rev string) error {
	if rev == "" {
		return errors.New("revision is empty")
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("revision %q starts with '-' and would be parsed as an option", rev)
	}
	if strings.HasPrefix(rev, ".") {
		return fmt.Errorf("revision %q starts with '.'", rev)
	}
	if strings.Contains(rev, "..") {
		return fmt.Errorf("revision %q contains '..'", rev)
	}
	for _, r := range rev {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '/' || r == '-':
		default:
			return fmt.Errorf("revision %q contains the disallowed character %q", rev, string(r))
		}
	}
	return nil
}

// resolveCommitSHA resolves a validated ref name to a full commit SHA.
//
// Resolving to a SHA is what makes the diff operand inert: a 40-hex string
// cannot be parsed as an option, whatever the caller passed in.
func resolveCommitSHA(ctx context.Context, dir, rev string) (string, error) {
	if err := ValidateRevisionName(rev); err != nil {
		return "", err
	}
	cmd, err := gitsafe.Command(ctx, dir, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("build git rev-parse: %w", err)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve revision %q: %w", rev, err)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) != 40 {
		return "", fmt.Errorf("resolve revision %q: unexpected commit id %q", rev, sha)
	}
	return sha, nil
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
	// The base ref is task-supplied data. It is validated and resolved to a
	// commit SHA before it ever reaches the git argv: a base that starts with
	// "-" would otherwise be parsed as an option (--output=...) and turn the
	// observation into a file-write primitive outside the worktree.
	baseSHA, err := resolveCommitSHA(ctx, worktreeDir, baseRef)
	if err != nil {
		return TerritoryVerifiedPayload{}, fmt.Errorf("invalid base ref: %w", err)
	}

	cmd, err := gitsafe.Command(ctx, worktreeDir,
		"diff", "--no-ext-diff", "--no-textconv", "--name-only", baseSHA+"..HEAD")
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
