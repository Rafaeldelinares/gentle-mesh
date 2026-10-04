package envelope

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
)

// Validation errors.
var (
	ErrUnknownProtocolVersion = errors.New("protocol_version: must be strictly \"2\"")
	ErrInvalidMeshID          = errors.New("mesh_id: must be non-empty")
	ErrMeshMismatch           = errors.New("mesh_id: network mismatch")
	ErrInvalidEnvelopeID      = errors.New("envelope_id: must be a non-empty UUID")
	ErrInvalidEmitter         = errors.New("emitter_agent_id: must be non-empty")
	ErrInvalidExecutor        = errors.New("executor_agent_id: must be non-empty")
	ErrSameAgent              = errors.New("emitter and executor must differ")
	ErrInvalidTerritory       = errors.New("territory: invalid")
	ErrNoAssertions           = errors.New("assertions: at least one is required")
	ErrInvalidAssertionID     = errors.New("assertion: id must be non-empty")
	ErrInvalidAssertionType   = errors.New("assertion: unknown type")
	ErrNegativeTimeout        = errors.New("timeout_seconds: must be positive")
	ErrNegativeRemediations   = errors.New("max_remediations: must be non-negative")
)

// Validate checks that the envelope is structurally and semantically valid.
// It does NOT verify cryptographic signatures.
func Validate(env *CognitiveTaskEnvelope) error {
	if env == nil {
		return errors.New("envelope: nil")
	}

	// Protocol version (S9: must be strictly "2")
	if env.ProtocolVersion != CurrentProtocolVersion {
		return ErrUnknownProtocolVersion
	}

	// MeshID (S9: must be non-empty)
	if strings.TrimSpace(env.MeshID) == "" {
		return ErrInvalidMeshID
	}

	// ID
	if env.EnvelopeID == "" {
		return ErrInvalidEnvelopeID
	}

	// Agents
	if env.EmitterAgentID == "" {
		return ErrInvalidEmitter
	}
	if env.ExecutorAgentID == "" {
		return ErrInvalidExecutor
	}
	if env.EmitterAgentID == env.ExecutorAgentID {
		return ErrSameAgent
	}

	// Territory
	if err := ValidateTerritory(&env.Territory); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerritory, err)
	}

	// Assertions
	if len(env.Assertions) == 0 {
		return ErrNoAssertions
	}
	seenIDs := make(map[string]bool)
	for i := range env.Assertions {
		if err := ValidateAssertion(&env.Assertions[i]); err != nil {
			return fmt.Errorf("assertion[%d]: %w", i, err)
		}
		if seenIDs[env.Assertions[i].ID] {
			return fmt.Errorf("assertion: duplicate id %q", env.Assertions[i].ID)
		}
		seenIDs[env.Assertions[i].ID] = true
	}

	// Constraints
	if env.TimeoutSeconds <= 0 {
		return ErrNegativeTimeout
	}
	if env.MaxRemediations < 0 {
		return ErrNegativeRemediations
	}

	return nil
}

// ValidateLease checks that a lease is structurally and semantically valid under S9.
func ValidateLease(l *Lease) error {
	if l == nil {
		return errors.New("lease: nil")
	}
	if l.ProtocolVersion != CurrentProtocolVersion {
		return ErrUnknownProtocolVersion
	}
	if strings.TrimSpace(l.MeshID) == "" {
		return ErrInvalidMeshID
	}
	if l.LeaseID == "" {
		return errors.New("lease_id: must be non-empty")
	}
	if l.EnvelopeID == "" {
		return errors.New("envelope_id: must be non-empty")
	}
	if l.ExecutorAgentID == "" {
		return errors.New("executor_agent_id: must be non-empty")
	}
	return nil
}

// ValidateTerritory checks the territory fields.
func ValidateTerritory(t *Territory) error {
	if t == nil {
		return errors.New("territory: nil")
	}
	if t.Repository == "" {
		return errors.New("repository: empty")
	}
	// Normalize repository: remove trailing slash and .git suffix.
	repo := strings.TrimSuffix(strings.TrimSuffix(t.Repository, "/"), ".git")
	if repo == "" {
		return errors.New("repository: empty after normalization")
	}
	if !isValidRepoURL(repo) {
		return fmt.Errorf("repository: malformed %q", t.Repository)
	}
	if t.Branch == "" {
		return errors.New("branch: empty")
	}
	// Basic Git ref name validation.
	if strings.ContainsAny(t.Branch, " \t\n") {
		return errors.New("branch: contains invalid characters")
	}
	if t.WorkspacePath == "" {
		return errors.New("workspace_path: empty")
	}
	// Workspace path should be absolute.
	if !filepath.IsAbs(t.WorkspacePath) {
		return fmt.Errorf("workspace_path: must be absolute, got %q", t.WorkspacePath)
	}
	return nil
}

// isValidRepoURL checks that a repository identifier looks reasonable.
// Accepts: github.com/org/repo, git@host:path, https://host/path
func isValidRepoURL(repo string) bool {
	if strings.Contains(repo, "://") {
		_, err := url.Parse(repo)
		return err == nil
	}
	// Simple host/path format.
	parts := strings.SplitN(repo, "/", 3)
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

// ValidateAssertion checks a single assertion for validity.
func ValidateAssertion(a *Assertion) error {
	if a == nil {
		return errors.New("assertion: nil")
	}
	if a.ID == "" {
		return ErrInvalidAssertionID
	}
	switch a.Type {
	case AssertionFileModified:
		if a.Params.FilePath == "" {
			return errors.New("file_path: required for file_modified")
		}
		if a.Params.ExpectedSHA256 == "" {
			return errors.New("expected_sha256: required for file_modified")
		}
		if !isSHA256(a.Params.ExpectedSHA256) {
			return errors.New("expected_sha256: must be 64 hex chars")
		}

	case AssertionFileHashEquals:
		if a.Params.FilePath == "" {
			return errors.New("file_path: required for file_hash_equals")
		}
		if !isSHA256(a.Params.ExpectedSHA256) {
			return errors.New("expected_sha256: must be 64 hex chars")
		}

	case AssertionCommandExitCode:
		if a.Params.Command == "" {
			return errors.New("command: required for command_exit_code")
		}

	case AssertionCommandOutputContains:
		if a.Params.Command == "" {
			return errors.New("command: required for command_output_contains")
		}
		if a.Params.ContainsPattern == "" {
			return errors.New("contains_pattern: required for command_output_contains")
		}
		if a.Params.ContainsRegex {
			_, err := regexp.Compile(a.Params.ContainsPattern)
			if err != nil {
				return fmt.Errorf("contains_pattern: invalid regex: %v", err)
			}
		}

	case AssertionNoRegression:
		if a.Params.Command == "" {
			return errors.New("command: required for no_regression")
		}

	case AssertionGitCleanWorktree:
		// No specific params required.

	case AssertionPortAvailable:
		if a.Params.Port <= 0 || a.Params.Port > 65535 {
			return fmt.Errorf("port: must be 1-65535, got %d", a.Params.Port)
		}

	default:
		return fmt.Errorf("%w: %q", ErrInvalidAssertionType, a.Type)
	}

	return nil
}

// ValidatePrecondition checks a single precondition for validity.
func ValidatePrecondition(p *Precondition) error {
	if p == nil {
		return errors.New("precondition: nil")
	}
	switch p.Type {
	case PreconditionCommandExitCode:
		if p.Params["command"] == "" {
			return errors.New("command: required for command_exit_code precondition")
		}
	case PreconditionGitCleanWorktree:
		// No params required.
	case PreconditionPortAvailable:
		// Port is parsed at evaluation time.
	case PreconditionToolAvailable:
		if p.Params["tool"] == "" {
			return errors.New("tool: required for tool_available precondition")
		}
	default:
		return fmt.Errorf("precondition: unknown type %q", p.Type)
	}
	return nil
}

// isSHA256 checks if a string is a valid 64-char hex SHA-256.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────
// JCS Hash computation
// ─────────────────────────────────────────────────────────────────

// ComputeEnvelopeHash computes the JCS canonical SHA-256 hash of the envelope.
// The envelope must pass Validate before hashing.
//
// The hash is computed over the envelope with both signature fields set to "",
// per RFC 8785 §3.2.5. This ensures the hash is independent of any signatures
// added after creation.
func ComputeEnvelopeHash(env *CognitiveTaskEnvelope) (string, error) {
	if err := Validate(env); err != nil {
		return "", fmt.Errorf("cannot hash invalid envelope: %w", err)
	}

	// Build the signable envelope: copies the envelope with empty signatures.
	// Normalize the repository identifier per spec: remove trailing slash and .git.
	repo := strings.TrimSuffix(strings.TrimSuffix(env.Territory.Repository, "/"), ".git")

	signable := &CognitiveTaskEnvelope{
		ProtocolVersion: env.ProtocolVersion,
		MeshID:          env.MeshID,
		EnvelopeID:      env.EnvelopeID,
		EmitterAgentID:  env.EmitterAgentID,
		ExecutorAgentID: env.ExecutorAgentID,
		Territory: Territory{
			Repository:    repo,
			Branch:        env.Territory.Branch,
			WorkspacePath: env.Territory.WorkspacePath,
		},
		Preconditions:   env.Preconditions,
		Assertions:      env.Assertions,
		TimeoutSeconds:  env.TimeoutSeconds,
		MaxRemediations: env.MaxRemediations,
		NoSubdelegation: env.NoSubdelegation,
		CreatedAt:       env.CreatedAt,
		// Signature fields intentionally omitted (defaults to "").
	}

	// Marshal and canonicalize.
	data, err := jcs.Marshal(signable)
	if err != nil {
		return "", fmt.Errorf("jcs marshal failed: %w", err)
	}

	hash, err := jcs.HashHex(data)
	if err != nil {
		return "", fmt.Errorf("jcs hash failed: %w", err)
	}

	return hash, nil
}

// ─────────────────────────────────────────────────────────────────
// File system helpers
// ─────────────────────────────────────────────────────────────────

// ResolvePath returns the absolute path for a file parameter.
// If path is absolute, returns it as-is. If relative, joins with workspace.
func ResolvePath(workspace, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(workspace, filePath)
}

// FileExists checks if a file exists and is accessible.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
