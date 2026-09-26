package settlement

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AssertionType defines the closed set of settlement assertions.
type AssertionType string

const (
	// AssertionFileModified asserts a file was modified.
	AssertionFileModified AssertionType = "file_modified"
	// AssertionFileHashEquals asserts a file's SHA-256 hash equals an expected value.
	AssertionFileHashEquals AssertionType = "file_hash_equals"
	// AssertionCommandExitCode asserts a command exits with an expected code.
	AssertionCommandExitCode AssertionType = "command_exit_code"
	// AssertionCommandOutputContains asserts a command's output contains a substring.
	AssertionCommandOutputContains AssertionType = "command_output_contains"
	// AssertionGitCleanWorktree asserts the git worktree is clean.
	AssertionGitCleanWorktree AssertionType = "git_clean_worktree"
	// AssertionPortAvailable asserts a TCP port is available (not in use).
	AssertionPortAvailable AssertionType = "port_available"
	// AssertionNoRegression asserts a command's exit code is 0 (no regressions).
	AssertionNoRegression AssertionType = "no_regression"
)

// AllAssertionTypes is the closed set of valid assertion types.
var AllAssertionTypes = []AssertionType{
	AssertionFileModified,
	AssertionFileHashEquals,
	AssertionCommandExitCode,
	AssertionCommandOutputContains,
	AssertionGitCleanWorktree,
	AssertionPortAvailable,
	AssertionNoRegression,
}

// Assertion defines a single settlement assertion with typed parameters.
type Assertion struct {
	ID      string                 `json:"id"`
	Type    AssertionType          `json:"type"`
	Params  map[string]any         `json:"params"`
	Timeout time.Duration          `json:"timeout,omitempty"`
}

// Validate checks that the assertion type is known and required params are present.
func (a *Assertion) Validate() error {
	valid := false
	for _, t := range AllAssertionTypes {
		if a.Type == t {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("unknown assertion type: %q", a.Type)
	}

	switch a.Type {
	case AssertionFileModified:
		if a.Params["path"] == nil || a.Params["path"] == "" {
			return errors.New("file_modified requires param: path")
		}
	case AssertionFileHashEquals:
		if a.Params["path"] == nil || a.Params["path"] == "" {
			return errors.New("file_hash_equals requires param: path")
		}
		if a.Params["expected_hash"] == nil || a.Params["expected_hash"] == "" {
			return errors.New("file_hash_equals requires param: expected_hash")
		}
		// Validate hex format.
		h := fmt.Sprintf("%v", a.Params["expected_hash"])
		if len(h) != 64 || !isHex(h) {
			return errors.New("file_hash_equals: expected_hash must be a 64-char hex string (SHA-256)")
		}
	case AssertionCommandExitCode:
		if a.Params["command"] == nil || a.Params["command"] == "" {
			return errors.New("command_exit_code requires param: command")
		}
		if a.Params["expected_code"] == nil {
			return errors.New("command_exit_code requires param: expected_code")
		}
	case AssertionCommandOutputContains:
		if a.Params["command"] == nil || a.Params["command"] == "" {
			return errors.New("command_output_contains requires param: command")
		}
		if a.Params["contains"] == nil || a.Params["contains"] == "" {
			return errors.New("command_output_contains requires param: contains")
		}
	case AssertionGitCleanWorktree:
		// No required params; cwd is optional.
	case AssertionPortAvailable:
		if a.Params["port"] == nil {
			return errors.New("port_available requires param: port")
		}
	case AssertionNoRegression:
		if a.Params["command"] == nil || a.Params["command"] == "" {
			return errors.New("no_regression requires param: command")
		}
	}

	return nil
}

// isHex reports whether s is a valid lowercase hex string.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ParseAssertion parses a compact assertion from a human-readable string.
// Format: "<id>:<type>:<key>=<value>,..."
// Example: "tests_passing:command_exit_code:command=go test ./...,expected_code=0"
func ParseAssertion(s string) (*Assertion, error) {
	colonIdx := strings.Index(s, ":")
	if colonIdx <= 0 {
		return nil, errors.New("assertion must start with 'id:type'")
	}
	id := strings.TrimSpace(s[:colonIdx])
	if id == "" {
		return nil, errors.New("assertion id cannot be empty")
	}

	rest := strings.TrimSpace(s[colonIdx+1:])
	colonIdx2 := strings.Index(rest, ":")
	if colonIdx2 <= 0 {
		return nil, errors.New("assertion must have 'id:type:'")
	}
	typ := strings.TrimSpace(rest[:colonIdx2])
	paramsStr := strings.TrimSpace(rest[colonIdx2+1:])

	assertionType := AssertionType(typ)
	valid := false
	for _, t := range AllAssertionTypes {
		if t == assertionType {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("unknown assertion type: %q", typ)
	}

	params, err := parseParams(paramsStr)
	if err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}

	a := &Assertion{ID: id, Type: assertionType, Params: params}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("validate assertion: %w", err)
	}
	return a, nil
}

// parseParams parses "key=value,key2=value2" into a map.
// Commas and equals signs inside quoted strings are not delimiters.
func parseParams(s string) (map[string]any, error) {
	params := make(map[string]any)
	if strings.TrimSpace(s) == "" {
		return params, nil
	}

	// Simple tokenizer: find key=value pairs, respecting simple quoted strings.
	var pairs []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
			current.WriteByte(c)
		} else if inQuote && c == quoteChar {
			inQuote = false
			current.WriteByte(c)
		} else if !inQuote && c == ',' {
			pairs = append(pairs, strings.TrimSpace(current.String()))
			current.Reset()
		} else {
			current.WriteByte(c)
		}
	}
	if tail := strings.TrimSpace(current.String()); tail != "" {
		pairs = append(pairs, tail)
	}

	for _, pair := range pairs {
		eqIdx := -1
		inQuote := false
		quoteChar := byte(0)
		for i := 0; i < len(pair); i++ {
			c := pair[i]
			if !inQuote && (c == '"' || c == '\'') {
				inQuote = true
				quoteChar = c
			} else if inQuote && c == quoteChar {
				inQuote = false
			} else if !inQuote && c == '=' {
				eqIdx = i
				break
			}
		}
		if eqIdx <= 0 {
			return nil, fmt.Errorf("invalid param pair: %q", pair)
		}
		key := strings.TrimSpace(pair[:eqIdx])
		value := strings.TrimSpace(pair[eqIdx+1:])
		if key == "" {
			return nil, errors.New("param key cannot be empty")
		}
		// Strip quotes from value.
		value = stripQuotes(value)
		params[key] = value
	}

	return params, nil
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
