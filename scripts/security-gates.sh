#!/usr/bin/env bash
# security-gates.sh — Gentle Mesh & RFC-002 Security Gates
#
# Fails if prohibited patterns are found outside build-tagged test files.
# Allowlist is in scripts/security-gates.allowlist.
#
# Usage: ./scripts/security-gates.sh
# Exit 0 = clean, Exit 1 = violations found

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ALLOWLIST="$SCRIPT_DIR/security-gates.allowlist"

# ── Patterns (always forbidden outside allowlist) ───────────────────────────

# S6: exec.Command*("sh", "-c", ...) or exec.Command*("bash", "-c", ...)
# ANY occurrence outside a file with testharness/redteam build tag is a violation.
# No variable-name heuristic — the risk is the shell invocation itself.
SHELL_PATTERN='exec\.Command.*"sh".*"-c"|exec\.Command.*"bash".*"-c"'

# S1: InsecureSkipVerify (client-side TLS bypass)
INSECURE_PATTERN='InsecureSkipVerify'

# S1-LITERAL: ApplyDevInsecure(..., true) or DevInsecureConfig(true) with literal true
DEV_INSECURE_LITERAL_TRUE_PATTERN='(ApplyDevInsecure|DevInsecureConfig|DevInsecureConfigWithOutput)\([^)]*\btrue\b'

# Versioned DB artifacts (must not be in git)
DB_PATTERN='\.db$|\.db-wal$|\.db-shm$'

# ── Helpers ────────────────────────────────────────────────────────────────

# is_exempt_file returns 0 if the file has a testharness or redteam build tag.
is_exempt_file() {
    grep -qE "^//go:build.*(testharness|redteam)" "$1" 2>/dev/null || \
    grep -qE "^// \+build.*(testharness|redteam)" "$1" 2>/dev/null
}

# is_exempt_test_or_harness returns 0 if the file is a _test.go or has a testharness/redteam build tag.
is_exempt_test_or_harness() {
    local file="$1"
    [[ "$file" =~ _test\.go$ ]] && return 0
    is_exempt_file "$file"
}

# in_allowlist returns 0 if (file, pattern, phase) matches an allowlist entry.
# Format of each entry in security-gates.allowlist:
#   path:grep_pattern || phase || reason
# path is a directory prefix or filename.
# grep_pattern is an ERE matched with grep -E against file content.
# phase is the phase/milestone that eliminates this violation.
# Lines starting with # and blank lines are ignored.
in_allowlist() {
    local file="$1"; shift
    local phase="$1"

    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" =~ ^#.*$ ]] && continue       # comment
        [[ -z "${line// }" ]] && continue       # blank

        # Parse: path:grep_pattern || phase || reason
        local entry entry_phase
        entry="${line%% || *}"
        entry_phase="${line#*|| }"
        entry_phase="${entry_phase%% || *}"

        # Trim whitespace from entry and entry_phase
        entry="${entry#${entry%%[![:space:]]*}}"
        entry="${entry%${entry##*[![:space:]]}}"
        entry_phase="${entry_phase#${entry_phase%%[![:space:]]*}}"
        entry_phase="${entry_phase%${entry_phase##*[![:space:]]}}"

        # Match exact phase, permanent entries, or equivalent phase aliases (F1 <-> v1.0.2)
        local phase_match=0
        if [[ "$entry_phase" == "permanent" ]]; then
            phase_match=1
        elif [[ "$phase" == "$entry_phase" ]]; then
            phase_match=1
        elif [[ ("$phase" == "F1" || "$phase" == "v1.0.2") && ("$entry_phase" == "F1" || "$entry_phase" == "v1.0.2") ]]; then
            phase_match=1
        fi
        [[ "$phase_match" -ne 1 ]] && continue

        # Split entry on FIRST colon only (path may contain colons).
        local first_field="${entry%%:*}"
        local grep_pattern="${entry#"$first_field:"}"
        local path_pattern="$first_field"

        # Trim grep_pattern: remove ALL leading/trailing whitespace
        grep_pattern="${grep_pattern#${grep_pattern%%[![:space:]]*}}"
        grep_pattern="${grep_pattern%${grep_pattern##*[![:space:]]}}"

        # path_pattern uses ":" as delimiter; strip it for prefix matching.
        local dir_pattern="${path_pattern%:}"
        local matched=0
        if [[ "$dir_pattern" == !* ]]; then
            # Negated: skip if file matches the prefix
            local actual_prefix="${dir_pattern#.}"
            [[ "$file" == "$actual_prefix"* ]] && matched=1
        else
            # Positive: match if file starts with the directory prefix
            [[ "$file" == "$dir_pattern"* ]] && matched=1
        fi

        if [[ "$matched" -eq 1 ]]; then
            grep -qE "$grep_pattern" "$file" 2>/dev/null && return 0
        fi
    done < "$ALLOWLIST"
    return 1
}

# run_check PATTERN MESSAGE PHASE
# Returns 0 if no violations, 1 if violations found.
run_check() {
    local pattern="$1"; shift
    local message="$1"; shift
    local phase="${1:-F1}"

    echo "Checking: $message"
    local violations=0

    while IFS=: read -r file line _; do
        [[ -z "$file" ]] && continue
        # Strip leading "./" for consistent matching with allowlist paths
        file="${file#./}"
        [[ "$file" =~ \.git/ ]] && continue

        if is_exempt_file "$file"; then
            continue
        fi

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

# run_dev_insecure_literal_check checks that DevInsecureConfig or ApplyDevInsecure
# are not called with literal 'true' outside _test.go or testharness/redteam files.
run_dev_insecure_literal_check() {
    local pattern="$1"; shift
    local message="$1"; shift
    local phase="${1:-v1.0.2}"

    echo "Checking: $message"
    local violations=0

    while IFS=: read -r file line _; do
        [[ -z "$file" ]] && continue
        file="${file#./}"
        [[ "$file" =~ \.git/ ]] && continue

        if is_exempt_test_or_harness "$file"; then
            continue
        fi

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

# ── Main ─────────────────────────────────────────────────────────────────

echo "============================================================"
echo "Gentle Mesh & RFC-002 Security Gates"
echo "============================================================"
echo ""

[[ ! -f "$ALLOWLIST" ]] && {
    echo "ERROR: $ALLOWLIST not found"
    exit 1
}

total=0

# S6: any shell exec outside testharness files
# ALL sh -c exceptions are eliminated in Phase F2 (direct exec.Command replacement).
run_check "$SHELL_PATTERN" \
    "S6: exec.Command(\"sh\" | \"bash\", \"-c\", ...) — Phase F2: replace with direct exec" \
    "F2" || ((total+=$?))

# S1: InsecureSkipVerify
run_check "$INSECURE_PATTERN" \
    "S1: InsecureSkipVerify — gate behind --dev-insecure flag (Issue #15 / v1.0.2)" \
    "v1.0.2" || ((total+=$?))

# S1-LITERAL: Prohibit ApplyDevInsecure(..., true) or DevInsecureConfig(true) with literal true
# outside _test.go or testharness/redteam files.
run_dev_insecure_literal_check "$DEV_INSECURE_LITERAL_TRUE_PATTERN" \
    "S1-LITERAL: ApplyDevInsecure/DevInsecureConfig with literal true is forbidden outside _test.go or testharness/redteam" \
    "v1.0.2" || ((total+=$?))

# DB artifacts versioned (must NEVER be in git)
echo "Checking: versioned database artifacts (*.db, *.db-wal, *.db-shm)"
db_violations=0
while IFS=: read -r file _; do
    [[ -z "$file" ]] && continue
    [[ "$file" =~ \.git/ ]] && continue
    echo "  VIOLATION: $file — database artifact tracked in git"
    ((db_violations++)) || true
done < <(git ls-files --cached | grep -E "$DB_PATTERN" 2>/dev/null || true)
echo "  -> $db_violations violation(s)"
echo ""
((total+=db_violations)) || true

# ── Summary ──────────────────────────────────────────────────────────────

echo "============================================================"
if [[ $total -eq 0 ]]; then
    echo "OK: 0 violations"
    exit 0
else
    echo "FAILED: $total violation(s) found"
    exit 1
fi
