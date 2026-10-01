package receipt

import (
	"testing"
	"time"
)

func TestVerdict_IsTerminal(t *testing.T) {
	tests := []struct {
		verdict  Verdict
		terminal bool
	}{
		{VerdictSettledClean, true},
		{VerdictRemediated, true},
		{VerdictFailed, true},
		{VerdictDisputed, false},
		{VerdictTimeout, true},
		{Verdict(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.verdict), func(t *testing.T) {
			if got := tt.verdict.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal(%s) = %v, want %v", tt.verdict, got, tt.terminal)
			}
		})
	}
}

func TestVerdict_IsSuccess(t *testing.T) {
	tests := []struct {
		verdict Verdict
		success bool
	}{
		{VerdictSettledClean, true},
		{VerdictRemediated, true},
		{VerdictFailed, false},
		{VerdictDisputed, false},
		{VerdictTimeout, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.verdict), func(t *testing.T) {
			if got := tt.verdict.IsSuccess(); got != tt.success {
				t.Errorf("IsSuccess(%s) = %v, want %v", tt.verdict, got, tt.success)
			}
		})
	}
}

func TestVerdict_String(t *testing.T) {
	v := VerdictSettledClean
	if v.String() != "SETTLED_CLEAN" {
		t.Errorf("Verdict.String() = %q, want %q", v.String(), "SETTLED_CLEAN")
	}
}

func TestAcceptance_IsValid(t *testing.T) {
	tests := []struct {
		a     Acceptance
		valid bool
	}{
		{AcceptanceAccepted, true},
		{AcceptanceDisputed, true},
		{Acceptance("UNKNOWN"), false},
		{Acceptance(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.a), func(t *testing.T) {
			if got := tt.a.IsValid(); got != tt.valid {
				t.Errorf("IsValid(%s) = %v, want %v", tt.a, got, tt.valid)
			}
		})
	}
}

func TestAcceptance_String(t *testing.T) {
	a := AcceptanceAccepted
	if a.String() != "ACCEPTED" {
		t.Errorf("Acceptance.String() = %q, want %q", a.String(), "ACCEPTED")
	}
}

func TestResult_String(t *testing.T) {
	r := ResultPass
	if r.String() != "PASS" {
		t.Errorf("Result.String() = %q, want %q", r.String(), "PASS")
	}
}

func TestRemediationResult_String(t *testing.T) {
	r := RemediationSuccess
	if r.String() != "SUCCESS" {
		t.Errorf("RemediationResult.String() = %q, want %q", r.String(), "SUCCESS")
	}
}

func TestReceiptStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   ReceiptStatus
		terminal bool
	}{
		{ReceiptStatusEmitted, false},
		{ReceiptStatusAccepted, true},
		{ReceiptStatusDisputed, false},
		{ReceiptStatusResolved, true},
		{ReceiptStatusStale, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal(%s) = %v, want %v", tt.status, got, tt.terminal)
			}
		})
	}
}

func TestReceiptStatus_String(t *testing.T) {
	s := ReceiptStatusEmitted
	if s.String() != "EMITTED" {
		t.Errorf("ReceiptStatus.String() = %q, want %q", s.String(), "EMITTED")
	}
}

// ─────────────────────────────────────────────────────────────────
// Fixture helpers
// ─────────────────────────────────────────────────────────────────

func validReceipt() *SettlementReceipt {
	now := time.Now().UTC()
	return &SettlementReceipt{
		ProtocolVersion: CurrentProtocolVersion,
		MeshID:          "gentle-mesh-dev",
		ReceiptID:       "0192de5f-7c01-8000-b000-000000000002",
		ContractID:      "0192de5f-7c00-8000-b000-000000000001",
		EnvelopeHash:    "abc123def456",
		EmitterAgentID:  "agent-a",
		ExecutorAgentID: "agent-b",
		Territory: Territory{
			Repository:    "github.com/gentleman-programming/gentle-mesh",
			Branch:        "feature/auth",
			WorkspacePath: "/srv/workspace/mesh",
		},
		Verdict: VerdictSettledClean,
		Assertions: []AssertionResult{
			{
				AssertionIndex: 0,
				AssertionID:    "tests_passing",
				AssertionType:  "command_exit_code",
				Result:         ResultPass,
				Evidence: AssertionEvidence{
					Command:   "go test ./pkg/auth/...",
					ExitCode:  0,
					CheckedAt: now,
				},
			},
		},
		ExecutorSignature:   "sig-from-b",
		ExecutorSignedAt:    now,
		PreviousReceiptHash: "previous-sig-hash",
	}
}

func TestSettlementReceipt_Fields(t *testing.T) {
	r := validReceipt()

	if r.ReceiptID == "" {
		t.Error("ReceiptID is empty")
	}
	if r.ContractID == "" {
		t.Error("ContractID is empty")
	}
	if r.EmitterAgentID == "" {
		t.Error("EmitterAgentID is empty")
	}
	if r.ExecutorAgentID == "" {
		t.Error("ExecutorAgentID is empty")
	}
	if len(r.Assertions) == 0 {
		t.Error("Assertions is empty")
	}
}

func TestAssertionResult_Fields(t *testing.T) {
	ar := AssertionResult{
		AssertionIndex: 0,
		AssertionID:    "build_success",
		AssertionType:  "command_exit_code",
		Result:         ResultPass,
		Evidence: AssertionEvidence{
			Command:   "go build ./...",
			ExitCode:  0,
			CheckedAt: time.Now().UTC(),
		},
	}

	if ar.AssertionID != "build_success" {
		t.Errorf("AssertionID = %q, want %q", ar.AssertionID, "build_success")
	}
	if ar.Result != ResultPass {
		t.Errorf("Result = %v, want PASS", ar.Result)
	}
}

func TestTerritory_Fields(t *testing.T) {
	terr := Territory{
		Repository:    "github.com/org/repo",
		Branch:        "main",
		WorkspacePath: "/workspace/repo",
	}

	if terr.Repository != "github.com/org/repo" {
		t.Errorf("Repository = %q, want %q", terr.Repository, "github.com/org/repo")
	}
	if terr.Branch != "main" {
		t.Errorf("Branch = %q, want %q", terr.Branch, "main")
	}
}

func TestRemediationAttempt_Fields(t *testing.T) {
	ra := RemediationAttempt{
		AttemptIndex:           0,
		TriggeredByAssertionID: "tests_passing",
		Action:                 "Ran go mod tidy to resolve dependency issue",
		Command:                "go mod tidy",
		ExitCode:               0,
		AssertionsRechecked:    []string{"tests_passing", "build_clean"},
		Result:                 RemediationSuccess,
		Message:                "Dependencies updated, tests re-run",
		AttemptedAt:            time.Now().UTC(),
	}

	if ra.Result != RemediationSuccess {
		t.Errorf("Result = %v, want SUCCESS", ra.Result)
	}
	if len(ra.AssertionsRechecked) != 2 {
		t.Errorf("AssertionsRechecked len = %d, want 2", len(ra.AssertionsRechecked))
	}
}

func TestVerdict_Constants(t *testing.T) {
	// Verify all constants are distinct.
	constants := []string{
		string(VerdictSettledClean),
		string(VerdictRemediated),
		string(VerdictFailed),
		string(VerdictDisputed),
		string(VerdictTimeout),
	}
	seen := make(map[string]bool)
	for _, c := range constants {
		if seen[c] {
			t.Errorf("Duplicate verdict constant: %s", c)
		}
		seen[c] = true
	}
}

func TestAcceptance_Constants(t *testing.T) {
	if AcceptanceAccepted != "ACCEPTED" {
		t.Errorf("AcceptanceAccepted = %q, want %q", AcceptanceAccepted, "ACCEPTED")
	}
	if AcceptanceDisputed != "DISPUTED" {
		t.Errorf("AcceptanceDisputed = %q, want %q", AcceptanceDisputed, "DISPUTED")
	}
}

func TestReceiptStatus_Constants(t *testing.T) {
	if ReceiptStatusEmitted != "EMITTED" {
		t.Errorf("ReceiptStatusEmitted = %q, want %q", ReceiptStatusEmitted, "EMITTED")
	}
	if ReceiptStatusAccepted != "ACCEPTED" {
		t.Errorf("ReceiptStatusAccepted = %q, want %q", ReceiptStatusAccepted, "ACCEPTED")
	}
	if ReceiptStatusStale != "STALE" {
		t.Errorf("ReceiptStatusStale = %q, want %q", ReceiptStatusStale, "STALE")
	}
}
