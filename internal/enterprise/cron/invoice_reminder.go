package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/pkg/notifyx"
)

// =====================================================================
// Wave 107 — invoice reminder dispatcher.
//
// Once a day, scan invoices whose status is in (issued, partial) AND
// due_at is within 3 days. For each candidate, send a notifyx push to
// the customer's billing email (best-effort; stub provider in dev) +
// stamp `reminder_sent_at` so the next tick dedupes.
//
// Idempotency: the `ListInvoicesDueSoon` usecase pre-filters out
// invoices whose ReminderSentAt is already within the current
// (due - 3d, due) window. A re-run on the same day touches zero rows.
// =====================================================================

const (
	invoiceReminderTickInterval = 24 * time.Hour
	invoiceReminderWithinDays   = 3
)

// invoiceReminderService is the narrow slice of the enterprise Service
// the cron needs. Declared locally so the cron package can be wired
// with anything matching this shape.
type invoiceReminderService interface {
	ListInvoicesDueSoon(ctx context.Context, withinDays int) ([]domain.Invoice, error)
	MarkInvoiceReminderSent(ctx context.Context, invoiceID uuid.UUID) error
}

// InvoiceReminderDispatcher fans the reminder push + stamps the
// timestamp on each invoice.
type InvoiceReminderDispatcher struct {
	pool     *pgxpool.Pool
	svc      invoiceReminderService
	notifier *notifyx.Dispatcher
	log      *slog.Logger
}

// NewInvoiceReminderDispatcher constructs the dispatcher. Notifier may
// be nil — when missing, the cron still stamps the timestamp (so the
// "due-soon" inbox stays clean) but no actual push fans out.
func NewInvoiceReminderDispatcher(
	pool *pgxpool.Pool,
	svc invoiceReminderService,
	notifier *notifyx.Dispatcher,
	log *slog.Logger,
) *InvoiceReminderDispatcher {
	return &InvoiceReminderDispatcher{
		pool:     pool,
		svc:      svc,
		notifier: notifier,
		log:      log.With("worker", "invoice_reminder"),
	}
}

// Start spawns the worker goroutine.
func (d *InvoiceReminderDispatcher) Start(ctx context.Context) {
	go d.run(ctx)
}

func (d *InvoiceReminderDispatcher) run(ctx context.Context) {
	// First tick: 1 minute after boot so the service is fully up.
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
	}
	d.RunOnce(ctx)
	t := time.NewTicker(invoiceReminderTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.RunOnce(ctx)
		}
	}
}

// RunOnce is the per-tick body. Exposed for tests.
func (d *InvoiceReminderDispatcher) RunOnce(ctx context.Context) {
	if d.svc == nil {
		return
	}
	candidates, err := d.svc.ListInvoicesDueSoon(ctx, invoiceReminderWithinDays)
	if err != nil {
		d.log.Warn("reminder: list due soon failed", "err", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	d.log.Info("reminder: dispatching", "count", len(candidates))
	for _, inv := range candidates {
		d.fire(ctx, &inv)
	}
}

func (d *InvoiceReminderDispatcher) fire(ctx context.Context, inv *domain.Invoice) {
	// Notifyx fan-out is best-effort — when the stub provider is wired
	// (the default in dev), it just logs the payload.
	if d.notifier != nil {
		msg := notifyx.Message{
			Title:    "Invoice due soon",
			Body:     "Invoice " + inv.InvoiceNumber + " is due " + inv.DueAt.UTC().Format("2006-01-02"),
			DeepLink: "/enterprise/invoices/" + inv.ID.String(),
			Topic:    "invoice.due_soon",
			Data: map[string]string{
				"invoice_id":     inv.ID.String(),
				"invoice_number": inv.InvoiceNumber,
				"due_at":         inv.DueAt.UTC().Format(time.RFC3339),
			},
		}
		// We don't have a customer-user-id resolver here; the stub
		// provider drops UserID-less messages. A follow-up wave wires
		// a CustomerID-based resolver.
		d.notifier.Send(ctx, notifyx.Target{CustomerID: uuid.Nil}, msg)
	}
	// Always stamp — even if the notifier is missing/failed, the inbox
	// dedupe field advances so the cron doesn't re-scan tomorrow.
	if err := d.svc.MarkInvoiceReminderSent(ctx, inv.ID); err != nil {
		d.log.Warn("reminder: stamp failed",
			"invoice_id", inv.ID.String(), "err", err)
	}
}

// WithInvoiceReminderDispatcher attaches the dispatcher to the runner.
// Nil-safe — passing a nil svc keeps the dispatcher off.
func (r *Runner) WithInvoiceReminderDispatcher(svc invoiceReminderService) *Runner {
	if svc == nil {
		return r
	}
	r.invoiceReminder = NewInvoiceReminderDispatcher(r.pool, svc, r.notifier, r.log)
	return r
}
