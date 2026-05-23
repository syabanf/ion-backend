// Wave 118 — orchestration HRIS bridge test.
//
// Verifies that RunCommissionTriggerTick consults the HRISResignedReader
// before queuing a commission trigger, and skips when the sales rep
// resigned on or before paid_at.

package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/audit"
)

// stubHRISResignedReader returns a configured map of (salesUserID → resignedBefore).
// Mirrors the domain semantics: IsResignedBefore returns true when paid_at
// is strictly AFTER end-of-resign-day (sales rep had already left).
type stubHRISResignedReader struct {
	resignedUsers map[uuid.UUID]time.Time
}

func (r *stubHRISResignedReader) IsResignedBefore(_ context.Context, salesUserID uuid.UUID, t time.Time) bool {
	if r == nil {
		return false
	}
	resign, ok := r.resignedUsers[salesUserID]
	if !ok {
		return false
	}
	endOfResignDay := time.Date(resign.Year(), resign.Month(), resign.Day(), 23, 59, 59, 0, time.UTC)
	return t.After(endOfResignDay)
}

func TestRunCommissionTriggerTick_HRISGate_SkipsResigned(t *testing.T) {
	now := time.Now().UTC()
	resignedUserID := uuid.New()
	activeUserID := uuid.New()
	planChange1 := uuid.New()
	planChange2 := uuid.New()

	// Paid invoice ~6 months ago (within 24h lookback won't work here since
	// the tick has a fixed lookback; we instead paid_at near `now-1h` and
	// resigned BEFORE paid_at by 7 days so the gate fires).
	paidAt := now.Add(-1 * time.Hour)
	rows := []port.PlanChangePaidInvoice{
		{
			InvoiceID:    uuid.New(),
			CustomerID:   uuid.New(),
			PlanChangeID: planChange1,
			SalesUserID:  resignedUserID,
			AmountBasis:  500000,
			PaidAt:       paidAt,
		},
		{
			InvoiceID:    uuid.New(),
			CustomerID:   uuid.New(),
			PlanChangeID: planChange2,
			SalesUserID:  activeUserID,
			AmountBasis:  300000,
			PaidAt:       paidAt,
		},
	}
	repo := newStubCommissionTriggerRepo()
	reader := &stubHRISResignedReader{
		resignedUsers: map[uuid.UUID]time.Time{
			// Resigned 7 days ago. paid_at = now-1h, which is strictly
			// after end-of-resign-day-7-days-ago → IsResignedBefore=true → skipped.
			resignedUserID: now.AddDate(0, 0, -7),
		},
	}
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, nil, repo,
		&stubPlanChangeReader{rows: rows},
		nil, nil, nil, nil, nil, audit.Nop{}, quietLogger(),
	).WithHRISResignedReader(reader)

	n, err := svc.RunCommissionTriggerTick(context.Background())
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}
	// Only the active sales rep's trigger should be queued.
	if n != 1 {
		t.Fatalf("expected 1 trigger queued (active only), got %d", n)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("repo rows: want 1, got %d", len(repo.rows))
	}
	if repo.rows[0].SalesUserID == nil || *repo.rows[0].SalesUserID != activeUserID {
		t.Fatalf("queued row is not for the active user: %v", repo.rows[0].SalesUserID)
	}
}

func TestRunCommissionTriggerTick_HRISGate_NilReader_FiresAllTriggers(t *testing.T) {
	// Backwards-compat: nil reader → legacy behaviour.
	row := port.PlanChangePaidInvoice{
		InvoiceID:    uuid.New(),
		CustomerID:   uuid.New(),
		PlanChangeID: uuid.New(),
		SalesUserID:  uuid.New(),
		AmountBasis:  500000,
		PaidAt:       time.Now().UTC(),
	}
	repo := newStubCommissionTriggerRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, nil, repo,
		&stubPlanChangeReader{rows: []port.PlanChangePaidInvoice{row}},
		nil, nil, nil, nil, nil, audit.Nop{}, quietLogger(),
	) // no WithHRISResignedReader call

	n, err := svc.RunCommissionTriggerTick(context.Background())
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("nil reader should not gate: want 1, got %d", n)
	}
}
