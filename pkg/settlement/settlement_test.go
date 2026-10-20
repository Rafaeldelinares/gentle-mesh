package settlement

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────
// Assertion parsing
// ─────────────────────────────────────────────────────────────────

func TestParseAssertion_Valid(t *testing.T) {
	tests := []struct {
		input    string
		wantID   string
		wantType AssertionType
		wantKeys []string
	}{
		{
			"tests_passing:command_exit_code:command=go test ./...,expected_code=0",
			"tests_passing",
			AssertionCommandExitCode,
			[]string{"command", "expected_code"},
		},
		{
			"readme_updated:file_modified:path=README.md",
			"readme_updated",
			AssertionFileModified,
			[]string{"path"},
		},
		{
			"auth_hash:file_hash_equals:path=pkg/auth/auth.go,expected_hash=a948904f2f0f479b8f8564cbf12dae62c683b2a5f1677e1af51a92d4ed30a89f",
			"auth_hash",
			AssertionFileHashEquals,
			[]string{"path", "expected_hash"},
		},
		{
			"output_ok:command_output_contains:command=go test -v,contains=ok",
			"output_ok",
			AssertionCommandOutputContains,
			[]string{"command", "contains"},
		},
		{
			"clean:git_clean_worktree:cwd=/home/user/project",
			"clean",
			AssertionGitCleanWorktree,
			[]string{"cwd"},
		},
		{
			"port_free:port_available:port=8080",
			"port_free",
			AssertionPortAvailable,
			[]string{"port"},
		},
		{
			"no_regression:no_regression:command=make test",
			"no_regression",
			AssertionNoRegression,
			[]string{"command"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			a, err := ParseAssertion(tt.input)
			if err != nil {
				t.Fatalf("ParseAssertion(%q) error: %v", tt.input, err)
			}
			if a.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", a.ID, tt.wantID)
			}
			if a.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", a.Type, tt.wantType)
			}
			for _, k := range tt.wantKeys {
				if _, ok := a.Params[k]; !ok {
					t.Errorf("Params missing key %q", k)
				}
			}
		})
	}
}

func TestParseAssertion_InvalidType(t *testing.T) {
	_, err := ParseAssertion("bad:type:x=y")
	if err == nil {
		t.Error("ParseAssertion with unknown type should fail")
	}
}

func TestParseAssertion_EmptyID(t *testing.T) {
	_, err := ParseAssertion(":command_exit_code:x=y")
	if err == nil {
		t.Error("ParseAssertion with empty ID should fail")
	}
}

func TestParseAssertion_QuotedValues(t *testing.T) {
	a, err := ParseAssertion(`msg:command_output_contains:command="go test -v",contains='PASS'`)
	if err != nil {
		t.Fatalf("ParseAssertion error: %v", err)
	}
	if a.Params["command"] != "go test -v" {
		t.Errorf("command = %q, want %q", a.Params["command"], "go test -v")
	}
	if a.Params["contains"] != "PASS" {
		t.Errorf("contains = %q, want %q", a.Params["contains"], "PASS")
	}
}

func TestParseAssertion_ComplexCommand(t *testing.T) {
	// Command with spaces, special chars, and pipes.
	a, err := ParseAssertion(`tests:command_exit_code:command=go test -v ./pkg/... 2>&1 | grep -q "ok",expected_code=0`)
	if err != nil {
		t.Fatalf("ParseAssertion error: %v", err)
	}
	if a.Params["command"] != `go test -v ./pkg/... 2>&1 | grep -q "ok"` {
		t.Errorf("command = %q", a.Params["command"])
	}
}

// ─────────────────────────────────────────────────────────────────
// Assertion validation
// ─────────────────────────────────────────────────────────────────

func TestAssertion_Validate_AllTypes(t *testing.T) {
	valid := []*Assertion{
		{ID: "a", Type: AssertionFileModified, Params: map[string]any{"path": "foo.txt"}},
		{ID: "b", Type: AssertionFileHashEquals, Params: map[string]any{"path": "foo.txt", "expected_hash": "a948904f2f0f479b8f8564cbf12dae62c683b2a5f1677e1af51a92d4ed30a89f"}},
		{ID: "c", Type: AssertionCommandExitCode, Params: map[string]any{"command": "go test", "expected_code": 0}},
		{ID: "d", Type: AssertionCommandOutputContains, Params: map[string]any{"command": "go test", "contains": "ok"}},
		{ID: "e", Type: AssertionGitCleanWorktree, Params: map[string]any{}},
		{ID: "f", Type: AssertionPortAvailable, Params: map[string]any{"port": 8080}},
		{ID: "g", Type: AssertionNoRegression, Params: map[string]any{"command": "make test"}},
	}

	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Errorf("Assertion %s.Validate() error: %v", a.ID, err)
		}
	}
}

func TestAssertion_Validate_InvalidHash(t *testing.T) {
	a := &Assertion{
		ID:   "bad",
		Type: AssertionFileHashEquals,
		Params: map[string]any{
			"path":          "foo.txt",
			"expected_hash": "not-a-64-char-hex",
		},
	}
	err := a.Validate()
	if err == nil {
		t.Error("Invalid hash should fail validation")
	}
}

func TestAssertion_Validate_MissingParams(t *testing.T) {
	cases := []struct {
		id    string
		a     *Assertion
	}{
		{"file_modified", &Assertion{ID: "a", Type: AssertionFileModified, Params: map[string]any{}}},
		{"command_exit_code", &Assertion{ID: "b", Type: AssertionCommandExitCode, Params: map[string]any{"command": "x"}}},
		{"command_output_contains", &Assertion{ID: "c", Type: AssertionCommandOutputContains, Params: map[string]any{"command": "x"}}},
		{"port_available", &Assertion{ID: "d", Type: AssertionPortAvailable, Params: map[string]any{}}},
		{"no_regression", &Assertion{ID: "e", Type: AssertionNoRegression, Params: map[string]any{}}},
	}

	for _, tt := range cases {
		t.Run(tt.id, func(t *testing.T) {
			err := tt.a.Validate()
			if err == nil {
				t.Errorf("%s should fail validation without required params", tt.id)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────
// Evaluator — file assertions
// ─────────────────────────────────────────────────────────────────

func testDir(t *testing.T) string {
	dir := t.TempDir()
	return dir
}

func TestEvaluator_FileModified_Pass(t *testing.T) {
	dir := testDir(t)
	f := filepath.Join(dir, "modified.txt")
	os.WriteFile(f, []byte("hello"), 0644)

	ev := NewEvaluator(dir)
	a := &Assertion{ID: "m", Type: AssertionFileModified, Params: map[string]any{"path": "modified.txt"}}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
}

func TestEvaluator_FileModified_NotExists(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{ID: "m", Type: AssertionFileModified, Params: map[string]any{"path": "nonexistent.txt"}}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
	if result.Evidence.FileExists {
		t.Error("FileExists should be false")
	}
}

func TestEvaluator_FileHashEquals_Pass(t *testing.T) {
	dir := testDir(t)
	content := []byte("hello world")
	f := filepath.Join(dir, "hello.txt")
	os.WriteFile(f, content, 0644)

	// SHA-256 of "hello world".
	expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "h",
		Type: AssertionFileHashEquals,
		Params: map[string]any{
			"path":          "hello.txt",
			"expected_hash": expectedHash,
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
	if result.Evidence.ActualHash != expectedHash {
		t.Errorf("ActualHash = %s, want %s", result.Evidence.ActualHash, expectedHash)
	}
}

func TestEvaluator_FileHashEquals_Fail(t *testing.T) {
	dir := testDir(t)
	os.WriteFile(filepath.Join(dir, "foo.txt"), []byte("content"), 0644)

	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "h",
		Type: AssertionFileHashEquals,
		Params: map[string]any{
			"path":          "foo.txt",
			"expected_hash": "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
}

// ─────────────────────────────────────────────────────────────────
// Evaluator — command assertions
// ─────────────────────────────────────────────────────────────────

func TestEvaluator_CommandExitCode_Pass(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "c",
		Type: AssertionCommandExitCode,
		Params: map[string]any{
			"command":       "true", // exit 0
			"expected_code": 0,
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
	if result.Evidence.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.Evidence.ExitCode)
	}
}

func TestEvaluator_CommandExitCode_Fail(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "c",
		Type: AssertionCommandExitCode,
		Params: map[string]any{
			"command":       "false", // exit 1
			"expected_code": 0,
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
	if result.Evidence.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.Evidence.ExitCode)
	}
}

func TestEvaluator_CommandOutputContains_Pass(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "o",
		Type: AssertionCommandOutputContains,
		Params: map[string]any{
			"command":  "echo hello world",
			"contains": "hello",
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
	if !contains(result.Evidence.Stdout, "hello") {
		t.Errorf("Stdout = %q, should contain 'hello'", result.Evidence.Stdout)
	}
}

func TestEvaluator_CommandOutputContains_Fail(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "o",
		Type: AssertionCommandOutputContains,
		Params: map[string]any{
			"command":  "echo hello",
			"contains": "goodbye",
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
}

func TestEvaluator_CommandNotFound(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "e",
		Type: AssertionCommandExitCode,
		Params: map[string]any{
			"command":      "nonexistent-binary-xyz",
			"expected_code": 0,
		},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	// Non-existent binary: runCommand returns an error with exitCode -1.
	// evalCommandExitCode treats this as FAIL (command error).
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL (command error)", result.Result)
	}
}

func TestEvaluator_CommandTimeout(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir).WithTimeout(100 * time.Millisecond)
	a := &Assertion{
		ID:   "t",
		Type: AssertionCommandExitCode,
		Params: map[string]any{
			"command":      "sleep 10",
			"expected_code": 0,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := ev.Evaluate(ctx, a, 0)
	if result.Result != ResultError {
		t.Errorf("Result = %v, want ERROR (timeout)", result.Result)
	}
	if !contains(result.Message, "deadline") {
		t.Errorf("Message = %q, want deadline message", result.Message)
	}
}

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		})()
}

// ─────────────────────────────────────────────────────────────────
// Evaluator — git
// ─────────────────────────────────────────────────────────────────

func TestEvaluator_GitCleanWorktree_Clean(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)

	// Init a git repo with no commits (clean).
	runGit(t, []string{"init", dir})
	runGit(t, []string{"-C", dir, "config", "user.email", "test@test.com"})
	runGit(t, []string{"-C", dir, "config", "user.name", "Test"})

	a := &Assertion{ID: "g", Type: AssertionGitCleanWorktree, Params: map[string]any{}}
	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
}

func TestEvaluator_GitCleanWorktree_Dirty(t *testing.T) {
	dir := testDir(t)
	runGit(t, []string{"init", dir})
	runGit(t, []string{"-C", dir, "config", "user.email", "test@test.com"})
	runGit(t, []string{"-C", dir, "config", "user.name", "Test"})
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644)

	ev := NewEvaluator(dir)
	a := &Assertion{ID: "g", Type: AssertionGitCleanWorktree, Params: map[string]any{}}
	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
}

// ─────────────────────────────────────────────────────────────────
// Evaluator — port
// ─────────────────────────────────────────────────────────────────

func TestEvaluator_PortAvailable_Pass(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)

	// Use a random high port that is unlikely to be in use.
	a := &Assertion{
		ID:   "p",
		Type: AssertionPortAvailable,
		Params: map[string]any{"port": 65432},
	}
	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
	if !result.Evidence.PortAvailable {
		t.Error("PortAvailable should be true")
	}
}

func TestEvaluator_PortAvailable_Fail(t *testing.T) {
	dir := testDir(t)

	// Start a listener on a port.
	ln, err := listen("localhost:0")
	if err != nil {
		t.Skipf("cannot bind a port: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)

	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "p",
		Type: AssertionPortAvailable,
		Params: map[string]any{"port": addr.Port},
	}
	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL (port in use)", result.Result)
	}
	if result.Evidence.PortAvailable {
		t.Error("PortAvailable should be false")
	}
}

func listen(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// ─────────────────────────────────────────────────────────────────
// Evaluator — no regression
// ─────────────────────────────────────────────────────────────────

func TestEvaluator_NoRegression_Pass(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	a := &Assertion{
		ID:   "r",
		Type: AssertionNoRegression,
		Params: map[string]any{"command": "true"},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultPass {
		t.Errorf("Result = %v, want PASS: %s", result.Result, result.Message)
	}
}

func TestEvaluator_NoRegression_Fail(t *testing.T) {
	dir := testDir(t)
	ev := NewEvaluator(dir)
	// Use sh -c so we can call the shell builtin "exit 1".
	a := &Assertion{
		ID:   "r",
		Type: AssertionNoRegression,
		Params: map[string]any{"command": "sh -c 'exit 1'"},
	}

	result := ev.Evaluate(context.Background(), a, 0)
	if result.Result != ResultFail {
		t.Errorf("Result = %v, want FAIL", result.Result)
	}
}

// ─────────────────────────────────────────────────────────────────
// EvaluateAll and Summary
// ─────────────────────────────────────────────────────────────────

func TestEvaluateAll(t *testing.T) {
	dir := testDir(t)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644)
	ev := NewEvaluator(dir)

	assertions := []*Assertion{
		{ID: "f", Type: AssertionFileModified, Params: map[string]any{"path": "hello.txt"}},
		{ID: "c", Type: AssertionCommandExitCode, Params: map[string]any{"command": "true", "expected_code": 0}},
		{ID: "b", Type: AssertionCommandExitCode, Params: map[string]any{"command": "false", "expected_code": 0}},
	}

	results := ev.EvaluateAll(context.Background(), assertions)
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}

	if results[0].Result != ResultPass {
		t.Errorf("results[0] = %v, want PASS", results[0].Result)
	}
	if results[1].Result != ResultPass {
		t.Errorf("results[1] = %v, want PASS", results[1].Result)
	}
	if results[2].Result != ResultFail {
		t.Errorf("results[2] = %v, want FAIL", results[2].Result)
	}
}

func TestSummary(t *testing.T) {
	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultFail},
		{Result: ResultPass},
		{Result: ResultError},
	}

	allPass, failed, errors := Summary(results)
	if allPass {
		t.Error("allPass = true, want false")
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if errors != 1 {
		t.Errorf("errors = %d, want 1", errors)
	}
}

func TestSummary_AllPass(t *testing.T) {
	results := []*AssertionResult{
		{Result: ResultPass},
		{Result: ResultPass},
	}

	allPass, failed, errors := Summary(results)
	if !allPass {
		t.Error("allPass = false, want true")
	}
	if failed != 0 || errors != 0 {
		t.Errorf("failed=%d errors=%d, want 0,0", failed, errors)
	}
}

// runGit runs git with args, ignoring errors (for test setup).
func runGit(t *testing.T, args []string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Run() // ignore errors; tests that depend on it will fail
}
