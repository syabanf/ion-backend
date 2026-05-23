#!/usr/bin/env bash
# scripts/verify_p1e_compliance.sh
#
# Wave 108 — single-shot Phase 1 Enterprise compliance verifier.
#
# Runs the four acceptance gates (build, vet, test, summary), prints a
# concise pass/fail per gate + a coverage summary, and exits with the
# correct status for CI.
#
# Usage:
#   bash scripts/verify_p1e_compliance.sh
#
# Environment:
#   DATABASE_URL — optional. When set, DB-required tests run; otherwise
#                  they t.Skip cleanly (counted as PASS in the summary).
#
# Exit codes:
#   0  every gate passed
#   1  any gate failed; see the printed gate-by-gate detail

set -uo pipefail

# Resolve the backend root so we can invoke the script from any cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${BACKEND_ROOT}"

ANY_FAILED=0
GATE_LOG="$(mktemp)"
trap 'rm -f "${GATE_LOG}"' EXIT

print_section() {
    printf '\n=== %s ===\n' "$1"
}

run_gate() {
    local label="$1"
    shift
    print_section "${label}"
    if "$@" 2>&1 | tee "${GATE_LOG}"; then
        printf '%s: OK\n' "${label}"
        return 0
    else
        printf '%s: FAIL\n' "${label}"
        ANY_FAILED=1
        return 1
    fi
}

# -----------------------------------------------------------------
# Gate 1 — go build
# -----------------------------------------------------------------
print_section "Gate 1: go build ./..."
if go build ./... 2>&1 | tee "${GATE_LOG}"; then
    printf 'Gate 1 OK: build clean\n'
else
    printf 'Gate 1 FAIL: build errors above\n'
    ANY_FAILED=1
fi

# -----------------------------------------------------------------
# Gate 2 — go vet
# -----------------------------------------------------------------
print_section "Gate 2: go vet ./..."
if go vet ./... 2>&1 | tee "${GATE_LOG}"; then
    printf 'Gate 2 OK: no vet warnings\n'
else
    printf 'Gate 2 FAIL: vet warnings above\n'
    ANY_FAILED=1
fi

# -----------------------------------------------------------------
# Gate 3 — go test
#
# We filter the output to surface only FAIL / --- FAIL: lines so the
# script's own output stays readable. The full transcript is in
# /tmp/p1e_compliance_tests.log for post-mortems.
# -----------------------------------------------------------------
print_section "Gate 3: go test -count=1 ./..."
TEST_LOG="/tmp/p1e_compliance_tests.log"
go test -count=1 ./... > "${TEST_LOG}" 2>&1
TEST_EXIT=$?
if [ ${TEST_EXIT} -eq 0 ]; then
    printf 'Gate 3 OK: all tests passed (transcript: %s)\n' "${TEST_LOG}"
else
    printf 'Gate 3 FAIL — failures and build errors below:\n'
    grep -E '^(--- FAIL|FAIL\b|.*\.go:[0-9]+:)' "${TEST_LOG}" | head -50
    printf '\nFull transcript: %s\n' "${TEST_LOG}"
    ANY_FAILED=1
fi

# Print a count of skipped DB-required tests for transparency.
SKIP_COUNT=$(grep -c -E '^--- SKIP' "${TEST_LOG}" || true)
if [ -n "${DATABASE_URL:-}" ]; then
    printf 'DATABASE_URL is set — %d skips observed (expected close to 0).\n' "${SKIP_COUNT}"
else
    printf 'DATABASE_URL unset — %d skips observed (expected for DB-required tests).\n' "${SKIP_COUNT}"
fi

# -----------------------------------------------------------------
# Gate 4 — test file inventory + TC ID coverage
# -----------------------------------------------------------------
print_section "Gate 4: test file + TC ID inventory"

# Test files per package family the Wave 91 audit cares about.
ENTERPRISE_SM=$(find internal/enterprise -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
RESELLER_SM=$(find internal/reseller -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
PARTNERSHIP_SM=$(find internal/partnership -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')

ENTERPRISE_TESTS=$(find internal/enterprise -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
RESELLER_TESTS=$(find internal/reseller -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PARTNERSHIP_TESTS=$(find internal/partnership -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
TAX_TESTS=$(find internal/tax -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
AUDIT_TESTS=$(find pkg/audit -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
HTTPSERVER_TESTS=$(find pkg/httpserver -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PERF_TESTS=$(find test/perf -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
BOUNDARY_TESTS=$(find test/boundary -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')

printf 'State-machine test files:\n'
printf '  enterprise:  %s\n' "${ENTERPRISE_SM}"
printf '  reseller:    %s\n' "${RESELLER_SM}"
printf '  partnership: %s\n' "${PARTNERSHIP_SM}"

printf '\nAll test files by package:\n'
printf '  internal/enterprise:  %s\n' "${ENTERPRISE_TESTS}"
printf '  internal/reseller:    %s\n' "${RESELLER_TESTS}"
printf '  internal/partnership: %s\n' "${PARTNERSHIP_TESTS}"
printf '  internal/tax:         %s\n' "${TAX_TESTS}"
printf '  pkg/audit:            %s\n' "${AUDIT_TESTS}"
printf '  pkg/httpserver:       %s\n' "${HTTPSERVER_TESTS}"
printf '  test/perf:            %s\n' "${PERF_TESTS}"
printf '  test/boundary:        %s\n' "${BOUNDARY_TESTS}"

# TC ID references in test files. Each TC family carries a prefix
# (TC-PB-, TC-OP-, TC-BQ-, TC-TAX-, TC-PA-, TC-AP-, TC-NG-, TC-QT-,
# TC-CPO-, TC-IC-, TC-EW-, TC-TL-, TC-TM-, TC-FN-, TC-VF-, TC-WS-,
# TC-RE-, TC-RP-, TC-PMS-, TC-PS-, TC-MC-, TC-AU-, TC-NT-, TC-SM-,
# TC-RBAC-, TC-EDGE-, TC-CONC-, TC-NFR-).
#
# We greb across internal/, pkg/, and test/ to capture every test file
# in scope.
TC_PREFIXES=(
    "TC-PB-" "TC-OP-" "TC-BQ-" "TC-TAX-" "TC-PA-" "TC-AP-" "TC-NG-"
    "TC-QT-" "TC-CPO-" "TC-IC-" "TC-EW-" "TC-TL-" "TC-TM-" "TC-FN-"
    "TC-VF-" "TC-WS-" "TC-RE-" "TC-RP-" "TC-PMS-" "TC-PS-" "TC-MC-"
    "TC-AU-" "TC-NT-" "TC-SM-" "TC-RBAC-" "TC-EDGE-" "TC-CONC-" "TC-NFR-"
)
DISTINCT_TC_IDS=0
printf '\nTC ID references by prefix (in test files):\n'
for prefix in "${TC_PREFIXES[@]}"; do
    count=$(grep -r -h -o -E "${prefix}[A-Z0-9-]+" \
        internal/ pkg/ test/ 2>/dev/null \
        | sort -u | wc -l | tr -d ' ')
    if [ "${count}" != "0" ]; then
        printf '  %-12s %s\n' "${prefix}" "${count}"
        DISTINCT_TC_IDS=$((DISTINCT_TC_IDS + count))
    fi
done
printf '  -------------------------\n'
printf '  TOTAL distinct TC IDs:  %s\n' "${DISTINCT_TC_IDS}"

# -----------------------------------------------------------------
# Summary
# -----------------------------------------------------------------
print_section "Summary"
if [ ${ANY_FAILED} -eq 0 ]; then
    printf 'All gates PASSED.\n'
    printf 'See docs/wave-108-100pct-compliance-report.md for the per-TC map.\n'
    exit 0
else
    printf 'One or more gates FAILED. See output above.\n'
    printf 'Full test transcript: %s\n' "${TEST_LOG}"
    exit 1
fi
