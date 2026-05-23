// Package usecase implements invoicesvc business rules.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/errors"
)

// SnapshotService implements port.SnapshotUseCase.
//
// One snapshot row per invoice issuance. The "auto-fire on issuance hook"
// from the billing module simply calls CreateSnapshot — we don't keep
// state about hooks; the caller is responsible for ensuring the right
// trigger point.
type SnapshotService struct {
	snapshots port.InvoiceSnapshotRepository
	reader    port.InvoiceReader
}

func NewSnapshotService(snapshots port.InvoiceSnapshotRepository, reader port.InvoiceReader) *SnapshotService {
	return &SnapshotService{snapshots: snapshots, reader: reader}
}

var _ port.SnapshotUseCase = (*SnapshotService)(nil)

// invoiceProjectionAdapter bridges port.InvoiceProjection (flat, source-
// agnostic) into the domain.InvoiceLike interface BuildFromInvoice
// needs. Local — never escapes this package.
type invoiceProjectionAdapter struct {
	id       uuid.UUID
	customer uuid.UUID
	status   string
	total    float64
}

func (a invoiceProjectionAdapter) GetID() uuid.UUID         { return a.id }
func (a invoiceProjectionAdapter) GetCustomerID() uuid.UUID { return a.customer }
func (a invoiceProjectionAdapter) GetStatus() string        { return a.status }
func (a invoiceProjectionAdapter) GetTotal() float64        { return a.total }

// CreateSnapshot loads the invoice via the SQL-only reader, then builds
// + persists the immutable snapshot. lines come from the caller (the
// billing module renders them at issue time); we don't go fetch them
// here because the upstream module already has them in memory.
func (s *SnapshotService) CreateSnapshot(
	ctx context.Context,
	invoiceID uuid.UUID,
	lines []domain.SnapshotLineItem,
	schemaSnapshotID *uuid.UUID,
) (*domain.InvoiceSnapshot, error) {
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("snapshot.invoice_required", "invoice_id is required")
	}
	if s.reader == nil {
		return nil, errors.Internal("snapshot.reader_nil", "invoice reader not configured")
	}
	proj, err := s.reader.FindByID(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	if proj == nil {
		return nil, errors.NotFound("snapshot.invoice_not_found", "invoice not found")
	}
	source := domain.SourceModule(proj.SourceModule)
	if source == "" {
		source = domain.SourceBilling
	}
	snap, err := domain.BuildFromInvoice(
		invoiceProjectionAdapter{
			id:       proj.ID,
			customer: proj.CustomerID,
			status:   proj.Status,
			total:    proj.Total,
		},
		lines, schemaSnapshotID, source,
	)
	if err != nil {
		return nil, errors.Wrap(errors.KindValidation, "snapshot.build_failed", err.Error(), err)
	}
	if err := s.snapshots.Create(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *SnapshotService) GetSnapshot(ctx context.Context, id uuid.UUID) (*domain.InvoiceSnapshot, error) {
	if id == uuid.Nil {
		return nil, errors.Validation("snapshot.id_required", "snapshot id is required")
	}
	return s.snapshots.FindByID(ctx, id)
}

func (s *SnapshotService) ListSnapshots(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoiceSnapshot, error) {
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("snapshot.invoice_required", "invoice_id is required")
	}
	return s.snapshots.ListByInvoice(ctx, invoiceID)
}
