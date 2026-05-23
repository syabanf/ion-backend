#!/usr/bin/env bash
# scripts/verify_p1b_compliance.sh
#
# Wave 120 — single-shot Phase 1B Broadband compliance verifier.
#
# Runs the four acceptance gates (build, vet, test, summary), prints a
# concise pass/fail per gate + a coverage summary, and exits with the
# correct status for CI.
#
# Distinct from scripts/verify_p1e_compliance.sh (Wave 108 — Phase 1
# Enterprise). The two scripts share no state; the broadband + enterprise
# coverage sets overlap in identity/RBAC/audit but otherwise count
# different TC families.
#
# Usage:
#   bash scripts/verify_p1b_compliance.sh
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
# /tmp/p1b_compliance_tests.log for post-mortems.
# -----------------------------------------------------------------
print_section "Gate 3: go test -count=1 ./..."
TEST_LOG="/tmp/p1b_compliance_tests.log"
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

# Print a count of skipped DB-required + future-pinned tests.
SKIP_COUNT=$(grep -c -E '^--- SKIP' "${TEST_LOG}" || true)
if [ -n "${DATABASE_URL:-}" ]; then
    printf 'DATABASE_URL is set — %d skips observed (expected close to 0 + Wave 120 t.Skip pins).\n' "${SKIP_COUNT}"
else
    printf 'DATABASE_URL unset — %d skips observed (expected for DB-required + Wave 120 future-contract pins).\n' "${SKIP_COUNT}"
fi

# -----------------------------------------------------------------
# Gate 3b — Wave 121C new-context E2E tests
#
# These tests wire the five new Phase 1B bounded contexts (payment,
# nocmon, netdev, hris, invoicesvc) + the Wave 114 billing crons
# against live Postgres. Skipped when DATABASE_URL is unset; otherwise
# they exercise each context's usecase end-to-end + observe the cron
# evaluators emitting their side-effect log rows.
#
# Closes SIT gaps #3 (zero E2E coverage for new contexts) and #6
# (billing crons never observed running).
# -----------------------------------------------------------------
print_section "Gate 3b: Wave 121C new-context E2E"
if [ -z "${DATABASE_URL:-}" ]; then
    printf 'Gate 3b SKIP: DATABASE_URL unset — Wave 121C E2E tests require live Postgres.\n'
else
    W121C_LOG="/tmp/p1b_wave121c_e2e.log"
    : "${JWT_SECRET:=01234567890123456789012345678901test_jwt_secret_for_local_smoke_only}"
    : "${JWT_ISSUER:=ion-sit}"
    export JWT_SECRET JWT_ISSUER
    go test -tags=e2e -count=1 \
        -run 'TestPayment|TestNoc|TestNetdev|TestHRIS|TestInvoice_|TestBilling' \
        -timeout=180s ./test/e2e/... > "${W121C_LOG}" 2>&1
    W121C_EXIT=$?
    W121C_PASS=$(grep -c -E '^--- PASS' "${W121C_LOG}" || true)
    W121C_SKIP=$(grep -c -E '^--- SKIP' "${W121C_LOG}" || true)
    W121C_FAIL=$(grep -c -E '^--- FAIL' "${W121C_LOG}" || true)
    if [ ${W121C_EXIT} -eq 0 ]; then
        printf 'Gate 3b OK: pass=%s skip=%s fail=%s (transcript: %s)\n' \
            "${W121C_PASS}" "${W121C_SKIP}" "${W121C_FAIL}" "${W121C_LOG}"
    else
        printf 'Gate 3b FAIL: pass=%s skip=%s fail=%s (transcript: %s)\n' \
            "${W121C_PASS}" "${W121C_SKIP}" "${W121C_FAIL}" "${W121C_LOG}"
        grep -E '^(--- FAIL|FAIL\b)' "${W121C_LOG}" | head -20
        ANY_FAILED=1
    fi
fi

# -----------------------------------------------------------------
# Gate 4 — test file inventory + TC ID coverage
# -----------------------------------------------------------------
print_section "Gate 4: test file + TC ID inventory"

# State-machine tests across all 18 bounded contexts (8 carry-over + 10
# newer Phase 1B contexts).
ENTERPRISE_SM=$(find internal/enterprise -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
RESELLER_SM=$(find internal/reseller -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
PARTNERSHIP_SM=$(find internal/partnership -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
PAYMENT_SM=$(find internal/payment -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
NOCMON_SM=$(find internal/nocmon -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
NETDEVICES_SM=$(find internal/netdevices -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')
INVOICESVC_SM=$(find internal/invoicesvc -name '*_sm_test.go' 2>/dev/null | wc -l | tr -d ' ')

printf 'State-machine test files:\n'
printf '  enterprise:    %s\n' "${ENTERPRISE_SM}"
printf '  reseller:      %s\n' "${RESELLER_SM}"
printf '  partnership:   %s\n' "${PARTNERSHIP_SM}"
printf '  payment:       %s\n' "${PAYMENT_SM}"
printf '  nocmon:        %s\n' "${NOCMON_SM}"
printf '  netdevices:    %s\n' "${NETDEVICES_SM}"
printf '  invoicesvc:    %s\n' "${INVOICESVC_SM}"

# Carry-over contexts (8) — Wave 1-90 foundation.
CARRYOVER_CTX=(identity crm platform field network billing operations warehouse)
printf '\nCarry-over context test files (Wave 1-90 foundation):\n'
CARRYOVER_TOTAL=0
for ctx in "${CARRYOVER_CTX[@]}"; do
    n=$(find internal/${ctx} -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
    printf '  internal/%-12s %s\n' "${ctx}:" "${n}"
    CARRYOVER_TOTAL=$((CARRYOVER_TOTAL + n))
done
printf '  -------------------------\n'
printf '  carry-over total:       %s\n' "${CARRYOVER_TOTAL}"

# Newer P1B contexts (10) — Waves 111-118.
NEWER_CTX=(enterprise tax reseller partnership vendormgmt payment nocmon netdevices invoicesvc hris)
printf '\nPhase 1B newer-context test files (Waves 111-118):\n'
NEWER_TOTAL=0
for ctx in "${NEWER_CTX[@]}"; do
    n=$(find internal/${ctx} -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
    printf '  internal/%-12s %s\n' "${ctx}:" "${n}"
    NEWER_TOTAL=$((NEWER_TOTAL + n))
done
printf '  -------------------------\n'
printf '  newer total:            %s\n' "${NEWER_TOTAL}"

# pkg/ supporting tests.
PKG_AUDIT=$(find pkg/audit -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PKG_HTTPSERVER=$(find pkg/httpserver -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PKG_CRYPTUTIL=$(find pkg/cryptutil -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PKG_SANITIZE=$(find pkg/sanitize -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PKG_WEBHOOKX=$(find pkg/webhookx -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PKG_RATELIMIT=$(find pkg/ratelimit -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
PERF_TESTS=$(find test/perf -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
BOUNDARY_TESTS=$(find test/boundary -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
RBAC_TESTS=$(find test/rbac -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')

printf '\nPkg / cross-cutting test files:\n'
printf '  pkg/audit:              %s\n' "${PKG_AUDIT}"
printf '  pkg/httpserver:         %s\n' "${PKG_HTTPSERVER}"
printf '  pkg/cryptutil:          %s\n' "${PKG_CRYPTUTIL}"
printf '  pkg/sanitize:           %s\n' "${PKG_SANITIZE}"
printf '  pkg/webhookx:           %s\n' "${PKG_WEBHOOKX}"
printf '  pkg/ratelimit:          %s\n' "${PKG_RATELIMIT}"
printf '  test/perf:              %s\n' "${PERF_TESTS}"
printf '  test/boundary:          %s\n' "${BOUNDARY_TESTS}"
printf '  test/rbac:              %s\n' "${RBAC_TESTS}"

# TC ID references in test files. Each TC family carries a prefix
# (the catalog at /tmp/p1b-catalog.csv lists 63 modules with distinct
# prefixes). We grep across internal/, pkg/, and test/.
TC_PREFIXES=(
    "TC-BR-" "TC-USR-" "TC-RBAC-" "TC-SCH-" "TC-PRD-"
    "TC-CRM-" "TC-SAP-" "TC-CAP-" "TC-RAD-" "TC-WO-"
    "TC-TLP-" "TC-BS-" "TC-OTC-" "TC-REC-" "TC-AOB-"
    "TC-FPJ-" "TC-PAY-" "TC-SUS-" "TC-SUE-" "TC-REM-"
    "TC-LF-" "TC-COM-" "TC-FR-" "TC-IT1-" "TC-IT2-"
    "TC-IT3-" "TC-IT4-" "TC-BOM-" "TC-DSP-" "TC-RTN-"
    "TC-RTF-" "TC-STK-" "TC-IWT-" "TC-OPN-" "TC-SOT-"
    "TC-SWH-" "TC-IQR-" "TC-QR-" "TC-NDL-" "TC-ICM-"
    "TC-ALT-" "TC-MPE-" "TC-THC-" "TC-OPN-T-" "TC-BCT-"
    "TC-PSA-" "TC-PSR-" "TC-PSW-" "TC-PSH-" "TC-PSV-"
    "TC-PSF-" "TC-PH2-" "TC-ISV-" "TC-IGE-" "TC-IMC-"
    "TC-IMD-" "TC-HRI-" "TC-SOB-" "TC-SBE-" "TC-SSV-"
    "TC-SSD-" "TC-SCD-" "TC-SSE-" "TC-NSM-" "TC-FAM-"
    "TC-FIA-" "TC-NTV-" "TC-NAW-" "TC-CSM-" "TC-CRC-"
    "TC-DRF-" "TC-PRF-" "TC-PRT-" "TC-TEC-" "TC-PWH-"
)
DISTINCT_TC_IDS=0
ACTIVE_PREFIXES=0
printf '\nTC ID references by prefix (in test files):\n'
for prefix in "${TC_PREFIXES[@]}"; do
    count=$(grep -r -h -o -E "${prefix}[A-Z0-9-]+" \
        internal/ pkg/ test/ 2>/dev/null \
        | sort -u | wc -l | tr -d ' ')
    if [ "${count}" != "0" ]; then
        printf '  %-12s %s\n' "${prefix}" "${count}"
        DISTINCT_TC_IDS=$((DISTINCT_TC_IDS + count))
        ACTIVE_PREFIXES=$((ACTIVE_PREFIXES + 1))
    fi
done
printf '  -------------------------\n'
printf '  Prefixes with references: %s of %s P1B families\n' "${ACTIVE_PREFIXES}" "${#TC_PREFIXES[@]}"
printf '  TOTAL distinct TC IDs:   %s\n' "${DISTINCT_TC_IDS}"

# -----------------------------------------------------------------
# Summary
# -----------------------------------------------------------------
print_section "Summary"
if [ ${ANY_FAILED} -eq 0 ]; then
    printf 'All gates PASSED.\n'
    printf 'See docs/wave-120-100pct-broadband-compliance-report.md for the per-TC map.\n'
    printf '\nCarry-over context test files: %s\n' "${CARRYOVER_TOTAL}"
    printf 'Phase 1B newer context test files: %s\n' "${NEWER_TOTAL}"
    printf 'Total internal/ test files: %s\n' "$((CARRYOVER_TOTAL + NEWER_TOTAL))"
    exit 0
else
    printf 'One or more gates FAILED. See output above.\n'
    printf 'Full test transcript: %s\n' "${TEST_LOG}"
    exit 1
fi
