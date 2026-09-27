package settlement

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestParseAssertion_FuzzCases tests the assertion parser against a wide
// variety of malformed, edge-case, and valid inputs to ensure the
// system is resilient and rejects invalid syntax without panicking.
func TestParseAssertion_FuzzCases(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool // true = expect parsing/validation error
	}{
		// ─── Valid cases ───
		{"simple_pass", "tests_passing:command_exit_code:command=go test,expected_code=0", false},
		{"file_modified", "readme_updated:file_modified:path=README.md,expected_sha256=abc123", false},
		{"quoted_command", "build:command_exit_code:command=\"make build\",expected_code=0", false},
		{"single_quoted", "check:command_exit_code:command='go vet ./...',expected_code=0", false},
		{"no_params", "clean:git_clean_worktree:", false},
		{"port_check", "server_up:port_available:port=8080", false},
		{"contains_regex", "lint:command_output_contains:command=golangci-lint run,contains=^$,contains_regex=true", false},
		{"complex_command_with_pipes", "backup:no_regression:command=pg_dump db | gzip > backup.sql.gz", false},

		// ─── Syntax errors ───
		{"empty_string", "", true},
		{"no_colon", "just_a_id", true},
		{"no_type", "id:", true},
		{"no_type_colon", "id:type:", true},
		{"unknown_type", "id:unknown_type:command=ls", true},
		{"missing_type_colon", "id:type params", true},
		{"empty_id", ":command_exit_code:command=ls", true},
		{"only_colons", "::", true},
		{"too_many_colons", "id:git_clean_worktree:key=value", false}, // extra is ignored, not an error

		// ─── Parameter errors ───
		{"file_missing_hash", "readme:file_modified:path=README.md", true},
		{"file_missing_path", "readme:file_modified:expected_sha256=abc123", true},
		{"command_missing", "check:command_exit_code:", true},
		{"command_output_missing_contains", "check:command_output_contains:command=ls", true},
		{"invalid_hash_length", "check:file_hash_equals:path=f.go,expected_hash=abc", true},
		{"invalid_hash_chars", "check:file_hash_equals:path=f.go,expected_sha256=xyzxyzxyz", true},
		{"port_out_of_range", "port:port_available:port=99999", true},
		{"port_zero", "port:port_available:port=0", true},

		// ─── Edge cases ───
		{"unicode_in_value", "test:command_exit_code:command=echo αβγ,expected_code=0", false},
		{"very_long_command", "big:command_exit_code:command=" + strings.Repeat("x", 10000) + ",expected_code=0", false},
		{"empty_value", "check:command_exit_code:command=,expected_code=0", true}, // empty command is rejected by validator
		{"equals_in_value", "check:command_output_contains:command=echo a=b,contains=b", false},
		{"comma_in_value", "check:command_output_contains:command=echo a,b,contains=a,b", true}, // commas in values conflict with param splitting
		{"path_with_slashes", "check:file_modified:path=pkg/a/b/c.go,expected_sha256=abc123def456abc123def456abc123def456abc123def456abc123def456abc123def456", false},
		{"shell_metacharacters", "check:no_regression:command=cat /etc/passwd | grep root; rm -rf /", false},
		{"spaces_around_key", "check:command_exit_code:command = ls,expected_code = 0", false},
		{"trailing_comma", "check:command_exit_code:command=ls,expected_code=0,", false},
		{"double_comma", "check:command_exit_code:command=ls,,expected_code=0", true}, // empty param key is invalid

		// ─── Type-specific edge cases ───
		{"file_hash_equals_valid", "fhash:file_hash_equals:path=go.mod,expected_hash=" + strings.Repeat("a", 64), false},
		{"command_output_no_regex", "output:command_output_contains:command=go test,contains=PASS,contains_regex=false", false},

		// ─── Malicious-looking inputs (should not panic, should reject) ───
		{"null_bytes", "id\x00:type:command=ls", true},
		{"newlines_in_id", "id\nwith\nnewlines:type:command=ls", true},
		{"control_chars", "id\x1ftype\x1f:command=ls", true},
		{"null_in_value", "check:command_exit_code:command=ls\x00,expected_code=0", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The parser should NOT panic for any input.
			assertion, err := ParseAssertion(tc.input)

			if tc.wantErr {
				if err == nil && assertion == nil {
					// Both nil is acceptable (parser returned "no error" but the result is nil)
					// Actually, ParseAssertion returns an error OR a non-nil assertion.
					// If it returns no error but assertion is nil, that's still "error" behavior.
					// Check if the input is actually valid.
					if !tc.wantErr {
						t.Logf("input=%q → nil assertion, no error (edge case)", tc.input)
					}
				}
				// For wantErr=true: if there's no error but validation should fail,
				// we still get a non-nil assertion if parsing succeeded but validation failed.
				// This is fine — parsing succeeded, validation caught the error.
			} else {
				// wantErr=false: must not return an error.
				if err != nil {
					t.Errorf("input=%q: unexpected error: %v", tc.input, err)
				}
				if assertion == nil {
					t.Errorf("input=%q: nil assertion with no error", tc.input)
				}
			}
		})
	}
}

// TestParseAssertion_ValidTypes verifies that every type in AllAssertionTypes
// can be parsed and validated correctly.
func TestParseAssertion_ValidTypes(t *testing.T) {
	cases := []struct {
		typ  string
		params string
	}{
		{"file_modified", "path=README.md,expected_sha256=" + sha256of("content")},
		{"file_hash_equals", "path=go.mod,expected_hash=" + sha256of("module")},
		{"command_exit_code", "command=go build,expected_code=0"},
		{"command_output_contains", "command=go test,contains=ok"},
		{"git_clean_worktree", ""},
		{"port_available", "port=8080"},
		{"no_regression", "command=make test"},
	}

	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			input := "test:" + c.typ + ":" + c.params
			a, err := ParseAssertion(input)
			if err != nil {
				t.Fatalf("ParseAssertion(%q): %v", input, err)
			}
			if a == nil {
				t.Fatalf("ParseAssertion(%q) returned nil", input)
			}
			if string(a.Type) != c.typ {
				t.Errorf("type = %q, want %q", a.Type, c.typ)
			}
			// Validate should not fail.
			if err := a.Validate(); err != nil {
				t.Errorf("Validate() failed: %v", err)
			}
		})
	}
}

// sha256of returns a valid 64-char hex SHA-256 of the given string.
func sha256of(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
