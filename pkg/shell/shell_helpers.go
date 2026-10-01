package shell

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/gentleman-programming/gentle-mesh/pkg/envelope"
	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
	"github.com/gentleman-programming/gentle-mesh/pkg/settlement"
)

// ConvertAssertions converts envelope.Assertion (typed params) to settlement.Assertion (map params).
// The settlement engine uses map[string]any for maximum flexibility.
func ConvertAssertions(envAssertions []envelope.Assertion) []*settlement.Assertion {
	result := make([]*settlement.Assertion, len(envAssertions))
	for i, ea := range envAssertions {
		result[i] = &settlement.Assertion{
			ID:     ea.ID,
			Type:   settlement.AssertionType(ea.Type), // both are string types
			Params: convertParams(ea.Params),
		}
	}
	return result
}

// convertParams flattens envelope.AssertionParams (typed struct) into map[string]any.
func convertParams(p envelope.AssertionParams) map[string]any {
	m := make(map[string]any)
	if p.Description != "" {
		m["description"] = p.Description
	}
	if p.FilePath != "" {
		m["path"] = p.FilePath
	}
	if p.ExpectedSHA256 != "" {
		m["expected_hash"] = p.ExpectedSHA256 // settlement uses "expected_hash"
	}
	if p.Command != "" {
		m["command"] = p.Command
	}
	m["expected_code"] = p.ExpectedExitCode // always include (zero is valid)
	if p.ContainsPattern != "" {
		m["contains"] = p.ContainsPattern
	}
	if p.ContainsRegex {
		m["regex"] = true
	}
	if p.WorkingDir != "" {
		m["cwd"] = p.WorkingDir
	}
	if len(p.Env) > 0 {
		m["env"] = p.Env
	}
	return m
}

// ConvertResults converts settlement.AssertionResult to receipt.AssertionResult.
func ConvertResults(sr []*settlement.AssertionResult) []receipt.AssertionResult {
	result := make([]receipt.AssertionResult, len(sr))
	for i, s := range sr {
		// Map settlement.Result (PASS/FAIL/ERROR) to receipt.Result (PASS/FAIL)
		var rcptResult receipt.Result
		switch s.Result {
		case settlement.ResultPass:
			rcptResult = receipt.ResultPass
		default:
			rcptResult = receipt.ResultFail // FAIL or ERROR → FAIL
		}

		result[i] = receipt.AssertionResult{
			AssertionIndex: s.AssertionIndex,
			AssertionID:   s.AssertionID,
			AssertionType: string(s.AssertionType),
			Result:       rcptResult,
			Evidence: receipt.AssertionEvidence{
				Command:      s.Evidence.Command,
				ExitCode:    s.Evidence.ExitCode,
				StdoutHash:  sha256Hex(s.Evidence.Stdout),
				StderrHash:  sha256Hex(s.Evidence.Stderr),
				// File evidence: path is in Params["path"], not in Evidence
				ExpectedSHA256:  s.Evidence.ExpectedHash,
				ActualSHA256:    s.Evidence.ActualHash,
				FileWasModified: s.Evidence.FileExists,
				// Git evidence.
				GitStatus: s.Evidence.GitStatus,
				// Port evidence.
				PortWasFree: s.Evidence.PortAvailable,
			},
			Message: s.Message,
		}
	}
	return result
}

// sha256Hex computes the SHA-256 hash of a string and returns it as a hex string.
func sha256Hex(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
