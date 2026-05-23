// Package usecase implements the warehouse application services.
//
// Same conventions as identity / network usecases: one method = one use
// case, orchestrates domain + driven ports, no HTTP / SQL leakage.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Service struct {
	warehouses port.WarehouseRepository
	items      port.StockItemRepository
	assets     port.AssetRepository
	levels     port.StockLevelRepository
	movements  port.MovementRepository
	inventory  port.InventoryRepository
	transfers  port.TransferRepository
	// Optional — wired by WithSuppliers; the supplier surface is
	// independent of stock movement so leaving it nil keeps the rest
	// of the usecase functional.
	suppliers port.SupplierRepository
	// M3 r2 — optional, nil-safe so M3 r1 wiring keeps compiling.
	thresholds port.ThresholdRepository
	alerts     port.AlertRepository
	opnames    port.OpnameRepository
	// valuation resolves platform_config.inventory_valuation_method
	// when a caller doesn't specify an explicit FIFO/LIFO direction.
	// Nil → fall back to LIFO (the historical default).
	valuation port.ValuationReader
	// woDispatch handles BOM-driven dispatch from a warehouse to a field
	// work order. Optional (WithWODispatch) — nil-safe via
	// errWODispatchNotConfigured.
	woDispatch port.WODispatchRepository
	// Wave 85 (Tier 3 starter) — purchase orders. Optional; the create
	// + list + detail surface 503s cleanly when this isn't wired.
	purchaseOrders port.PurchaseOrderRepository
	// Wave 86 — goods receipts. Depends on purchaseOrders being wired
	// since CreateGoodsReceipt mutates parent PO line state.
	goodsReceipts port.GoodsReceiptRepository
	// Wave 87 — asset retrofit. Cannibalizes a source asset and
	// produces a new one in a single audit-logged tx.
	assetRetrofits port.AssetRetrofitRepository
	// Wave 89 — per-product BOM templates. The dispatch flow (later)
	// pre-fills its BOM lines from the active template here.
	bomTemplates port.ProductBOMTemplateRepository
	log          *slog.Logger
}

// WithValuation attaches the platform_config reader so ListAssets +
// Inventory default to the configured FIFO/LIFO direction when callers
// don't pass `order_by` explicitly. cmd/warehouse-svc/main.go wires
// this; tests that don't care can leave it nil.
func (s *Service) WithValuation(r port.ValuationReader) *Service {
	s.valuation = r
	return s
}

func NewService(
	warehouses port.WarehouseRepository,
	items port.StockItemRepository,
	assets port.AssetRepository,
	levels port.StockLevelRepository,
	movements port.MovementRepository,
	inventory port.InventoryRepository,
	transfers port.TransferRepository,
	log *slog.Logger,
) *Service {
	return &Service{
		warehouses: warehouses, items: items, assets: assets,
		levels: levels, movements: movements, inventory: inventory,
		transfers: transfers, log: log,
	}
}

// WithR2 attaches the M3 r2 driven ports (thresholds, alerts, opname).
// Optional — M3 r1 callers can keep using NewService unchanged; the new
// HTTP routes will surface clear "not configured" errors when these are
// nil. cmd/warehouse-svc/main.go always calls this.
func (s *Service) WithR2(thresholds port.ThresholdRepository, alerts port.AlertRepository, opnames port.OpnameRepository) *Service {
	s.thresholds = thresholds
	s.alerts = alerts
	s.opnames = opnames
	return s
}

// WithSuppliers attaches the supplier registry. Kept as a separate
// builder so a future deployment that doesn't want the supplier
// surface (e.g. a minimal warehouse svc for a side-test) doesn't have
// to set up the table; the supplier endpoints will then 503 cleanly
// via `errSupplierNotConfigured`.
func (s *Service) WithSuppliers(r port.SupplierRepository) *Service {
	s.suppliers = r
	return s
}

var _ port.UseCase = (*Service)(nil)

// errSupplierNotConfigured is the canonical response when the supplier
// surface is called on a Service that was constructed without
// WithSuppliers. We surface a clear unavailable error rather than a
// nil-pointer panic.
func errSupplierNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "supplier.not_configured",
		"supplier registry is not configured for this service", nil)
}

// =====================================================================
// Suppliers (CRM-Sales-Enterprise PRD §5.1)
// =====================================================================

func (s *Service) ListSuppliers(ctx context.Context, f port.SupplierListFilter) ([]domain.Supplier, int, error) {
	if s.suppliers == nil {
		return nil, 0, errSupplierNotConfigured()
	}
	return s.suppliers.List(ctx, f)
}

func (s *Service) GetSupplier(ctx context.Context, id uuid.UUID) (*domain.Supplier, error) {
	if s.suppliers == nil {
		return nil, errSupplierNotConfigured()
	}
	return s.suppliers.FindByID(ctx, id)
}

func (s *Service) CreateSupplier(ctx context.Context, in port.CreateSupplierInput) (*domain.Supplier, error) {
	if s.suppliers == nil {
		return nil, errSupplierNotConfigured()
	}
	// Guard against duplicate codes before the DB does — gives us a
	// clean "code_taken" error code instead of a generic uniqueness
	// violation. (The DB-level unique index is still the source of
	// truth — this is a UX nicety.)
	if existing, err := s.suppliers.FindByCode(ctx, in.Code); err == nil && existing != nil {
		return nil, derrors.Conflict("supplier.code_taken", "code already in use")
	}
	sup, err := domain.NewSupplier(in.Code, in.CompanyName)
	if err != nil {
		return nil, err
	}
	sup.ContactPerson = in.ContactPerson
	sup.Phone = in.Phone
	sup.Email = in.Email
	sup.Address = in.Address
	sup.PaymentTerms = in.PaymentTerms
	sup.NPWP = in.NPWP
	sup.NIB = in.NIB
	if in.CategoryTags != nil {
		sup.CategoryTags = in.CategoryTags
	}
	sup.Notes = in.Notes
	if err := s.suppliers.Create(ctx, sup); err != nil {
		return nil, err
	}
	return sup, nil
}

func (s *Service) UpdateSupplier(ctx context.Context, in port.UpdateSupplierInput) (*domain.Supplier, error) {
	if s.suppliers == nil {
		return nil, errSupplierNotConfigured()
	}
	return s.suppliers.Update(ctx, in)
}

// =====================================================================
// Warehouses
// =====================================================================

func (s *Service) ListWarehouses(ctx context.Context, activeOnly bool) ([]port.WarehouseListItem, error) {
	return s.warehouses.List(ctx, activeOnly)
}

func (s *Service) GetWarehouse(ctx context.Context, id uuid.UUID) (*port.WarehouseListItem, error) {
	return s.warehouses.FindByID(ctx, id)
}

func (s *Service) CreateWarehouse(ctx context.Context, in port.CreateWarehouseInput) (*domain.Warehouse, error) {
	w, err := domain.NewWarehouse(in.Name, in.Code)
	if err != nil {
		return nil, err
	}
	w.BranchID = in.BranchID
	w.Address = in.Address
	w.Notes = in.Notes
	if err := s.warehouses.Create(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Service) UpdateWarehouse(ctx context.Context, in port.UpdateWarehouseInput) (*domain.Warehouse, error) {
	return s.warehouses.Update(ctx, in)
}

// =====================================================================
// Stock catalog
// =====================================================================

func (s *Service) ListStockItems(ctx context.Context, f port.StockItemListFilter) ([]domain.StockItem, int, error) {
	return s.items.List(ctx, f)
}

func (s *Service) GetStockItem(ctx context.Context, id uuid.UUID) (*domain.StockItem, error) {
	return s.items.FindByID(ctx, id)
}

func (s *Service) CreateStockItem(ctx context.Context, in port.CreateStockItemInput) (*domain.StockItem, error) {
	if existing, err := s.items.FindBySKU(ctx, in.SKU); err == nil && existing != nil {
		return nil, derrors.Conflict("stock_item.sku_taken", "sku already in use")
	}
	item, err := domain.NewStockItem(in.SKU, in.Name, in.Category)
	if err != nil {
		return nil, err
	}
	if in.Unit != "" {
		if !in.Unit.Valid() {
			return nil, derrors.Validation("stock_item.unit_invalid", "invalid unit")
		}
		item.Unit = in.Unit
	}
	item.Brand = in.Brand
	item.Model = in.Model
	item.Spec = in.Spec
	item.DefaultUnitCost = in.DefaultUnitCost

	if err := s.items.Create(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) UpdateStockItem(ctx context.Context, in port.UpdateStockItemInput) (*domain.StockItem, error) {
	return s.items.Update(ctx, in)
}

// =====================================================================
// Intake
// =====================================================================
//
// Intake is the only write path in the round-1 slice. The flow:
//
//   1. Verify the warehouse + stock_item exist.
//   2. For serialized items: create one asset row per serial entry, then
//      write a single 'intake' movement with quantity = N (the count).
//      Each asset gets `received_at` so FIFO/LIFO suggestions work later.
//   3. For non-serialized: upsert the (warehouse,item) stock_level with
//      a positive delta, write an 'intake' movement carrying that delta.
//
// The whole thing is intentionally not transactional across repos right
// now — the assets/levels/movements writes happen sequentially. We accept
// the risk for round 1; round 2 will lift this into a transaction once
// the OperationRepository abstraction exists.
func (s *Service) Intake(ctx context.Context, in port.IntakeInput, performedBy uuid.UUID) (*port.IntakeResult, error) {
	item, err := s.items.FindByID(ctx, in.StockItemID)
	if err != nil {
		return nil, err
	}
	if _, err := s.warehouses.FindByID(ctx, in.WarehouseID); err != nil {
		return nil, err
	}

	if in.ReceivedAt.IsZero() {
		in.ReceivedAt = time.Now().UTC()
	}

	result := &port.IntakeResult{}

	if item.Serialized {
		if len(in.Serials) == 0 {
			return nil, derrors.Validation("intake.serials_required",
				"serialized item needs at least one serial entry")
		}
		for _, e := range in.Serials {
			a, err := domain.NewAsset(item.ID, in.WarehouseID, e.SerialNumber, in.ReceivedAt)
			if err != nil {
				return nil, err
			}
			a.QRCode = e.QRCode
			a.MACAddress = e.MACAddress
			if e.Condition.Valid() {
				a.Condition = e.Condition
			}
			if e.Ownership.Valid() {
				a.Ownership = e.Ownership
			}
			if in.UnitCost != nil {
				a.PurchaseCost = in.UnitCost
			} else {
				a.PurchaseCost = item.DefaultUnitCost
			}
			a.PurchaseDate = in.PurchaseDate
			a.Distributor = in.Distributor
			a.PurchaseOrderRef = in.PurchaseOrderRef
			a.WarrantyExpiry = in.WarrantyExpiry

			if err := s.assets.Create(ctx, a); err != nil {
				return nil, err
			}
			result.CreatedAssets = append(result.CreatedAssets, a.ID)

			// One movement per serialized unit so the audit trail is per-asset.
			if err := s.movements.Record(ctx, &domain.StockMovement{
				WarehouseID:   in.WarehouseID,
				StockItemID:   item.ID,
				AssetID:       &a.ID,
				MovementType:  domain.MovementIntake,
				Quantity:      1,
				Reason:        in.Reason,
				ReferenceType: "intake",
				PerformedBy:   &performedBy,
				PerformedAt:   time.Now().UTC(),
			}); err != nil {
				return nil, err
			}
		}
		return result, nil
	}

	// Non-serialized — cable (meters) or consumable (count/pack).
	if in.Quantity <= 0 {
		return nil, derrors.Validation("intake.quantity_required",
			"non-serialized intake needs a positive quantity")
	}
	level, err := s.levels.UpsertDelta(ctx, in.WarehouseID, item.ID, in.Quantity)
	if err != nil {
		return nil, err
	}
	result.StockLevel = level

	if err := s.movements.Record(ctx, &domain.StockMovement{
		WarehouseID:   in.WarehouseID,
		StockItemID:   item.ID,
		MovementType:  domain.MovementIntake,
		Quantity:      in.Quantity,
		Reason:        in.Reason,
		ReferenceType: "intake",
		PerformedBy:   &performedBy,
		PerformedAt:   time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// =====================================================================
// Inventory + Assets + Movements
// =====================================================================

func (s *Service) Inventory(ctx context.Context, f port.InventoryFilter) ([]port.InventoryRow, int, error) {
	if f.WarehouseID == uuid.Nil {
		return nil, 0, derrors.Validation("inventory.warehouse_required", "warehouse_id is required")
	}
	if f.OrderBy == "" {
		f.OrderBy = s.defaultValuation(ctx)
	}
	return s.inventory.Inventory(ctx, f)
}

func (s *Service) ListAssets(ctx context.Context, f port.AssetListFilter) ([]domain.Asset, int, error) {
	if f.OrderBy == "" {
		f.OrderBy = s.defaultValuation(ctx)
	}
	return s.assets.List(ctx, f)
}

// defaultValuation returns the platform-wide FIFO/LIFO method. Empty
// string when no reader is wired — repos treat that as LIFO (legacy).
func (s *Service) defaultValuation(ctx context.Context) string {
	if s.valuation == nil {
		return ""
	}
	return s.valuation.InventoryValuationMethod(ctx)
}

func (s *Service) GetAsset(ctx context.Context, id uuid.UUID) (*domain.Asset, error) {
	return s.assets.FindByID(ctx, id)
}

func (s *Service) ListMovements(ctx context.Context, warehouseID uuid.UUID, limit, offset int) ([]domain.StockMovement, int, error) {
	return s.movements.List(ctx, warehouseID, limit, offset)
}

// =====================================================================
// Transfers
// =====================================================================

func (s *Service) CreateTransfer(ctx context.Context, in port.CreateTransferInput) (*domain.Transfer, error) {
	if in.SourceWarehouseID == in.DestinationWarehouseID {
		return nil, derrors.Validation("transfer.same_warehouse",
			"source and destination must differ")
	}
	if len(in.Items) == 0 {
		return nil, derrors.Validation("transfer.items_required", "at least one item is required")
	}
	if _, err := s.warehouses.FindByID(ctx, in.SourceWarehouseID); err != nil {
		return nil, err
	}
	if _, err := s.warehouses.FindByID(ctx, in.DestinationWarehouseID); err != nil {
		return nil, err
	}

	t := &domain.Transfer{
		ID:                     uuid.New(),
		TransferNumber:         transferNumber(time.Now()),
		SourceWarehouseID:      in.SourceWarehouseID,
		DestinationWarehouseID: in.DestinationWarehouseID,
		Status:                 domain.TransferStatusDraft,
		Notes:                  in.Notes,
		CreatedBy:              &in.CreatedBy,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}
	for _, ii := range in.Items {
		if ii.Quantity <= 0 {
			return nil, derrors.Validation("transfer.quantity_invalid", "quantity must be positive")
		}
		t.Items = append(t.Items, domain.TransferItem{
			ID:          uuid.New(),
			TransferID:  t.ID,
			StockItemID: ii.StockItemID,
			AssetID:     ii.AssetID,
			Quantity:    ii.Quantity,
		})
	}
	if err := s.transfers.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) ListTransfers(ctx context.Context, status string, limit, offset int) ([]domain.Transfer, int, error) {
	return s.transfers.List(ctx, status, limit, offset)
}

func (s *Service) GetTransfer(ctx context.Context, id uuid.UUID) (*domain.Transfer, error) {
	return s.transfers.FindByID(ctx, id)
}

// DispatchTransfer: source warehouse confirms shipment.
//
//   For non-serialized lines: decrement source stock_level by qty,
//     write 'transfer_out' movement.
//   For serialized lines: flip asset.warehouse_id to NULL (in transit),
//     status='dispatched', write 'transfer_out' movement.
//
// Not yet wrapped in a transaction across repos — same trade-off as Intake.
func (s *Service) DispatchTransfer(ctx context.Context, id, performedBy uuid.UUID) (*domain.Transfer, error) {
	t, err := s.transfers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != domain.TransferStatusDraft {
		return nil, derrors.Conflict("transfer.not_draft", "only draft transfers can be dispatched")
	}
	for _, it := range t.Items {
		if it.AssetID != nil {
			if err := s.assets.UpdateStatus(ctx, *it.AssetID, domain.AssetStatusDispatched, nil); err != nil {
				return nil, err
			}
		} else {
			if _, err := s.levels.UpsertDelta(ctx, t.SourceWarehouseID, it.StockItemID, -it.Quantity); err != nil {
				return nil, err
			}
		}
		if err := s.movements.Record(ctx, &domain.StockMovement{
			WarehouseID:   t.SourceWarehouseID,
			StockItemID:   it.StockItemID,
			AssetID:       it.AssetID,
			MovementType:  domain.MovementTransferOut,
			Quantity:      it.Quantity,
			ReferenceType: "transfer",
			ReferenceID:   &t.ID,
			PerformedBy:   &performedBy,
			PerformedAt:   time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if err := s.transfers.UpdateStatus(ctx, t.ID, domain.TransferStatusDispatched, &now); err != nil {
		return nil, err
	}
	return s.transfers.FindByID(ctx, t.ID)
}

// ReceiveTransfer: destination warehouse confirms receipt.
//
//   For non-serialized: increment destination stock_level.
//   For serialized: move asset to destination warehouse, status='in_stock'.
//   Either way: 'transfer_in' movement at destination.
func (s *Service) ReceiveTransfer(ctx context.Context, id, performedBy uuid.UUID) (*domain.Transfer, error) {
	t, err := s.transfers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != domain.TransferStatusDispatched {
		return nil, derrors.Conflict("transfer.not_dispatched",
			"only dispatched transfers can be received")
	}
	for _, it := range t.Items {
		if it.AssetID != nil {
			dst := t.DestinationWarehouseID
			if err := s.assets.UpdateStatus(ctx, *it.AssetID, domain.AssetStatusInStock, &dst); err != nil {
				return nil, err
			}
		} else {
			if _, err := s.levels.UpsertDelta(ctx, t.DestinationWarehouseID, it.StockItemID, it.Quantity); err != nil {
				return nil, err
			}
		}
		if err := s.movements.Record(ctx, &domain.StockMovement{
			WarehouseID:   t.DestinationWarehouseID,
			StockItemID:   it.StockItemID,
			AssetID:       it.AssetID,
			MovementType:  domain.MovementTransferIn,
			Quantity:      it.Quantity,
			ReferenceType: "transfer",
			ReferenceID:   &t.ID,
			PerformedBy:   &performedBy,
			PerformedAt:   time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if err := s.transfers.UpdateStatus(ctx, t.ID, domain.TransferStatusReceived, &now); err != nil {
		return nil, err
	}
	return s.transfers.FindByID(ctx, t.ID)
}

func (s *Service) CancelTransfer(ctx context.Context, id uuid.UUID) error {
	t, err := s.transfers.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if t.Status != domain.TransferStatusDraft {
		return derrors.Conflict("transfer.cannot_cancel",
			"only draft transfers can be cancelled")
	}
	return s.transfers.UpdateStatus(ctx, t.ID, domain.TransferStatusCancelled, nil)
}

// transferNumber generates a human-readable transfer number like
// "TR-20260513-XXXX" using a date prefix + a short UUID suffix.
func transferNumber(t time.Time) string {
	return fmt.Sprintf("TR-%s-%s",
		t.UTC().Format("20060102"),
		uuid.New().String()[:8],
	)
}
