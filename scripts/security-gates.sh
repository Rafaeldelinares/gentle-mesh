#!/usr/bin/env bash
# security-gates.sh — RFC-001 / Gentle Mesh Security Gate
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ALLOWLIST="$SCRIPT_DIR/security-gates.allowlist"
SHELL_PATTERN='exec\.Command.*"sh".*"-c"|exec\.Command.*"bash".*"-c"'
INSECURE_PATTERN='InsecureSkipVerify'
DB_PATTERN='\.db$|\.db-wal$|\.db-shm$'

is_exempt_file() {
    grep -qE "^//go:build.*(testharness|redteam)|^// \+build.*(testharness|redteam)" "$1" 2>/dev/null
}

in_allowlist() {
    local file="$1" phase="$2"
    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" =~ ^#.*$ || -z "${line// }" ]] && continue
        local entry entry_phase
        entry="${line%% || *}"
        entry_phase="${line#*|| }"; entry_phase="${entry_phase%% || *}"
        entry_phase="$(echo "$entry_phase" | xargs)"
        [[ "$phase" != "$entry_phase" ]] && continue

        local first_field="${entry%%:*}"
        local grep_pattern="$(echo "${entry#"$first_field:"}" | xargs)"
        local dir_pattern="${first_field%:}"
        local matched=0
        if [[ "$dir_pattern" == !* ]]; then
            local actual_prefix="${dir_pattern#.}"
            [[ "$file" == "$actual_prefix"* ]] && matched=1
        else
            [[ "$file" == "$dir_pattern"* ]] && matched=1
        fi
        if [[ "$matched" -eq 1 ]]; then
            grep -qE "$grep_pattern" "$file" 2>/dev/null && return 0
        fi
    done < "$ALLOWLIST"
    return 1
}

run_check() {
    local pattern="$1" message="$2" phase="${3:-v1.0.2}"
    echo "Checking: $message"
    local violations=0
    while IFS=: read -r file line _; do
        [[ -z "$file" ]] && continue
        file="${file#./}"
        [[ "$file" =~ \.git/ ]] && continue
        is_exempt_file "$file" && continue
        in_allowlist "$file" "$phase" && continue
        echo "  VIOLATION: $file:$line -> $message"
        ((violations++)) || true
    done < <(grep -rnE --include="*.go" "$pattern" . 2>/dev/null || true)
    echo "  -> $violations violation(s)"; echo ""
    return $violations
}

echo "=== Gentle Mesh Security Gates ==="
[[ ! -f "$ALLOWLIST" ]] && { echo "ERROR: $ALLOWLIST not found"; exit 1; }
total=0
run_check "$SHELL_PATTERN" "S6: exec.Command sh/bash -c" "v1.0.2" || ((total+=$?))
run_check "$INSECURE_PATTERN" "S1: InsecureSkipVerify" "v1.0.2" || ((total+=$?))

echo "Checking: versioned database artifacts"
db_violations=0
while IFS=: read -r file _; do
    [[ -z "$file" || "$file" =~ \.git/ ]] && continue
    in_allowlist "$file" "v1.0.2" && continue
    echo "  VIOLATION: $file (database artifact in git)"
    ((db_violations++)) || true
done < <(git ls-files --cached | grep -E "$DB_PATTERN" 2>/dev/null || true)
echo "  -> $db_violations violation(s)"; echo ""
((total+=db_violations)) || true

if [[ $total -eq 0 ]]; then
    echo "OK: 0 violations"; exit 0
else
    echo "FAIL: $total violation(s)"; exit 1
fi
