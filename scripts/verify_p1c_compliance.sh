#!/usr/bin/env bash
# scripts/verify_p1c_compliance.sh
#
# Wave 127 — single-shot Phase 1C Broadband compliance verifier.
#
# Mirrors scripts/verify_p1b_compliance.sh (Wave 120) for the Phase 1C
# tranche. Runs the four acceptance gates (build, vet, test, summary),
# prints a concise pass/fail per gate + a coverage summary, and exits
# with the correct status for CI.
#
# Distinct from:
#   - scripts/verify_p1b_compliance.sh (Wave 120 — Phase 1B Broadband)
#   - scripts/verify_p1e_compliance.sh (Wave 108 — Phase 1 Enterprise)
#
# The three scripts share build/vet/test gates but count different TC
# families — the Phase 1C catalog adds 125 new TCs across 19 modules on
# top of the 713 Phase 1B regression carry-over.
#
# Usage:
#   bash scripts/verify_p1c_compliance.sh
#
# Environment:
#   DATABASE_URL — optional. When set, the Phase 1C E2E suite runs against
#                  the local ion_p1c_smoke database. When unset, the suite
#                  t.Skip's cleanly (counted as PASS in the summary).
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
# Gate 3a — go test (unit + contract; no infra)
# -----------------------------------------------------------------
print_section "Gate 3a: go test -count=1 ./..."
TEST_LOG="/tmp/p1c_compliance_tests.log"
go test -count=1 ./... > "${TEST_LOG}" 2>&1
TEST_EXIT=$?
if [ ${TEST_EXIT} -eq 0 ]; then
    printf 'Gate 3a OK: all tests passed (transcript: %s)\n' "${TEST_LOG}"
else
    printf 'Gate 3a FAIL — failures and build errors below:\n'
    grep -E '^(--- FAIL|FAIL\b|.*\.go:[0-9]+:)' "${TEST_LOG}" | head -50
    printf '\nFull transcript: %s\n' "${TEST_LOG}"
    ANY_FAILED=1
fi

# Print a count of skipped DB-required + future-pinned tests.
SKIP_COUNT=$(grep -c -E '^--- SKIP' "${TEST_LOG}" || true)
if [ -n "${DATABASE_URL:-}" ]; then
    printf 'DATABASE_URL is set — %d skips observed (expected close to 0 + Wave 127 t.Skip pins).\n' "${SKIP_COUNT}"
else
    printf 'DATABASE_URL unset — %d skips observed (expected for DB-required + Wave 127 future-contract pins).\n' "${SKIP_COUNT}"
fi

# -----------------------------------------------------------------
# Gate 3b — Wave 127 new-context E2E tests
#
# These tests cover the Phase 1C tranche end-to-end against live
# Postgres (ion_p1c_smoke). They cover:
#
#   - cs_ticket_lifecycle      — Wave 123 ticket SM + @mentions + channels
#   - cs_sla_breach            — Wave 124 SLA matrix + breach evaluator +
#                                service requests + teams + WO-from-ticket + CSAT
#   - bulk_ops_executor        — Wave 125 executor mixed-outcomes + dry-run + idempotency
#   - maintenance_lead_time    — Wave 126 affected-customers + overrun + lead-time
#   - announcement_dispatch    — Wave 126 dispatcher + severity normalize + ack
#   - cs_dashboards            — Wave 126 agent queue / supervisor / channel / cross-module
#
# Skipped when DATABASE_URL is unset.
# -----------------------------------------------------------------
print_section "Gate 3b: Wave 127 Phase 1C E2E"
if [ -z "${DATABASE_URL:-}" ]; then
    printf 'Gate 3b SKIP: DATABASE_URL unset — Phase 1C E2E tests require live Postgres.\n'
else
    W127_LOG="/tmp/p1c_wave127_e2e.log"
    : "${JWT_SECRET:=01234567890123456789012345678901test_jwt_secret_for_local_smoke_only}"
    : "${JWT_ISSUER:=ion-sit}"
    export JWT_SECRET JWT_ISSUER
    go test -tags=e2e -count=1 \
        -run 'TestCS_|TestBulkOps|TestMaintenance|TestAnnouncement|TestCrossModuleSLA|TestDashboard' \
        -timeout=240s ./test/e2e/... > "${W127_LOG}" 2>&1
    W127_EXIT=$?
    W127_PASS=$(grep -c -E '^--- PASS' "${W127_LOG}" || true)
    W127_SKIP=$(grep -c -E '^--- SKIP' "${W127_LOG}" || true)
    W127_FAIL=$(grep -c -E '^--- FAIL' "${W127_LOG}" || true)
    if [ ${W127_EXIT} -eq 0 ]; then
        printf 'Gate 3b OK: pass=%s skip=%s fail=%s (transcript: %s)\n' \
            "${W127_PASS}" "${W127_SKIP}" "${W127_FAIL}" "${W127_LOG}"
    else
        printf 'Gate 3b FAIL: pass=%s skip=%s fail=%s (transcript: %s)\n' \
            "${W127_PASS}" "${W127_SKIP}" "${W127_FAIL}" "${W127_LOG}"
        grep -E '^(--- FAIL|FAIL\b)' "${W127_LOG}" | head -20
        ANY_FAILED=1
    fi
fi

# -----------------------------------------------------------------
# Gate 4 — test file inventory + TC ID coverage (Phase 1C)
# -----------------------------------------------------------------
print_section "Gate 4: test file + TC ID inventory (Phase 1C)"

# Wave 123 — new internal/cs/ bounded context.
CS_DOMAIN=$(find internal/cs/domain -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
CS_USECASE=$(find internal/cs/usecase -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
CS_ADAPTER=$(find internal/cs/adapter -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')

printf 'Wave 123 internal/cs/ test files:\n'
printf '  internal/cs/domain:    %s\n' "${CS_DOMAIN}"
printf '  internal/cs/usecase:   %s\n' "${CS_USECASE}"
printf '  internal/cs/adapter:   %s\n' "${CS_ADAPTER}"
CS_TOTAL=$((CS_DOMAIN + CS_USECASE + CS_ADAPTER))
printf '  -------------------------\n'
printf '  cs total:              %s\n' "${CS_TOTAL}"

# Wave 125 — operations bulk executor tests.
OPS_DOMAIN=$(find internal/operations/domain -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
OPS_USECASE=$(find internal/operations/usecase -name '*_test.go' 2>/dev/null | wc -l | tr -d ' ')
printf '\nWave 125 internal/operations/ test files:\n'
printf '  internal/operations/domain:  %s\n' "${OPS_DOMAIN}"
printf '  internal/operations/usecase: %s\n' "${OPS_USECASE}"

# Phase 1C E2E test files.
P1C_E2E=$(ls test/e2e/cs_ticket_lifecycle_e2e_test.go \
              test/e2e/cs_sla_breach_e2e_test.go \
              test/e2e/bulk_ops_executor_e2e_test.go \
              test/e2e/bulk_executor_e2e_test.go \
              test/e2e/maintenance_lead_time_e2e_test.go \
              test/e2e/announcement_dispatch_e2e_test.go \
              test/e2e/cs_dashboards_e2e_test.go \
              2>/dev/null | wc -l | tr -d ' ')
printf '\nPhase 1C E2E test files: %s\n' "${P1C_E2E}"

# TC ID references — Phase 1C specifically. Each prefix is a sub-module
# of the 19 net-new modules from the audit:
#
#   Operations (47 TCs across 8 modules):
#     TC-PM-     Planned Maintenance
#     TC-ME-     Maintenance Escalation (alias TC-MES-)
#     TC-BPC-    Bulk Plan Change
#     TC-BOM-    Bulk ODP Migration (overloaded prefix; also BOM = Bill of Materials)
#     TC-BWO-    Bulk WO Creation
#     TC-OPC-    Operational Calendar
#     TC-IAN-    Internal Announcements (catalog used TC-ANN-)
#     TC-XSL-    Cross-Module SLA (catalog used TC-CSM-)
#
#   Customer Service (78 TCs across 11 modules):
#     TC-TT-     Ticket Types (catalog used TC-TKT-)
#     TC-TL-     Ticket Lifecycle
#     TC-TCH-    Ticket Channels (catalog used TC-CHN-)
#     TC-PSL-    Priority & SLA (catalog used TC-SLA-)
#     TC-TA-     Team Assignment (catalog used TC-ASN-)
#     TC-MEN-    @Mentions
#     TC-WFT-    WO from Ticket (catalog used TC-WOT-)
#     TC-SR-     Service Requests
#     TC-COM-    Communication (overloaded; also Commission Calculation)
#     TC-CSAT-   CSAT (catalog used TC-CST-)
#     TC-CSD-    CS Dashboards
TC_PREFIXES=(
    "TC-PM-" "TC-ME-" "TC-MES-" "TC-BPC-" "TC-BOM-" "TC-BWO-"
    "TC-OPC-" "TC-IAN-" "TC-ANN-" "TC-XSL-" "TC-CSM-"
    "TC-TT-" "TC-TKT-" "TC-TL-" "TC-TCH-" "TC-CHN-"
    "TC-PSL-" "TC-SLA-" "TC-TA-" "TC-ASN-"
    "TC-MEN-" "TC-WFT-" "TC-WOT-" "TC-SR-" "TC-COM-"
    "TC-CSAT-" "TC-CST-" "TC-CSD-"
)
DISTINCT_TC_IDS=0
ACTIVE_PREFIXES=0
printf '\nPhase 1C TC ID references by prefix (in test files):\n'
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
printf '  Prefixes with references: %s of %s Phase 1C families\n' "${ACTIVE_PREFIXES}" "${#TC_PREFIXES[@]}"
printf '  TOTAL distinct TC IDs:    %s\n' "${DISTINCT_TC_IDS}"

# -----------------------------------------------------------------
# Summary
# -----------------------------------------------------------------
print_section "Summary"
if [ ${ANY_FAILED} -eq 0 ]; then
    printf 'All gates PASSED.\n'
    printf 'See docs/wave-127-100pct-phase1c-compliance-report.md for the per-TC map.\n'
    printf '\nWave 123 cs/ test files:           %s\n' "${CS_TOTAL}"
    printf 'Wave 125 operations/ test files:   %s\n' "$((OPS_DOMAIN + OPS_USECASE))"
    printf 'Phase 1C E2E test files:           %s\n' "${P1C_E2E}"
    exit 0
else
    printf 'One or more gates FAILED. See output above.\n'
    printf 'Full test transcript: %s\n' "${TEST_LOG}"
    exit 1
fi
