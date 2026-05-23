package cron

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// stubReminderSvc captures every MarkInvoiceReminderSent call so the
// test can assert which invoices got stamped. ListInvoicesDueSoon
// returns the pre-canned set.
type stubReminderSvc struct {
	dueSoon       []domain.Invoice
	stampCalls    []uuid.UUID
	listErr       error
	stampErr      error
}

func (s *stubReminderSvc) ListInvoicesDueSoon(ctx context.Context, withinDays int) ([]domain.Invoice, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.dueSoon, nil
}

func (s *stubReminderSvc) MarkInvoiceReminderSent(ctx context.Context, invoiceID uuid.UUID) error {
	s.stampCalls = append(s.stampCalls, invoiceID)
	return s.stampErr
}

// TestInvoiceReminder_RunOnce_HappyPath — TC-NT-007 + finance-AR
// reminder: candidates get stamped exactly once per tick.
func TestInvoiceReminder_RunOnce_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	inv1 := domain.Invoice{
		ID:            uuid.New(),
		InvoiceNumber: "INV-001",
		DueAt:         now.Add(24 * time.Hour),
		Status:        domain.InvoiceStatusIssued,
	}
	inv2 := domain.Invoice{
		ID:            uuid.New(),
		InvoiceNumber: "INV-002",
		DueAt:         now.Add(48 * time.Hour),
		Status:        domain.InvoiceStatusPartial,
	}
	svc := &stubReminderSvc{dueSoon: []domain.Invoice{inv1, inv2}}

	d := &InvoiceReminderDispatcher{
		svc: svc,
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	d.RunOnce(context.Background())

	if len(svc.stampCalls) != 2 {
		t.Fatalf("stamp calls = %d, want 2", len(svc.stampCalls))
	}
	// Both invoice IDs should have been stamped (order-independent).
	seen := map[uuid.UUID]bool{svc.stampCalls[0]: true, svc.stampCalls[1]: true}
	if !seen[inv1.ID] || !seen[inv2.ID] {
		t.Errorf("not all expected invoice IDs were stamped: %+v", svc.stampCalls)
	}
}

// TestInvoiceReminder_RunOnce_NoCandidates — empty list returns without
// touching the stamp surface.
func TestInvoiceReminder_RunOnce_NoCandidates(t *testing.T) {
	svc := &stubReminderSvc{dueSoon: nil}
	d := &InvoiceReminderDispatcher{
		svc: svc,
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	d.RunOnce(context.Background())
	if len(svc.stampCalls) != 0 {
		t.Errorf("unexpected stamps for empty input: %+v", svc.stampCalls)
	}
}

// TestInvoiceReminder_RunOnce_ListError — a list failure logs + returns
// without panicking; no stamp calls fire.
func TestInvoiceReminder_RunOnce_ListError(t *testing.T) {
	svc := &stubReminderSvc{listErr: context.DeadlineExceeded}
	d := &InvoiceReminderDispatcher{
		svc: svc,
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	d.RunOnce(context.Background())
	if len(svc.stampCalls) != 0 {
		t.Errorf("stamp calls on list-error: %+v", svc.stampCalls)
	}
}

// TestInvoiceReminder_RunOnce_NilSvc — the dispatcher tolerates a nil
// service (defensive: keeps the runner-level start path safe even if
// the wiring builder was given a nil arg).
func TestInvoiceReminder_RunOnce_NilSvc(t *testing.T) {
	d := &InvoiceReminderDispatcher{
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	// Should not panic.
	d.RunOnce(context.Background())
}
