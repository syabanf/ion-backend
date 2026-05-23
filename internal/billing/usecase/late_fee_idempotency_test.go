// Wave 120 — late-fee idempotency on repeated cron ticks.
//
// Pins TC-LF-* "if the cron ticks twice on the same overdue invoice,
// the late fee must be applied EXACTLY ONCE — the second tick is a
// no-op". The existing TestRunLateFeeTick_AppliesOnce in
// orchestration_test.go covers the bare double-tick; this test
// extends it to three+ ticks with elapsed time, asserting the
// late_fee_applications table never gains a second row for the same
// invoice.

package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/audit"
)

func TestRunLateFeeTick_ThreeTicks_AppliesOnce(t *testing.T) {
	now := time.Now().UTC()
	due := now.AddDate(0, 0, -10) // 10 days past, grace=3 → eligible
	inv := mkInvoice(due, domain.InvoiceStatusIssued, 300000)
	invRepo := &stubInvoiceRepo{views: []port.InvoiceView{inv}}
	lateRepo := newStubLateFeeRepo()
	svc := NewOrchestrationService(
		invRepo, nil, lateRepo, nil, nil, nil, nil, nil, nil, nil, nil,
		audit.Nop{}, quietLogger(),
	)
	// First tick — applies.
	n1, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("tick 1 want 1 application; got %d", n1)
	}
	// Second tick — no-op.
	n2, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("tick 2 want 0 applications (idempotent); got %d", n2)
	}
	// Third tick — still no-op.
	n3, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if n3 != 0 {
		t.Errorf("tick 3 want 0 applications (idempotent); got %d", n3)
	}
	// The repo should have exactly one row.
	if got := len(lateRepo.rows); got != 1 {
		t.Errorf("late_fee_applications rows = %d, want 1", got)
	}
}

func TestRunLateFeeTick_StillBeforeGrace_NoApplication(t *testing.T) {
	now := time.Now().UTC()
	// Default grace = 3 days; due 2 days ago → not yet eligible.
	due := now.AddDate(0, 0, -2)
	inv := mkInvoice(due, domain.InvoiceStatusIssued, 100000)
	invRepo := &stubInvoiceRepo{views: []port.InvoiceView{inv}}
	lateRepo := newStubLateFeeRepo()
	svc := NewOrchestrationService(
		invRepo, nil, lateRepo, nil, nil, nil, nil, nil, nil, nil, nil,
		audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("RunLateFeeTick: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 applications before grace; got %d", n)
	}
}
