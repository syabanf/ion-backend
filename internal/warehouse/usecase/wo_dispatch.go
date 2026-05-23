// WO dispatch service methods.
//
// Wiring: cmd/warehouse-svc/main.go calls WithWODispatch on the Service
// builder. Same nil-safe pattern as WithSuppliers / WithR2 — a Service
// without the repo wired returns a clean "not configured" error rather
// than panicking.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithWODispatch attaches the WO-dispatch repo. Without this, the
// dispatch endpoints return errWODispatchNotConfigured.
func (s *Service) WithWODispatch(r port.WODispatchRepository) *Service {
	s.woDispatch = r
	return s
}

func errWODispatchNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "wo_dispatch.not_configured",
		"WO dispatch repository is not configured for this service", nil)
}

// CreateDispatch — drafts the BOM. Returns the dispatch in 'planned' state.
//
// Wave 89b — when in.Items is empty AND in.ProductID is set AND a BOM
// template repo is wired, materialize the lines from the product's
// active template. The template id is stamped onto the dispatch
// regardless of whether the caller passed explicit lines (so even a
// hand-edited dispatch carries the audit link back to the seeding
// template).
func (s *Service) CreateDispatch(ctx context.Context, in port.CreateWODispatchInput) (*domain.WODispatch, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	if _, err := s.warehouses.FindByID(ctx, in.WarehouseID); err != nil {
		return nil, err
	}

	// Wave 89b — resolve the active BOM template + optionally use it
	// to seed lines.
	var (
		sourceTemplateID *uuid.UUID
		seededItems      []port.CreateWODispatchItemInput
	)
	if in.ProductID != nil && s.bomTemplates != nil {
		if detail, err := s.bomTemplates.FindActiveForProduct(ctx, *in.ProductID); err == nil && detail != nil {
			id := detail.Template.ID
			sourceTemplateID = &id
			if len(in.Items) == 0 {
				for _, it := range detail.Items {
					seededItems = append(seededItems, port.CreateWODispatchItemInput{
						ItemID: it.StockItemID,
						Qty:    it.DefaultQuantity,
						Notes:  it.Notes,
					})
				}
			}
		}
		// FindActiveForProduct returning NotFound is non-fatal — the
		// product simply has no template yet; caller falls through to
		// explicit Items (or to a Validation error from domain.NewWODispatch).
	}
	itemSrc := in.Items
	if len(itemSrc) == 0 && len(seededItems) > 0 {
		itemSrc = seededItems
	}

	// Verify each item exists in the catalog — cheap fan-out, items list
	// is typically <10. Catches typo'd UUIDs at the boundary instead of
	// at scan time.
	for _, it := range itemSrc {
		if _, err := s.items.FindByID(ctx, it.ItemID); err != nil {
			return nil, err
		}
	}
	bomItems := make([]domain.WODispatchItem, 0, len(itemSrc))
	for _, it := range itemSrc {
		bomItems = append(bomItems, domain.WODispatchItem{
			ItemID: it.ItemID,
			Qty:    it.Qty,
			Notes:  it.Notes,
		})
	}
	d, err := domain.NewWODispatch(in.WOID, in.WarehouseID, in.DispatchedBy, bomItems, in.Notes)
	if err != nil {
		return nil, err
	}
	if sourceTemplateID != nil {
		d.SourceBOMTemplateID = sourceTemplateID
	}
	if err := s.woDispatch.Create(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// StageDispatch — counter clerk confirms gear is gathered.
func (s *Service) StageDispatch(ctx context.Context, id, _ uuid.UUID) (*domain.WODispatch, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	d, err := s.woDispatch.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := d.Stage(); err != nil {
		return nil, err
	}
	if err := s.woDispatch.UpdateStatus(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// CancelDispatch — planned or staged only.
func (s *Service) CancelDispatch(ctx context.Context, id, _ uuid.UUID, reason string) (*domain.WODispatch, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	d, err := s.woDispatch.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := d.Cancel(reason); err != nil {
		return nil, err
	}
	if err := s.woDispatch.UpdateStatus(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// MarkPickedUp — every BOM line must have status='picked' first.
// On success the dispatch transitions staged → picked_up.
//
// We also write a stock movement of type 'dispatch_to_wo' per scanned
// line so the audit trail mirrors transfer dispatch. (No stock_level
// delta yet — when the warehouse module is fully wired to BOQ-driven
// reservations, this is where the decrement lives. Round-1 leaves
// stock unchanged to keep this migration's scope manageable.)
func (s *Service) MarkPickedUp(ctx context.Context, id, performedBy uuid.UUID) (*domain.WODispatch, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	d, err := s.woDispatch.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := d.MarkPickedUp(); err != nil {
		return nil, err
	}
	if err := s.woDispatch.UpdateStatus(ctx, d); err != nil {
		return nil, err
	}
	// Audit trail — one movement per picked line.
	for _, it := range d.Items {
		_ = s.movements.Record(ctx, &domain.StockMovement{
			WarehouseID:   d.WarehouseID,
			StockItemID:   it.ItemID,
			MovementType:  domain.MovementDispatch,
			Quantity:      it.Qty,
			ReferenceType: "wo_dispatch",
			ReferenceID:   &d.ID,
			PerformedBy:   &performedBy,
			PerformedAt:   time.Now().UTC(),
		})
	}
	return d, nil
}

// PickUpItemByScan — technician scans one QR/serial. Idempotent on the
// (dispatch_id, item_id, serial_or_qr) triple thanks to the partial
// unique index in the migration; we also short-circuit in the domain
// type when the same serial is re-scanned.
func (s *Service) PickUpItemByScan(ctx context.Context, itemID uuid.UUID, serialOrQR string, pickedBy uuid.UUID) (*domain.WODispatchItem, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	it, err := s.woDispatch.FindItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if err := it.PickByScan(serialOrQR, pickedBy); err != nil {
		return nil, err
	}
	if err := s.woDispatch.UpdateItem(ctx, it); err != nil {
		return nil, err
	}
	return it, nil
}

// ReturnItem — partial returns supported via running ReturnedQty tally.
func (s *Service) ReturnItem(ctx context.Context, itemID uuid.UUID, qty float64, notes string, _ uuid.UUID) (*domain.WODispatchItem, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	it, err := s.woDispatch.FindItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if err := it.Return(qty); err != nil {
		return nil, err
	}
	if notes != "" {
		it.Notes = notes
	}
	if err := s.woDispatch.UpdateItem(ctx, it); err != nil {
		return nil, err
	}
	// If every line is fully returned, advance the header too. We re-
	// load to get the latest sibling rows.
	d, err := s.woDispatch.FindByID(ctx, it.DispatchID)
	if err != nil {
		return it, nil
	}
	allReturned := len(d.Items) > 0
	for _, sib := range d.Items {
		if sib.Status != domain.WODispatchItemStatusReturned {
			allReturned = false
			break
		}
	}
	if allReturned && d.Status == domain.WODispatchStatusPickedUp {
		now := time.Now().UTC()
		d.Status = domain.WODispatchStatusReturned
		d.ReturnedAt = &now
		_ = s.woDispatch.UpdateStatus(ctx, d)
	}
	return it, nil
}

// ListDispatches — paginated.
func (s *Service) ListDispatches(ctx context.Context, f port.WODispatchListFilter) ([]domain.WODispatch, int, error) {
	if s.woDispatch == nil {
		return nil, 0, errWODispatchNotConfigured()
	}
	return s.woDispatch.List(ctx, f)
}

// GetDispatch — full header + items.
func (s *Service) GetDispatch(ctx context.Context, id uuid.UUID) (*domain.WODispatch, error) {
	if s.woDispatch == nil {
		return nil, errWODispatchNotConfigured()
	}
	return s.woDispatch.FindByID(ctx, id)
}
