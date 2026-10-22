package settlement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Result is the outcome of an assertion evaluation.
type Result string

const (
	ResultPass Result = "PASS"
	ResultFail Result = "FAIL"
	ResultError Result = "ERROR"
)

// Evidence captures the observable output of an assertion.
type Evidence struct {
	// For command assertions.
	Command   string `json:"command,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	// For file assertions.
	ActualHash   string `json:"actual_hash,omitempty"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	FileExists  bool   `json:"file_exists,omitempty"`
	// For git assertions.
	GitStatus string `json:"git_status,omitempty"`
	// For port assertions.
	PortAvailable bool `json:"port_available,omitempty"`
	// For all.
	CheckedAt time.Time `json:"checked_at"`
}

// AssertionResult is the deterministic outcome of evaluating one assertion.
type AssertionResult struct {
	AssertionIndex int                  `json:"assertion_index"`
	AssertionID    string               `json:"assertion_id"`
	AssertionType AssertionType         `json:"assertion_type"`
	Result        Result                `json:"result"`
	Evidence      Evidence              `json:"evidence"`
	Message       string                `json:"message,omitempty"`
}

// Evaluator runs assertions against a target workspace.
type Evaluator struct {
	workingDir string
	env        []string
	timeout    time.Duration
}

// NewEvaluator creates an evaluator targeting the given directory.
func NewEvaluator(workingDir string) *Evaluator {
	return &Evaluator{
		workingDir: workingDir,
		env:        os.Environ(),
		timeout:    60 * time.Second,
	}
}

// WithEnv sets extra environment variables for command execution.
func (e *Evaluator) WithEnv(env []string) *Evaluator {
	e.env = append(e.env, env...)
	return e
}

// WithTimeout sets the maximum time for a single assertion evaluation.
func (e *Evaluator) WithTimeout(timeout time.Duration) *Evaluator {
	e.timeout = timeout
	return e
}

// Evaluate runs a single assertion and returns its result.
func (e *Evaluator) Evaluate(ctx context.Context, assertion *Assertion, index int) *AssertionResult {
	result := &AssertionResult{
		AssertionIndex: index,
		AssertionID:    assertion.ID,
		AssertionType:  assertion.Type,
		Evidence:       Evidence{CheckedAt: time.Now().UTC()},
	}

	// Apply per-assertion timeout, but respect context.
	timeout := e.timeout
	if assertion.Timeout > 0 {
		timeout = assertion.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	switch assertion.Type {
	case AssertionFileModified:
		err = e.evalFileModified(ctx, result, assertion)
	case AssertionFileHashEquals:
		err = e.evalFileHashEquals(ctx, result, assertion)
	case AssertionCommandExitCode:
		err = e.evalCommandExitCode(ctx, result, assertion)
	case AssertionCommandOutputContains:
		err = e.evalCommandOutputContains(ctx, result, assertion)
	case AssertionGitCleanWorktree:
		err = e.evalGitCleanWorktree(ctx, result, assertion)
	case AssertionPortAvailable:
		err = e.evalPortAvailable(ctx, result, assertion)
	case AssertionNoRegression:
		err = e.evalNoRegression(ctx, result, assertion)
	default:
		result.Result = ResultError
		result.Message = fmt.Sprintf("unknown assertion type: %s", assertion.Type)
		return result
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.Result = ResultError
			result.Message = fmt.Sprintf("assertion timed out after %v", timeout)
		} else {
			result.Result = ResultError
			result.Message = err.Error()
		}
	}

	return result
}

// ─────────────────────────────────────────────────────────────────
// Evaluators
// ─────────────────────────────────────────────────────────────────

func (e *Evaluator) evalFileModified(ctx context.Context, result *AssertionResult, a *Assertion) error {
	path, err := e.absPath(fmt.Sprintf("%v", a.Params["path"]))
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Result = ResultFail
			result.Evidence.FileExists = false
			result.Message = fmt.Sprintf("file does not exist: %s", path)
			return nil
		}
		return fmt.Errorf("stat: %w", err)
	}

	result.Evidence.FileExists = true
	result.Result = ResultPass
	result.Message = fmt.Sprintf("file exists and was modified at %s", info.ModTime().Format(time.RFC3339))
	return nil
}

func (e *Evaluator) evalFileHashEquals(ctx context.Context, result *AssertionResult, a *Assertion) error {
	path, err := e.absPath(fmt.Sprintf("%v", a.Params["path"]))
	if err != nil {
		return err
	}
	expectedHash := fmt.Sprintf("%v", a.Params["expected_hash"])

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Result = ResultFail
			result.Evidence.FileExists = false
			result.Message = fmt.Sprintf("file does not exist: %s", path)
			return nil
		}
		return fmt.Errorf("read file: %w", err)
	}

	hash := sha256.Sum256(data)
	actualHash := hex.EncodeToString(hash[:])
	result.Evidence.ActualHash = actualHash
	result.Evidence.ExpectedHash = expectedHash
	result.Evidence.FileExists = true

	if !strings.EqualFold(actualHash, expectedHash) {
		result.Result = ResultFail
		result.Message = fmt.Sprintf("hash mismatch: expected %s, got %s", expectedHash, actualHash)
		return nil
	}

	result.Result = ResultPass
	result.Message = fmt.Sprintf("hash matches: %s", actualHash)
	return nil
}

func (e *Evaluator) evalCommandExitCode(ctx context.Context, result *AssertionResult, a *Assertion) error {
	cmdStr := fmt.Sprintf("%v", a.Params["command"])
	expectedCode, err := toInt(a.Params["expected_code"])
	if err != nil {
		return fmt.Errorf("expected_code: %w", err)
	}

	exitCode, stdout, stderr, err := e.runCommand(ctx, cmdStr)
	result.Evidence.Command = cmdStr
	result.Evidence.ExitCode = exitCode
	result.Evidence.Stdout = truncateOutput(stdout)
	result.Evidence.Stderr = truncateOutput(stderr)

	if err != nil {
		// Detect context deadline/cancellation: process killed by signal.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == -1 {
			return errors.New("context deadline exceeded")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		result.Result = ResultFail
		result.Message = fmt.Sprintf("command error: %v", err)
		return nil
	}

	if exitCode != expectedCode {
		result.Result = ResultFail
		result.Message = fmt.Sprintf("exit code %d, expected %d", exitCode, expectedCode)
		return nil
	}

	result.Result = ResultPass
	result.Message = fmt.Sprintf("exit code %d", exitCode)
	return nil
}

func (e *Evaluator) evalCommandOutputContains(ctx context.Context, result *AssertionResult, a *Assertion) error {
	cmdStr := fmt.Sprintf("%v", a.Params["command"])
	contains := fmt.Sprintf("%v", a.Params["contains"])

	exitCode, stdout, stderr, err := e.runCommand(ctx, cmdStr)
	result.Evidence.Command = cmdStr
	result.Evidence.ExitCode = exitCode
	result.Evidence.Stdout = truncateOutput(stdout)
	result.Evidence.Stderr = truncateOutput(stderr)

	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		result.Result = ResultError
		result.Message = fmt.Sprintf("command error: %v", err)
		return nil
	}

	if !strings.Contains(stdout, contains) {
		result.Result = ResultFail
		result.Message = fmt.Sprintf("output does not contain %q", contains)
		return nil
	}

	result.Result = ResultPass
	result.Message = fmt.Sprintf("output contains %q", contains)
	return nil
}

func (e *Evaluator) evalGitCleanWorktree(ctx context.Context, result *AssertionResult, a *Assertion) error {
	cwd := e.workingDir
	if cwdParam := a.Params["cwd"]; cwdParam != nil && cwdParam != "" {
		resolved, err := e.absPath(fmt.Sprintf("%v", cwdParam))
		if err != nil {
			return err
		}
		cwd = resolved
	}

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = cwd
	cmd.Env = e.env

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}

	result.Evidence.GitStatus = string(out)
	output := strings.TrimSpace(string(out))

	if output != "" {
		result.Result = ResultFail
		result.Message = fmt.Sprintf("git worktree is dirty:\n%s", output)
		return nil
	}

	result.Result = ResultPass
	result.Message = "git worktree is clean"
	return nil
}

func (e *Evaluator) evalPortAvailable(ctx context.Context, result *AssertionResult, a *Assertion) error {
	port, err := toInt(a.Params["port"])
	if err != nil {
		return fmt.Errorf("port: %w", err)
	}

	addr := fmt.Sprintf("localhost:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		result.Evidence.PortAvailable = false
		result.Result = ResultFail
		result.Message = fmt.Sprintf("port %d is already in use", port)
		return nil
	}
	listener.Close()

	result.Evidence.PortAvailable = true
	result.Result = ResultPass
	result.Message = fmt.Sprintf("port %d is available", port)
	return nil
}

func (e *Evaluator) evalNoRegression(ctx context.Context, result *AssertionResult, a *Assertion) error {
	cmdStr := fmt.Sprintf("%v", a.Params["command"])

	exitCode, stdout, stderr, err := e.runCommand(ctx, cmdStr)
	result.Evidence.Command = cmdStr
	result.Evidence.ExitCode = exitCode
	result.Evidence.Stdout = truncateOutput(stdout)
	result.Evidence.Stderr = truncateOutput(stderr)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == -1 {
			return errors.New("context deadline exceeded")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		result.Result = ResultError
		result.Message = fmt.Sprintf("regression test error: %v", err)
		return nil
	}

	if exitCode != 0 {
		result.Result = ResultFail
		result.Message = fmt.Sprintf("regression detected: exit code %d", exitCode)
		return nil
	}

	result.Result = ResultPass
	result.Message = "no regression detected"
	return nil
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

// absPath resolves an assertion-supplied path against the workspace root and
// guarantees the result stays inside it.
//
// Absolute paths and ".." are refused outright, and symlinks are resolved
// before the containment check: a lexical check alone (Clean + ".." prefix +
// filepath.Rel) is defeated by a symlink that sits inside the workspace and
// points out of it, which is exactly the shape an agent commits into a
// worktree.
//
// Residual risk: this is a check-then-use sequence, so a process that can swap
// a symlink between the resolution and the subsequent read could still race it.
// Closing that needs openat2/RESOLVE_BENEATH on every access, which is out of
// scope here; the resolution keeps the common case (a committed symlink,
// pointing at /etc/shadow) contained.
func (e *Evaluator) absPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute path %q is not allowed: assertion paths are relative to the workspace", p)
	}
	root, err := filepath.EvalSymlinks(e.workingDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve workspace root %q: %w", e.workingDir, err)
		}
		root = filepath.Clean(e.workingDir)
	}
	joined := filepath.Join(root, filepath.Clean(p))
	if !withinRoot(root, joined) {
		return "", fmt.Errorf("path %q escapes the workspace root", p)
	}
	resolved, err := resolveExistingPrefix(joined)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, resolved) {
		return "", fmt.Errorf("path %q resolves outside the workspace root", p)
	}
	return joined, nil
}

// withinRoot reports whether p is the root itself or lives under it.
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// resolveExistingPrefix resolves symlinks on the deepest existing ancestor of
// path and re-appends the components that do not exist yet, so a symlink whose
// target does not exist is still judged by where it points.
func resolveExistingPrefix(path string) (string, error) {
	remainder := ""
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func (e *Evaluator) runCommand(ctx context.Context, cmdStr string) (int, string, string, error) {
	// Split into cmd + args using shell-like tokenization.
	parts, err := splitShell(cmdStr)
	if err != nil {
		return -1, "", "", fmt.Errorf("parse command: %w", err)
	}
	if len(parts) == 0 {
		return -1, "", "", errors.New("empty command")
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = e.workingDir
	cmd.Env = e.env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := -1
	if err == nil {
		exitCode = 0
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		// Return nil if the exit code is what we expect (handled by caller).
		// For other errors (e.g., binary not found), return the error.
		if exitCode == -1 {
			return -1, stdout.String(), stderr.String(), err
		}
	}

	return exitCode, stdout.String(), stderr.String(), nil
}

// SplitCommand splits a command string into tokens, respecting quotes,
// without shell interpretation or subshell invocation (RFC-002 S6).
func SplitCommand(s string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
		} else if inQuote && c == quoteChar {
			inQuote = false
		} else if !inQuote && c == ' ' {
			if t := strings.TrimSpace(current.String()); t != "" {
				tokens = append(tokens, t)
				current.Reset()
			}
		} else {
			current.WriteByte(c)
		}
	}
	if t := strings.TrimSpace(current.String()); t != "" {
		tokens = append(tokens, t)
	}
	return tokens, nil
}

// splitShell delegates to SplitCommand for backwards compatibility.
func splitShell(s string) ([]string, error) {
	return SplitCommand(s)
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

func truncateOutput(s string) string {
	const maxLen = 4096
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}

// EvaluateAll runs a list of assertions and returns all results.
func (e *Evaluator) EvaluateAll(ctx context.Context, assertions []*Assertion) []*AssertionResult {
	results := make([]*AssertionResult, 0, len(assertions))
	for i, a := range assertions {
		results = append(results, e.Evaluate(ctx, a, i))
	}
	return results
}

// Summary summarises a list of results.
func Summary(results []*AssertionResult) (allPass bool, failed, errors int) {
	allPass = true
	for _, r := range results {
		switch r.Result {
		case ResultPass:
			// continue
		case ResultFail:
			allPass = false
			failed++
		case ResultError:
			allPass = false
			errors++
		}
	}
	return
}

// ToJSON serializes a list of results to JSON.
func ToJSON(results []*AssertionResult) ([]byte, error) {
	return json.MarshalIndent(results, "", "  ")
}
