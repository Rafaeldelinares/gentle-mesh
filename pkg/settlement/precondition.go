package settlement

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrPreconditionCommandNotAllowed indicates that a command is not permitted
// in a pre-flight precondition check (RFC-002 A2 / Issue #130).
var ErrPreconditionCommandNotAllowed = errors.New("command not permitted in precondition")

// allowedBinaries defines the list of permitted executables for PreconditionCommandExitCode.
// Preconditions are strictly read-only environment and capability verification checks.
var allowedBinaries = map[string]bool{
	"true":    true,
	"false":   true,
	"test":    true,
	"[":       true,
	"go":      true,
	"git":     true,
	"python":  true,
	"python3": true,
	"node":    true,
	"cargo":   true,
	"rustc":   true,
	"docker":  true,
	"pytest":  true,
}

// trustedSystemDirs lists standard directories where system binaries reside.
var trustedSystemDirs = map[string]bool{
	"/bin":           true,
	"/usr/bin":       true,
	"/usr/local/bin": true,
}

// ValidatePreconditionCommand parses and validates a command string for
// PreconditionCommandExitCode (RFC-002 A2 / Issue #130).
//
// It enforces:
// 1. Direct command tokenization without subshell invocation (RFC-002 S6).
// 2. Strict allowlist of read-only diagnostic tools.
// 3. Argument-level inspection for multi-purpose tools (go, git, python, node, etc.)
//    to prevent code evaluation (e.g. python -c, node -e, go run) or mutations.
func ValidatePreconditionCommand(cmdStr string) ([]string, error) {
	if strings.TrimSpace(cmdStr) == "" {
		return nil, fmt.Errorf("%w: command parameter is empty", ErrPreconditionCommandNotAllowed)
	}

	parts, err := SplitCommand(cmdStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid command syntax: %v", ErrPreconditionCommandNotAllowed, err)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: command token list is empty", ErrPreconditionCommandNotAllowed)
	}

	rawBin := parts[0]
	baseBin := filepath.Base(rawBin)

	// If a path was supplied, ensure it belongs to a trusted system directory.
	if strings.Contains(rawBin, "/") {
		dir := filepath.Clean(filepath.Dir(rawBin))
		if !trustedSystemDirs[dir] {
			return nil, fmt.Errorf("%w: binary path %q must reside in trusted system directory (/bin, /usr/bin, /usr/local/bin)", ErrPreconditionCommandNotAllowed, rawBin)
		}
	}

	if !allowedBinaries[baseBin] {
		return nil, fmt.Errorf("%w: binary %q is not in the allowed preconditions list (allowed: true, false, test, go, git, python, python3, node, cargo, rustc, docker, pytest)", ErrPreconditionCommandNotAllowed, baseBin)
	}

	// Argument-level inspection to prevent arbitrary code execution or mutations
	if err := validateCommandArgs(baseBin, parts[1:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreconditionCommandNotAllowed, err)
	}

	return parts, nil
}

func validateCommandArgs(bin string, args []string) error {
	switch bin {
	case "true", "false", "test", "[":
		// POSIX test and true/false are read-only inspection utilities by design.
		return nil

	case "go":
		if len(args) == 0 {
			return errors.New("subcommand required for 'go' in precondition (allowed: version, env)")
		}
		sub := args[0]
		if sub != "version" && sub != "env" {
			return fmt.Errorf("subcommand %q is not permitted for 'go' in precondition (allowed: version, env)", sub)
		}
		return nil

	case "git":
		if len(args) == 0 {
			return errors.New("subcommand required for 'git' in precondition (allowed: status, rev-parse, describe, version, --version)")
		}
		// Disallow global flags that configure arbitrary pagers/hooks or change repo configs
		for _, arg := range args {
			if strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--config") ||
				strings.HasPrefix(arg, "--exec-path") || strings.HasPrefix(arg, "--upload-pack") {
				return fmt.Errorf("argument %q is forbidden on 'git' in precondition", arg)
			}
		}
		sub := args[0]
		allowedGitSubs := map[string]bool{
			"status":    true,
			"rev-parse": true,
			"describe":  true,
			"version":   true,
			"--version": true,
			"-v":        true,
		}
		if sub == "diff" {
			// diff is only allowed with --quiet for exit-code checking
			for _, a := range args[1:] {
				if a == "--quiet" {
					return nil
				}
			}
			return errors.New("'git diff' in precondition requires '--quiet'")
		}
		if !allowedGitSubs[sub] {
			return fmt.Errorf("subcommand %q is not permitted for 'git' in precondition (allowed: status, rev-parse, describe, version, --version, diff --quiet)", sub)
		}
		return nil

	case "python", "python3":
		if len(args) == 0 {
			return errors.New("arguments required for python in precondition (e.g. --version)")
		}
		for _, a := range args {
			if a == "-c" || a == "-m" {
				return fmt.Errorf("flag %q is forbidden on python in precondition", a)
			}
		}
		first := args[0]
		if first != "--version" && first != "-V" && first != "-v" {
			return fmt.Errorf("argument %q is not permitted for python in precondition (allowed: --version, -V)", first)
		}
		return nil

	case "node":
		if len(args) == 0 {
			return errors.New("arguments required for node in precondition (e.g. --version)")
		}
		for _, a := range args {
			if a == "-e" || a == "--eval" || a == "-p" || a == "--print" {
				return fmt.Errorf("flag %q is forbidden on node in precondition", a)
			}
		}
		first := args[0]
		if first != "--version" && first != "-v" && first != "-V" {
			return fmt.Errorf("argument %q is not permitted for node in precondition (allowed: --version, -v)", first)
		}
		return nil

	case "cargo", "rustc":
		if len(args) == 0 {
			return fmt.Errorf("arguments required for %s in precondition (e.g. --version)", bin)
		}
		first := args[0]
		if first != "--version" && first != "-V" && first != "version" {
			return fmt.Errorf("argument %q is not permitted for %s in precondition (allowed: --version, -V)", first, bin)
		}
		return nil

	case "docker":
		if len(args) == 0 {
			return errors.New("subcommand/arguments required for docker in precondition (allowed: version, --version, info)")
		}
		first := args[0]
		if first != "version" && first != "--version" && first != "-v" && first != "info" {
			return fmt.Errorf("subcommand %q is not permitted for docker in precondition (allowed: version, --version, info)", first)
		}
		return nil

	case "pytest":
		if len(args) == 0 {
			return errors.New("arguments required for pytest in precondition (e.g. --version)")
		}
		first := args[0]
		if first != "--version" && first != "-V" && first != "-v" {
			return fmt.Errorf("argument %q is not permitted for pytest in precondition (allowed: --version, -V)", first)
		}
		return nil

	default:
		return fmt.Errorf("unhandled binary validation: %s", bin)
	}
}
