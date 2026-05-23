package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/errors"
)

// MonitoringService implements port.MonitoringUseCase.
//
// Two surfaces under one service:
//
//   - Customer-side: MyInvoices / MyInvoice / MyPaymentHistory /
//     MyReminderHistory — all customer-scoped (the customerID arrives
//     from claims.UserID in the handler, never trusted from the body).
//
//   - Dashboard-side: Aggregations / CycleHealth / TopOverdueCustomers
//     — finance-facing rollups. RBAC is enforced by the HTTP handler
//     (RequirePermission); this service blindly executes whatever
//     filter the caller hands in.
type MonitoringService struct {
	reader port.InvoiceReader
}

func NewMonitoringService(reader port.InvoiceReader) *MonitoringService {
	return &MonitoringService{reader: reader}
}

var _ port.MonitoringUseCase = (*MonitoringService)(nil)

// ---------------------------------------------------------------------
// Customer-side
// ---------------------------------------------------------------------

// MyInvoices returns invoices for the given customer. The handler MUST
// pass the customer_id from claims, never from query params — this
// enforces self-scope at the boundary.
func (s *MonitoringService) MyInvoices(
	ctx context.Context,
	customerID uuid.UUID,
	f port.CustomerInvoiceFilter,
) ([]port.InvoiceProjection, int, error) {
	if customerID == uuid.Nil {
		return nil, 0, errors.Unauthorized("monitoring.no_customer", "no customer context")
	}
	if s.reader == nil {
		return nil, 0, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	return s.reader.ListByCustomer(ctx, customerID, limit, offset)
}

// MyInvoice returns a single invoice scoped to the given customer. The
// reader's FindByID is unscoped, so we double-check the projection's
// CustomerID matches the claim. Mismatch → NotFound (don't leak the
// existence of someone else's invoice).
func (s *MonitoringService) MyInvoice(
	ctx context.Context,
	customerID, invoiceID uuid.UUID,
) (*port.InvoiceProjection, error) {
	if customerID == uuid.Nil {
		return nil, errors.Unauthorized("monitoring.no_customer", "no customer context")
	}
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("monitoring.invoice_id_required", "invoice id is required")
	}
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	proj, err := s.reader.FindByID(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	if proj == nil {
		return nil, errors.NotFound("monitoring.invoice_not_found", "invoice not found")
	}
	if proj.CustomerID != customerID {
		return nil, errors.NotFound("monitoring.invoice_not_found", "invoice not found")
	}
	return proj, nil
}

func (s *MonitoringService) MyPaymentHistory(
	ctx context.Context,
	customerID uuid.UUID,
	limit int,
) ([]port.PaymentHistoryRow, error) {
	if customerID == uuid.Nil {
		return nil, errors.Unauthorized("monitoring.no_customer", "no customer context")
	}
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	return s.reader.PaymentHistory(ctx, customerID, limit)
}

func (s *MonitoringService) MyReminderHistory(
	ctx context.Context,
	customerID uuid.UUID,
	limit int,
) ([]port.ReminderHistoryRow, error) {
	if customerID == uuid.Nil {
		return nil, errors.Unauthorized("monitoring.no_customer", "no customer context")
	}
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	return s.reader.ReminderHistory(ctx, customerID, limit)
}

// ---------------------------------------------------------------------
// Dashboard-side
// ---------------------------------------------------------------------

func (s *MonitoringService) Aggregations(ctx context.Context, f port.InvoiceQueryFilter) (*port.AggregationResult, error) {
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	return s.reader.Aggregations(ctx, f)
}

func (s *MonitoringService) CycleHealth(ctx context.Context, cycleID uuid.UUID) (*port.CycleHealthResult, error) {
	if cycleID == uuid.Nil {
		return nil, errors.Validation("monitoring.cycle_id_required", "cycle id is required")
	}
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	return s.reader.CycleHealth(ctx, cycleID)
}

func (s *MonitoringService) TopOverdueCustomers(ctx context.Context, limit int) ([]port.TopOverdueRow, error) {
	if s.reader == nil {
		return nil, errors.Internal("monitoring.reader_nil", "invoice reader not configured")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	return s.reader.TopOverdueCustomers(ctx, limit)
}
