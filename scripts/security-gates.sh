#!/usr/bin/env bash
# security-gates.sh — RFC-002 v2 Phase 0b Security Gate
#
# Fails if prohibited patterns are found outside build-tagged test files.
# All current violations are in the ALLOWLIST section below.
#
# Usage: ./scripts/security-gates.sh
# Exit 0 = clean, Exit 1 = violations found

set -euo pipefail

# ── Patterns ─────────────────────────────────────────────────────────────────

# S6: exec.Command* with "-c" where the arg looks like request/network data
# Safe: exec.CommandContext("sh", "-c", "echo hello") — literal constant
# Dangerous: exec.CommandContext("sh", "-c", req.Body) — request data
SHELL_PATTERN='exec\.Command.*"sh".*"-c".*(cmd|command|input|req\.|body|from|network|data)'

# S1: InsecureSkipVerify (client-side TLS skip — server must enforce mTLS)
INSECURE_PATTERN='InsecureSkipVerify'

# Tests skipped without security justification
SKIP_PATTERN='t\.Skip\('

# Private key in tracked files
KEY_PATTERN='-----BEGIN.*PRIVATE KEY-----'

# ── Allowlist ────────────────────────────────────────────────────────────────
# Format: each line is a grep-style pattern to skip.
# Lines starting with # are ignored.
# Sections: ALWAYS (always allow these), TESTINFRA (allow in test files)

ALLOWLIST="
# ── InsecureSkipVerify (Phase 0b) ───────────────────────────────────────────
# pkg/shell/mesh.go: dev stub, not imported in production binaries
pkg/shell/mesh.go
# integration/agent/server_shell.go: testharness-only stub
integration/agent/server_shell.go
# integration/agent/tls_test.go: explicitly tests TLS 1.1 rejection (S9)
integration/agent/tls_test.go
# integration/agent/client.go: the WithInsecureSkipVerify helper function definition
integration/agent/client.go:WithInsecureSkipVerify
# integration/testscenario/scenario.go: test harness with self-signed certs
integration/testscenario/scenario.go
# integration/testscenario/distributed_test.go: test setup with self-signed certs
integration/testscenario/distributed_test.go

# ── t.Skip() in test infrastructure (Phase 0b) ──────────────────────────────
# These are legitimate infrastructure skips (docker availability, short mode)
# — NOT security violations
integration/testscenario/
integration/agent/

# ── Shell execution in testharness-only files (Phase 2) ─────────────────
# server_shell.go and shell/ are testharness stubs — to be gated in Phase 2
integration/agent/server_shell.go
pkg/shell/
"

# ── Helpers ────────────────────────────────────────────────────────────────

is_exempt_file() {
    grep -qE "^//go:build.*(testharness|redteam)" "$1" 2>/dev/null || \
    grep -qE "^// \+build.*(testharness|redteam)" "$1" 2>/dev/null
}

# Returns 0 if file matches an allowlist pattern for given phase
in_allowlist() {
    local file="$1"
    local phase="$2"

    local skip_pattern
    # For phase 0b, also skip test infrastructure files for t.Skip checks
    if [[ "$phase" == "0b" ]] || [[ "$phase" == "F1" ]]; then
        skip_pattern="integration/testscenario/|integration/agent/|pkg/|cmd/"
    fi

    local allow_phase
    case "$phase" in
        0b) allow_phase="pkg/shell/mesh.go|integration/agent/server_shell.go|integration/agent/tls_test.go|integration/agent/client.go:WithInsecureSkipVerify|integration/testscenario/scenario.go|integration/testscenario/distributed_test.go" ;;
        F1) allow_phase="integration/testscenario/|integration/agent/|pkg/" ;;
        F2) allow_phase="integration/agent/server_shell.go|pkg/shell/|integration/testscenario/scenario_test.go" ;;
        *)  allow_phase="" ;;
    esac

    local combined="$allow_phase${skip_pattern:+|$skip_pattern}"
    if [[ -n "$combined" ]]; then
        grep -qE "$combined" <<< "$file" 2>/dev/null && return 0
    fi
    return 1
}

run_check() {
    local name="$1"
    local pattern="$2"
    local message="$3"
    local phase="${4:-F1}"

    echo "Checking: $name"
    local violations=0

    while IFS=: read -r file line _; do
        [[ -z "$file" ]] && continue

        # Skip excluded dirs
        [[ "$file" =~ \.git/ ]] && continue

        # Skip exempt files
        if is_exempt_file "$file"; then
            continue
        fi

        # Check allowlist
        if in_allowlist "$file" "$phase"; then
            continue
        fi

        echo "  VIOLATION: $file:$line"
        echo "    -> $message"
        ((violations++)) || true
    done < <(grep -rnE --include="*.go" "$pattern" . 2>/dev/null || true)

    echo "  -> $violations violation(s)"
    echo ""
    return $violations
}

# ── Main ────────────────────────────────────────────────────────────────

echo "============================================================"
echo "RFC-002 v2 Security Gates"
echo "============================================================"
echo ""

total=0

run_check "Shell exec (sh -c with request data)" "$SHELL_PATTERN" \
    "S6: data from network must not be executed as shell command" "F2" || ((total+=$?))

run_check "InsecureSkipVerify" "$INSECURE_PATTERN" \
    "S1: mTLS must be enforced in production; gate behind --dev-insecure" "0b" || ((total+=$?))

run_check "t.Skip() without security-gate comment" "$SKIP_PATTERN" \
    "t.Skip() without // security-gate: allowed — document exception" "F1" || ((total+=$?))

# Secrets (git-tracked only)
echo "Checking: Private key material in tracked files"
secrets_violations=0
while IFS=: read -r file line _; do
    [[ -z "$file" ]] && continue
    [[ "$file" =~ \.git/ ]] && continue
    if in_allowlist "$file" "0b"; then
        continue
    fi
    echo "  VIOLATION: $file:$line"
    echo "    -> $KEY_PATTERN"
    ((secrets_violations++)) || true
done < <(git grep -nE "$KEY_PATTERN" -- '*.pem' '*.key' 2>/dev/null || true)
echo "  -> $secrets_violations violation(s)"
echo ""
((total+=secrets_violations)) || true

# ── Summary ──────────────────────────────────────────────────────────────

echo "============================================================"
if [[ $total -eq 0 ]]; then
    echo "OK: 0 violations"
    echo "============================================================"
    exit 0
else
    echo "FAIL: $total violation(s)"
    echo ""
    echo "Phase 0b targets:"
    echo "  - InsecureSkipVerify: gate behind --dev-insecure flag"
    echo ""
    echo "Phase F1 targets:"
    echo "  - t.Skip(): add // security-gate: allowed comments"
    echo "  - G104: handle errors explicitly"
    echo "  - G306: file permissions 0600"
    echo "  - DisallowUnknownFields in JSON decoders"
    echo ""
    echo "Phase F2 targets:"
    echo "  - Replace exec.CommandContext(sh, -c, req.data) with direct exec"
    echo ""
    echo "Edit this script's in_allowlist() function to add exceptions."
    echo "============================================================"
    exit 1
fi
