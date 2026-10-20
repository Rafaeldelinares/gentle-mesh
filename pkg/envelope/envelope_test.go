package envelope

import (
	"encoding/json"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────

func validEnvelope() *CognitiveTaskEnvelope {
	return &CognitiveTaskEnvelope{
		EnvelopeID:       "0192de5f-7c00-8000-b000-000000000001",
		EmitterAgentID:   "agent-a",
		ExecutorAgentID:  "agent-b",
		Territory: Territory{
			Repository:   "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/auth",
			WorkspacePath: "/srv/workspace/mesh",
		},
		Preconditions: []Precondition{
			{Type: PreconditionGitCleanWorktree, Params: map[string]string{}},
			{Type: PreconditionToolAvailable, Params: map[string]string{"tool": "go", "min_version": "1.22"}},
		},
		Assertions: []Assertion{
			{
				ID:   "go_tests_passing",
				Type: AssertionCommandExitCode,
				Params: AssertionParams{
					Command:          "go test ./pkg/auth/... -count=1",
					ExpectedExitCode:  0,
					WorkingDir:        "",
					TimeoutSeconds:    120,
				},
			},
			{
				ID:   "go_build_clean",
				Type: AssertionCommandExitCode,
				Params: AssertionParams{
					Command:          "go build ./...",
					ExpectedExitCode:  0,
					WorkingDir:        "",
				},
			},
		},
		TimeoutSeconds:   300,
		MaxRemediations: 2,
		NoSubdelegation: true,
		CreatedAt:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ProtocolVersion: CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
	}
}

// ─────────────────────────────────────────────────────────────────
// Validation tests
// ─────────────────────────────────────────────────────────────────

func TestValidate_ValidEnvelope(t *testing.T) {
	env := validEnvelope()
	if err := Validate(env); err != nil {
		t.Fatalf("Validate(validEnvelope()) error = %v, want nil", err)
	}
}

func TestValidate_NilEnvelope(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("Validate(nil) expected error, got nil")
	}
}

func TestValidate_EmptyEnvelopeID(t *testing.T) {
	env := validEnvelope()
	env.EnvelopeID = ""
	if err := Validate(env); err != ErrInvalidEnvelopeID {
		t.Errorf("Validate(empty envelope_id) = %v, want %v", err, ErrInvalidEnvelopeID)
	}
}

func TestValidate_EmptyEmitter(t *testing.T) {
	env := validEnvelope()
	env.EmitterAgentID = ""
	if err := Validate(env); err != ErrInvalidEmitter {
		t.Errorf("Validate(empty emitter) = %v, want %v", err, ErrInvalidEmitter)
	}
}

func TestValidate_EmptyExecutor(t *testing.T) {
	env := validEnvelope()
	env.ExecutorAgentID = ""
	if err := Validate(env); err != ErrInvalidExecutor {
		t.Errorf("Validate(empty executor) = %v, want %v", err, ErrInvalidExecutor)
	}
}

func TestValidate_SameAgent(t *testing.T) {
	env := validEnvelope()
	env.ExecutorAgentID = env.EmitterAgentID
	if err := Validate(env); err != ErrSameAgent {
		t.Errorf("Validate(same agent) = %v, want %v", err, ErrSameAgent)
	}
}

func TestValidate_NoAssertions(t *testing.T) {
	env := validEnvelope()
	env.Assertions = nil
	if err := Validate(env); err != ErrNoAssertions {
		t.Errorf("Validate(no assertions) = %v, want %v", err, ErrNoAssertions)
	}
}

func TestValidate_DuplicateAssertionID(t *testing.T) {
	env := validEnvelope()
	env.Assertions = append(env.Assertions, env.Assertions[0]) // duplicate ID
	if err := Validate(env); err == nil {
		t.Error("Validate(duplicate assertion id) expected error, got nil")
	}
}

func TestValidate_InvalidTimeout(t *testing.T) {
	env := validEnvelope()
	env.TimeoutSeconds = 0
	if err := Validate(env); err != ErrNegativeTimeout {
		t.Errorf("Validate(timeout=0) = %v, want %v", err, ErrNegativeTimeout)
	}
}

func TestValidate_NegativeRemediations(t *testing.T) {
	env := validEnvelope()
	env.MaxRemediations = -1
	if err := Validate(env); err != ErrNegativeRemediations {
		t.Errorf("Validate(max_remediations=-1) = %v, want %v", err, ErrNegativeRemediations)
	}
}

func TestValidate_InvalidRepository(t *testing.T) {
	tests := []struct {
		name  string
		repo  string
	}{
		{"empty", ""},
		{"only host", "github.com"},
		{"double slash", "github.com//org/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvelope()
			env.Territory.Repository = tt.repo
			if err := Validate(env); err == nil {
				t.Errorf("Validate(repo=%q) expected error, got nil", tt.repo)
			}
		})
	}
}

func TestValidate_EmptyBranch(t *testing.T) {
	env := validEnvelope()
	env.Territory.Branch = ""
	if err := Validate(env); err == nil {
		t.Error("Validate(empty branch) expected error, got nil")
	}
}

func TestValidate_RelativeWorkspacePath(t *testing.T) {
	env := validEnvelope()
	env.Territory.WorkspacePath = "relative/path"
	if err := Validate(env); err == nil {
		t.Error("Validate(relative workspace_path) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// Assertion validation tests
// ─────────────────────────────────────────────────────────────────

func TestValidateAssertion_FileModified(t *testing.T) {
	a := &Assertion{
		ID:   "readme_updated",
		Type: AssertionFileModified,
		Params: AssertionParams{
			FilePath:       "README.md",
			ExpectedSHA256:  "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", // SHA-256 of "test"
		},
	}
	if err := ValidateAssertion(a); err != nil {
		t.Errorf("ValidateAssertion(file_modified) error = %v, want nil", err)
	}
}

func TestValidateAssertion_FileModified_MissingFields(t *testing.T) {
	tests := []struct {
		name   string
		assert Assertion
	}{
		{
			name: "missing file_path",
			assert: Assertion{
				ID:   "test",
				Type: AssertionFileModified,
				Params: AssertionParams{
					ExpectedSHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", // SHA-256 of "test"
				},
			},
		},
		{
			name: "missing sha256",
			assert: Assertion{
				ID:   "test",
				Type: AssertionFileModified,
				Params: AssertionParams{
					FilePath: "README.md",
				},
			},
		},
		{
			name: "invalid sha256",
			assert: Assertion{
				ID:   "test",
				Type: AssertionFileModified,
				Params: AssertionParams{
					FilePath:       "README.md",
					ExpectedSHA256: "not-a-sha256",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAssertion(&tt.assert); err == nil {
				t.Errorf("ValidateAssertion() expected error, got nil")
			}
		})
	}
}

func TestValidateAssertion_CommandExitCode(t *testing.T) {
	a := &Assertion{
		ID:   "build",
		Type: AssertionCommandExitCode,
		Params: AssertionParams{
			Command:          "go build ./...",
			ExpectedExitCode: 0,
		},
	}
	if err := ValidateAssertion(a); err != nil {
		t.Errorf("ValidateAssertion(command_exit_code) error = %v, want nil", err)
	}
}

func TestValidateAssertion_CommandOutputContains_Regex(t *testing.T) {
	a := &Assertion{
		ID:   "log_check",
		Type: AssertionCommandOutputContains,
		Params: AssertionParams{
			Command:          "cat /var/log/app.log",
			ContainsPattern:  `error|fatal|panic`,
			ContainsRegex:    true,
			ExpectedExitCode: 0,
		},
	}
	if err := ValidateAssertion(a); err != nil {
		t.Errorf("ValidateAssertion(command_output_contains, regex) error = %v, want nil", err)
	}
}

func TestValidateAssertion_CommandOutputContains_InvalidRegex(t *testing.T) {
	a := &Assertion{
		ID:   "log_check",
		Type: AssertionCommandOutputContains,
		Params: AssertionParams{
			Command:          "cat /var/log/app.log",
			ContainsPattern:  `[invalid(`,
			ContainsRegex:    true,
		},
	}
	if err := ValidateAssertion(a); err == nil {
		t.Error("ValidateAssertion(invalid regex) expected error, got nil")
	}
}

func TestValidateAssertion_InvalidPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too high", 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Assertion{
				ID:   "port_check",
				Type: AssertionPortAvailable,
				Params: AssertionParams{
					Port: tt.port,
				},
			}
			if err := ValidateAssertion(a); err == nil {
				t.Errorf("ValidateAssertion(port=%d) expected error, got nil", tt.port)
			}
		})
	}
}

func TestValidateAssertion_UnknownType(t *testing.T) {
	a := &Assertion{
		ID:   "unknown",
		Type: "made_up_type",
	}
	if err := ValidateAssertion(a); err == nil {
		t.Error("ValidateAssertion(unknown type) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// Precondition validation tests
// ─────────────────────────────────────────────────────────────────

func TestValidatePrecondition_Valid(t *testing.T) {
	tests := []Precondition{
		{Type: PreconditionGitCleanWorktree, Params: map[string]string{}},
		{Type: PreconditionToolAvailable, Params: map[string]string{"tool": "go"}},
		{Type: PreconditionCommandExitCode, Params: map[string]string{"command": "go version"}},
	}

	for _, p := range tests {
		if err := ValidatePrecondition(&p); err != nil {
			t.Errorf("ValidatePrecondition(%s) error = %v, want nil", p.Type, err)
		}
	}
}

func TestValidatePrecondition_ToolAvailable_MissingTool(t *testing.T) {
	p := Precondition{
		Type:   PreconditionToolAvailable,
		Params: map[string]string{},
	}
	if err := ValidatePrecondition(&p); err == nil {
		t.Error("ValidatePrecondition(missing tool) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// JCS Hash computation tests
// ─────────────────────────────────────────────────────────────────

func TestComputeEnvelopeHash_Valid(t *testing.T) {
	env := validEnvelope()
	hash, err := ComputeEnvelopeHash(env)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash() error = %v, want nil", err)
	}

	// SHA-256 hex is always 64 characters.
	if len(hash) != 64 {
		t.Errorf("Hash length = %d, want 64", len(hash))
	}

	// Hashing the same envelope twice must produce the same result.
	hash2, err := ComputeEnvelopeHash(env)
	if err != nil {
		t.Fatalf("ComputeEnvelopeHash() second call error = %v", err)
	}
	if hash != hash2 {
		t.Errorf("ComputeEnvelopeHash() not deterministic: %q != %q", hash, hash2)
	}
}

func TestComputeEnvelopeHash_IndependentOfFieldOrder(t *testing.T) {
	// Two logically equivalent envelopes with different field ordering
	// must produce the same hash.
	env1 := validEnvelope()
	env2 := validEnvelope()

	// Marshal both to JSON, then unmarshal to maps to scramble field order.
	data1, _ := json.Marshal(env1)
	data2, _ := json.Marshal(env2)

	var m1, m2 map[string]interface{}
	json.Unmarshal(data1, &m1)
	json.Unmarshal(data2, &m2)

	// Re-marshal in different order.
	data1, _ = json.Marshal(m1)
	data2, _ = json.Marshal(m2)

	json.Unmarshal(data1, env1)
	json.Unmarshal(data2, env2)

	hash1, _ := ComputeEnvelopeHash(env1)
	hash2, _ := ComputeEnvelopeHash(env2)

	if hash1 != hash2 {
		t.Errorf("JCS hash depends on field order: %q != %q", hash1, hash2)
	}
}

func TestComputeEnvelopeHash_ChangesWithContent(t *testing.T) {
	env := validEnvelope()
	hash1, _ := ComputeEnvelopeHash(env)

	// Change the timeout.
	env.TimeoutSeconds = 999
	hash2, _ := ComputeEnvelopeHash(env)

	if hash1 == hash2 {
		t.Error("JCS hash did not change after modifying content")
	}
}

func TestComputeEnvelopeHash_InvalidEnvelope(t *testing.T) {
	env := validEnvelope()
	env.EnvelopeID = "" // make it invalid
	_, err := ComputeEnvelopeHash(env)
	if err == nil {
		t.Error("ComputeEnvelopeHash(invalid envelope) expected error, got nil")
	}
}

func TestComputeEnvelopeHash_TerritoryNormalization(t *testing.T) {
	// Two envelopes with the same territory but different URI formatting
	// must canonicalize to the same hash.
	env1 := validEnvelope()
	env2 := validEnvelope()

	env1.Territory.Repository = "github.com/gentleman-programming/gentle-mesh"
	env2.Territory.Repository = "github.com/gentleman-programming/gentle-mesh/" // trailing slash

	hash1, _ := ComputeEnvelopeHash(env1)
	hash2, _ := ComputeEnvelopeHash(env2)

	if hash1 != hash2 {
		t.Errorf("JCS hash differs with trailing slash: %q != %q", hash1, hash2)
	}
}

// ─────────────────────────────────────────────────────────────────
// ContractStatus tests
// ─────────────────────────────────────────────────────────────────

func TestContractStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   ContractStatus
		terminal bool
	}{
		{ContractStatusProposed, false},
		{ContractStatusAccepted, false},
		{ContractStatusExecuting, false},
		{ContractStatusSettling, false},
		{ContractStatusRemediating, false},
		{ContractStatusSettled, true},
		{ContractStatusAcceptedFinal, true},
		{ContractStatusRejected, true},
		{ContractStatusExpired, true},
		{ContractStatusSettlementFailed, true},
		{ContractStatusDisputed, false},
		{ContractStatusAbandoned, true},
		{ContractStatusSettlementTimeout, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal(%s) = %v, want %v", tt.status, got, tt.terminal)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────

func TestResolvePath(t *testing.T) {
	tests := []struct {
		workspace string
		file      string
		want      string
	}{
		{"/srv/mesh", "pkg/main.go", "/srv/mesh/pkg/main.go"},
		{"/srv/mesh", "/absolute/path", "/absolute/path"},
	}

	for _, tt := range tests {
		got := ResolvePath(tt.workspace, tt.file)
		if got != tt.want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", tt.workspace, tt.file, got, tt.want)
		}
	}
}

func TestFileExists(t *testing.T) {
	// Should not panic on non-existent file.
	if FileExists("/nonexistent/path/to/file.txt") {
		t.Error("FileExists(nonexistent) = true, want false")
	}
}

func TestIsSHA256(t *testing.T) {
	// SHA-256 of "test".
	valid := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	invalid := []string{
		"short",
		"not-hex-xyz",
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a0", // 63 chars
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a0g", // 'g' is invalid
	}

	if !isSHA256(valid) {
		t.Errorf("isSHA256(%q) = false, want true", valid)
	}
	for _, s := range invalid {
		if isSHA256(s) {
			t.Errorf("isSHA256(%q) = true, want false", s)
		}
	}
}

func TestIsValidRepoURL(t *testing.T) {
	valid := []string{
		"github.com/org/repo",
		"gitlab.com/group/project",
		"https://github.com/org/repo",
		"git@github.com:org/repo.git",
	}
	invalid := []string{
		"",
		"github.com",
		"/only/path",
	}

	for _, s := range valid {
		if !isValidRepoURL(s) {
			t.Errorf("isValidRepoURL(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isValidRepoURL(s) {
			t.Errorf("isValidRepoURL(%q) = true, want false", s)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// ContractStatus String
// ─────────────────────────────────────────────────────────────────

func TestContractStatusString(t *testing.T) {
	status := ContractStatusProposed
	if status.String() != "PROPOSED" {
		t.Errorf("ContractStatus.String() = %q, want %q", status.String(), "PROPOSED")
	}
}
