// Wave 121E — HRIS gateway stub-mode determinism tests.
//
// StubGateway returns canned employees + events. The canned set must:
//
//   - Be deterministic across calls (same set every poll).
//   - Match the migration seed (so the unit tests for the polling cron
//     can rely on the same identities the dev DB ships with).
//   - Produce one stable EmployeeEvent id so re-poll is idempotent.
//
// What this DOES NOT validate:
//   - Real HRIS counterparty's delta-since semantics
//   - Real REST / SOAP / CSV wire format parsing
//   - Backpressure / pagination (the stub returns ≤3 records)
package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
)

// =====================================================================
// 1) FetchEmployees is deterministic and matches the seed.
// =====================================================================

func TestHRISStub_FetchEmployeesDeterministic(t *testing.T) {
	gw := NewStubGateway()
	ctx := context.Background()
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	a, err := gw.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("first FetchEmployees: %v", err)
	}
	b, err := gw.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("second FetchEmployees: %v", err)
	}
	if len(a) != 3 || len(b) != 3 {
		t.Fatalf("len: a=%d b=%d, want 3 each (matches migration seed)", len(a), len(b))
	}
	for i := range a {
		if a[i].EmployeeNo != b[i].EmployeeNo {
			t.Errorf("row %d EmployeeNo drift: %q vs %q", i, a[i].EmployeeNo, b[i].EmployeeNo)
		}
		if a[i].Status != b[i].Status {
			t.Errorf("row %d Status drift: %q vs %q", i, a[i].Status, b[i].Status)
		}
	}
	// Pin the contract: EMP00001 active, EMP00003 resigned.
	if a[0].EmployeeNo != "EMP00001" || a[0].Status != domain.EmployeeStatusActive {
		t.Errorf("seed row 0 = (%q,%q), want (EMP00001, active)", a[0].EmployeeNo, a[0].Status)
	}
	if a[2].EmployeeNo != "EMP00003" || a[2].Status != domain.EmployeeStatusResigned {
		t.Errorf("seed row 2 = (%q,%q), want (EMP00003, resigned)", a[2].EmployeeNo, a[2].Status)
	}
}

// =====================================================================
// 2) FetchEvents returns the resign event with a STABLE id (idempotent
// re-poll — the cron upserts on id).
// =====================================================================

func TestHRISStub_FetchEventsIdempotent(t *testing.T) {
	gw := NewStubGateway()
	ctx := context.Background()
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	a, err := gw.FetchEvents(ctx, since)
	if err != nil {
		t.Fatalf("first FetchEvents: %v", err)
	}
	b, err := gw.FetchEvents(ctx, since)
	if err != nil {
		t.Fatalf("second FetchEvents: %v", err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("len: a=%d b=%d, want 1 each", len(a), len(b))
	}
	wantID := uuid.MustParse("00000000-0000-0000-0000-00000000e003")
	if a[0].ID != wantID || b[0].ID != wantID {
		t.Errorf("event id drift: a=%v b=%v want=%v", a[0].ID, b[0].ID, wantID)
	}
	if a[0].EmployeeNo != "EMP00003" {
		t.Errorf("event for %q, want EMP00003", a[0].EmployeeNo)
	}
	if a[0].Kind != domain.EventKindResigned {
		t.Errorf("event kind = %q, want %q", a[0].Kind, domain.EventKindResigned)
	}
}

// =====================================================================
// 3) FetchEmployees ignores `since` (documented limitation — real
// adapter would honour it; stub returns the full set).
// =====================================================================

func TestHRISStub_SinceParameterIgnored(t *testing.T) {
	gw := NewStubGateway()
	ctx := context.Background()

	// Even with `since` set far in the future, the stub returns
	// the full set (it has no concept of incremental delta).
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	emps, err := gw.FetchEmployees(ctx, future)
	if err != nil {
		t.Fatalf("FetchEmployees: %v", err)
	}
	if len(emps) != 3 {
		t.Errorf("stub honoured `since` (returned %d) — must be 3 (ignores it)", len(emps))
	}
}
