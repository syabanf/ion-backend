// Wave 121E — HRIS gateway env-flag toggle tests.
//
// The current cmd/hris-svc reads HRIS_GATEWAY_ENABLED and logs a TODO,
// then falls back to StubGateway regardless. This file's job is to:
//
//   1) Pin StubGateway behavior so when the real adapter lands the
//      contract assertions don't need to be re-derived.
//   2) Document (via TestRealMode_NotYetImplemented) that the
//      env-flag toggle is a no-op today — a real bug to flag for ops.
//
// FINDING: HRIS_GATEWAY_ENABLED=true currently falls back to stub
// without erroring. Operators who flip the flag at runtime get a log
// message and zero behavior change. The Wave 121E readiness doc
// flags this; remediation is "either error on the flag with no
// implementation, or land the real gateway".
package gateway

import (
	"context"
	"testing"
	"time"
)

// =====================================================================
// 1) StubGateway construction is env-independent.
//
// The constructor takes no env input; flipping HRIS_GATEWAY_ENABLED
// doesn't change anything inside the stub. This pin protects against
// a future refactor that might add env reads to NewStubGateway.
// =====================================================================

func TestHRIS_StubGateway_EnvIndependent(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	withFlag := NewStubGateway()

	t.Setenv("HRIS_GATEWAY_ENABLED", "false")
	withoutFlag := NewStubGateway()

	ctx := context.Background()
	since := time.Unix(0, 0)

	a, err := withFlag.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("withFlag: %v", err)
	}
	b, err := withoutFlag.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("withoutFlag: %v", err)
	}
	if len(a) != len(b) {
		t.Errorf("employee count differs across env: with=%d without=%d", len(a), len(b))
	}
	for i := range a {
		if a[i].EmployeeNo != b[i].EmployeeNo {
			t.Errorf("row %d EmployeeNo drift across env: %q vs %q", i, a[i].EmployeeNo, b[i].EmployeeNo)
		}
	}
}

// =====================================================================
// 2) Real-mode toggle — currently a NO-OP, documented as a bug.
//
// When the real gateway lands, this Skip becomes a real assertion
// that:
//   - HRIS_GATEWAY_ENABLED=true returns a RealGateway (or named
//     interface) whose FetchEmployees hits an upstream HRIS.
//   - HRIS_GATEWAY_ENABLED=false / unset returns StubGateway.
//   - HRIS_GATEWAY_ENABLED=true without HRIS_BASE_URL errors cleanly.
// =====================================================================

func TestHRIS_RealMode_FlagIsCurrentlyNoOp(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Skip("Wave 121E FINDING: HRIS_GATEWAY_ENABLED=true currently falls back to stub silently in cmd/hris-svc/main.go (logs only). Tracked in production wiring readiness doc — remediation needed.")
}
