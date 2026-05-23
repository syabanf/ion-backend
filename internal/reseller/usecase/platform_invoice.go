package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
)

// InvoiceInboxService implements port.InvoiceInboxUseCase. All reads
// are tenant-scoped via the per-request tenant resolver (same pattern
// as SubscriberService).
type InvoiceInboxService struct {
	invoices port.SubscriberInvoiceRepository
	tenantOf func(ctx context.Context) uuid.UUID
}

func NewInvoiceInboxService(invoices port.SubscriberInvoiceRepository, tenantOf func(ctx context.Context) uuid.UUID) *InvoiceInboxService {
	return &InvoiceInboxService{invoices: invoices, tenantOf: tenantOf}
}

var _ port.InvoiceInboxUseCase = (*InvoiceInboxService)(nil)

func (s *InvoiceInboxService) guardTenant(ctx context.Context) (uuid.UUID, error) {
	tenant := s.tenantOf(ctx)
	if tenant == uuid.Nil {
		return uuid.Nil, errors.Unauthorized("session.missing", "tenant not resolved")
	}
	return tenant, nil
}

func (s *InvoiceInboxService) ListMyInvoices(ctx context.Context, f port.InvoiceListFilter) ([]domain.SubscriberInvoice, int, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, 0, err
	}
	f.ResellerAccountID = tenant
	return s.invoices.List(ctx, f)
}

func (s *InvoiceInboxService) GetMyInvoice(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.invoices.FindForReseller(ctx, tenant, id)
}

// MarkMyInvoicePaid is the inbox action: tenant guard via
// FindForReseller (NotFound on cross-tenant), domain transition,
// persist. The domain refuses cancelled invoices; it's idempotent on
// already-paid.
func (s *InvoiceInboxService) MarkMyInvoicePaid(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.invoices.FindForReseller(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := inv.MarkPaid(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.invoices.UpdateStatus(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// OverdueAtMonthEnd is the dashboard helper: list open invoices whose
// due_at is past `asOf`. We do NOT flip them in this method — the
// dashboard surfaces the count and a future cron drives the actual
// state transition. Keeps reads free of side effects.
func (s *InvoiceInboxService) OverdueAtMonthEnd(ctx context.Context, asOf time.Time) ([]domain.SubscriberInvoice, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.invoices.ListOverdueForReseller(ctx, tenant, asOf)
}
