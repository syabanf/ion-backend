// Wave 86 (Tier 3) — Goods Receipt use cases.
//
// CreateGoodsReceipt orchestrates the whole receipt event: validates
// PO state + line balances, allocates assets for serialized items,
// pre-computes po_line.quantity_received bumps + stock_level deltas
// + stock_movements + (optionally) the PO status flip, and hands the
// fully-built payload to the repo for atomic persistence.
//
// The validation surface protects against the common operator
// mistakes:
//   - receiving against a PO not in approved/receiving state
//   - over-receiving a line beyond its ordered quantity
//   - missing serials when the stock item is serialized
//   - mismatched serial count vs quantity_received
//   - per-receipt duplicates (same PO line listed twice in the same batch)
package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithGoodsReceipts wires the GR repo. Optional; the surface returns
// errGRNotConfigured cleanly when nil.
func (s *Service) WithGoodsReceipts(r port.GoodsReceiptRepository) *Service {
	s.goodsReceipts = r
	return s
}

func errGRNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "gr.not_configured",
		"goods receipts are not configured for this service", nil)
}

// ApprovePurchaseOrder advances submitted → approved. Wave 86 added so
// the GR workflow has a real precondition state to receive against.
func (s *Service) ApprovePurchaseOrder(
	ctx context.Context, id, by uuid.UUID,
) (*port.PurchaseOrderDetail, error) {
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	detail, err := s.purchaseOrders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := detail.PO.Approve(by, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.purchaseOrders.UpdateStatus(ctx, id, &detail.PO); err != nil {
		return nil, err
	}
	return detail, nil
}

// CreateGoodsReceipt is the load-bearing workflow. See package doc for
// the ordering + validation surface.
func (s *Service) CreateGoodsReceipt(
	ctx context.Context, in port.CreateGoodsReceiptInput,
) (*port.GoodsReceiptDetail, error) {
	if s.goodsReceipts == nil {
		return nil, errGRNotConfigured()
	}
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	if len(in.Lines) == 0 {
		return nil, derrors.Validation("gr.lines_required",
			"at least one receipt line is required")
	}

	// Load PO + lines. Detail is the canonical state we'll mutate +
	// hand back to the repo if a status flip is warranted.
	detail, err := s.purchaseOrders.FindByID(ctx, in.PurchaseOrderID)
	if err != nil {
		return nil, err
	}

	// Validate PO state — only approved/receiving accept receipts.
	if detail.PO.Status != domain.POStatusApproved && detail.PO.Status != domain.POStatusReceiving {
		return nil, derrors.Conflict("gr.po_not_receivable",
			"PO must be approved or receiving; current status: "+string(detail.PO.Status))
	}
	if in.WarehouseID == uuid.Nil {
		// Default to the PO's configured receiving warehouse.
		in.WarehouseID = detail.PO.ReceivingWarehouseID
	}

	// Index PO lines for O(1) lookup + a fast "is this poLine ours" check.
	poLineByID := map[uuid.UUID]*domain.PurchaseOrderLine{}
	for i := range detail.Lines {
		l := &detail.Lines[i]
		poLineByID[l.ID] = l
	}
	// Track per-receipt duplicates (same PO line referenced twice).
	seen := map[uuid.UUID]bool{}

	now := time.Now().UTC()
	receipt, err := domain.NewGoodsReceipt(in.PurchaseOrderID, in.WarehouseID,
		in.ReceivedBy, in.CarrierRef, in.Notes, now)
	if err != nil {
		return nil, err
	}

	// Build the persist payload incrementally.
	persist := port.CreateGoodsReceiptPersist{
		Receipt:          *receipt,
		POLineQtyUpdates: map[uuid.UUID]float64{},
	}

	for _, l := range in.Lines {
		poLine, ok := poLineByID[l.PurchaseOrderLineID]
		if !ok {
			return nil, derrors.Validation("gr.po_line_unknown",
				"purchase_order_line_id does not belong to this PO")
		}
		if seen[l.PurchaseOrderLineID] {
			return nil, derrors.Validation("gr.line_duplicate",
				"each PO line can only appear once per receipt; split into separate receipts if needed")
		}
		seen[l.PurchaseOrderLineID] = true

		if l.QuantityReceived <= 0 {
			return nil, derrors.Validation("gr.quantity_invalid",
				"quantity_received must be positive")
		}
		// Over-receipt guard. The dashboard typically prevents this
		// at the form level; the API mirrors the rule so the DB
		// stays clean.
		open := poLine.QuantityOrdered - poLine.QuantityReceived
		if l.QuantityReceived > open {
			return nil, derrors.Validation("gr.over_received",
				"quantity_received exceeds the remaining open quantity on the PO line")
		}

		// Look up stock item to learn serialized vs not. We don't
		// cache here — a typical receipt has < 20 lines so the
		// per-line fetch is cheap.
		item, ierr := s.items.FindByID(ctx, poLine.StockItemID)
		if ierr != nil {
			return nil, ierr
		}

		// Unit cost: caller can override; otherwise use the PO line's.
		unitCost := l.UnitCost
		if unitCost <= 0 {
			unitCost = poLine.UnitCost
		}

		if item.Serialized {
			expectedSerials := int(l.QuantityReceived)
			if float64(expectedSerials) != l.QuantityReceived {
				return nil, derrors.Validation("gr.serial_quantity_invalid",
					"serialized items must have integer quantity_received (one serial per unit)")
			}
			if len(l.Serials) != expectedSerials {
				return nil, derrors.Validation("gr.serial_count_mismatch",
					"serialized items need exactly quantity_received serial entries")
			}
			for _, sn := range l.Serials {
				asset, aerr := domain.NewAsset(item.ID, in.WarehouseID,
					strings.TrimSpace(sn.SerialNumber), now)
				if aerr != nil {
					return nil, aerr
				}
				asset.QRCode = strings.TrimSpace(sn.QRCode)
				asset.MACAddress = strings.TrimSpace(sn.MACAddress)
				if c := domain.Condition(sn.Condition); c.Valid() {
					asset.Condition = c
				}
				if o := domain.Ownership(sn.Ownership); o.Valid() {
					asset.Ownership = o
				}
				cost := unitCost
				asset.PurchaseCost = &cost
				poID := in.PurchaseOrderID
				asset.PurchaseOrderID = &poID
				persist.AssetsToCreate = append(persist.AssetsToCreate, *asset)

				// Receipt line referencing this asset.
				lineID := uuid.New()
				persist.Lines = append(persist.Lines, domain.GoodsReceiptLine{
					ID:                  lineID,
					GoodsReceiptID:      receipt.ID,
					PurchaseOrderLineID: poLine.ID,
					QuantityReceived:    1,
					UnitCost:            unitCost,
					AssetID:             &asset.ID,
					Notes:               strings.TrimSpace(l.Notes),
					CreatedAt:           now,
				})
				// One movement per serial.
				persist.Movements = append(persist.Movements, domain.StockMovement{
					WarehouseID:   in.WarehouseID,
					StockItemID:   item.ID,
					AssetID:       &asset.ID,
					MovementType:  domain.MovementIntake,
					Quantity:      1,
					ReferenceType: "goods_receipt",
					ReferenceID:   &receipt.ID,
					PerformedBy:   in.ReceivedBy,
					PerformedAt:   now,
				})
			}
		} else {
			// Non-serialized — single receipt line covers the bulk.
			persist.Lines = append(persist.Lines, domain.GoodsReceiptLine{
				ID:                  uuid.New(),
				GoodsReceiptID:      receipt.ID,
				PurchaseOrderLineID: poLine.ID,
				QuantityReceived:    l.QuantityReceived,
				UnitCost:            unitCost,
				Notes:               strings.TrimSpace(l.Notes),
				CreatedAt:           now,
			})
			persist.Movements = append(persist.Movements, domain.StockMovement{
				WarehouseID:   in.WarehouseID,
				StockItemID:   item.ID,
				MovementType:  domain.MovementIntake,
				Quantity:      l.QuantityReceived,
				ReferenceType: "goods_receipt",
				ReferenceID:   &receipt.ID,
				PerformedBy:   in.ReceivedBy,
				PerformedAt:   now,
			})
			persist.StockLevelDeltas = append(persist.StockLevelDeltas,
				port.StockLevelDelta{
					WarehouseID: in.WarehouseID,
					StockItemID: item.ID,
					Delta:       l.QuantityReceived,
				})
		}

		// Bump the parent PO line's quantity_received.
		newQty := poLine.QuantityReceived + l.QuantityReceived
		persist.POLineQtyUpdates[poLine.ID] = newQty
		// Reflect the bump on the in-memory PO detail so the
		// "all lines fully received" check below sees the new value.
		poLine.QuantityReceived = newQty
	}

	// Decide PO status flip. The first receipt against an approved
	// PO flips it to receiving. If every line is now fully received,
	// flip to closed instead.
	allReceived := true
	for _, l := range detail.Lines {
		if l.QuantityReceived < l.QuantityOrdered {
			allReceived = false
			break
		}
	}
	switch {
	case allReceived:
		// approved + all-received in one go: flip approved → receiving → closed.
		if detail.PO.Status == domain.POStatusApproved {
			if err := detail.PO.MarkReceiving(now); err != nil {
				return nil, err
			}
		}
		if err := detail.PO.Close(now); err != nil {
			return nil, err
		}
		persist.POStatusFlip = &detail.PO
	case detail.PO.Status == domain.POStatusApproved:
		if err := detail.PO.MarkReceiving(now); err != nil {
			return nil, err
		}
		persist.POStatusFlip = &detail.PO
	}

	return s.goodsReceipts.Create(ctx, persist)
}

func (s *Service) GetGoodsReceipt(
	ctx context.Context, id uuid.UUID,
) (*port.GoodsReceiptDetail, error) {
	if s.goodsReceipts == nil {
		return nil, errGRNotConfigured()
	}
	return s.goodsReceipts.FindByID(ctx, id)
}

func (s *Service) ListGoodsReceiptsForPO(
	ctx context.Context, poID uuid.UUID,
) ([]port.GoodsReceiptDetail, error) {
	if s.goodsReceipts == nil {
		return nil, errGRNotConfigured()
	}
	return s.goodsReceipts.ListForPO(ctx, poID)
}
