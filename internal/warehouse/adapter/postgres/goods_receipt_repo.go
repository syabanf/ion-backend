// Wave 86 (Tier 3) — postgres adapter for warehouse goods receipts.
//
// All-or-nothing tx so the receipt + PO bump + asset rows + stock
// movements + (optional) PO status flip land atomically. A partial
// failure (e.g. a duplicate serial number) rolls back the whole
// receipt, leaving the PO untouched.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type GoodsReceiptRepository struct {
	pool *pgxpool.Pool
}

func NewGoodsReceiptRepository(pool *pgxpool.Pool) *GoodsReceiptRepository {
	return &GoodsReceiptRepository{pool: pool}
}

var _ port.GoodsReceiptRepository = (*GoodsReceiptRepository)(nil)

func (r *GoodsReceiptRepository) Create(
	ctx context.Context, in port.CreateGoodsReceiptPersist,
) (*port.GoodsReceiptDetail, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "gr.tx_begin",
			"begin goods receipt tx", err)
	}
	defer tx.Rollback(ctx)

	// 1) Receipt header.
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.goods_receipts (
			id, receipt_number, purchase_order_id, warehouse_id,
			received_at, received_by, carrier_ref, notes, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),$5)
	`,
		in.Receipt.ID, in.Receipt.ReceiptNumber, in.Receipt.PurchaseOrderID,
		in.Receipt.WarehouseID, in.Receipt.ReceivedAt, in.Receipt.ReceivedBy,
		in.Receipt.CarrierRef, in.Receipt.Notes,
	); err != nil {
		return nil, mapDBError(err, "gr.header", "create goods receipt header")
	}

	// 2) Assets (serialized items only). We write the per-asset row
	//    here so the usecase doesn't have to depend on AssetRepository
	//    and we keep everything in the same tx without ctx hand-offs.
	for _, a := range in.AssetsToCreate {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.assets (
			  id, stock_item_id, warehouse_id, serial_number, qr_code, mac_address,
			  firmware_version, ownership_type, condition, status, received_at,
			  purchase_cost, purchase_date, distributor, purchase_order_ref, warranty_expiry,
			  is_retrofit, customer_id, assigned_technician_id, wo_id, network_node_id,
			  notes, created_at, updated_at, purchase_order_id
			) VALUES (
			  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			  $12,$13,$14,$15,$16,$17,$18,$19,$20,$21,
			  $22,$23,$23,$24
			)
		`,
			a.ID, a.StockItemID, a.WarehouseID,
			nullableString(a.SerialNumber), nullableString(a.QRCode), nullableString(a.MACAddress),
			nullableString(a.FirmwareVersion), string(a.Ownership), string(a.Condition), string(a.Status),
			a.ReceivedAt, a.PurchaseCost, a.PurchaseDate, nullableString(a.Distributor),
			nullableString(a.PurchaseOrderRef), a.WarrantyExpiry, a.IsRetrofit,
			a.CustomerID, a.AssignedTechnicianID, a.WOID, a.NetworkNodeID,
			nullableString(a.Notes), a.CreatedAt, a.PurchaseOrderID,
		); err != nil {
			return nil, mapDBError(err, "gr.asset", "create asset on receipt")
		}
	}

	// 3) Receipt lines. Each line points to (po_line, optional asset).
	for _, l := range in.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.goods_receipt_lines (
				id, goods_receipt_id, purchase_order_line_id,
				quantity_received, unit_cost, asset_id, notes, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)
		`,
			l.ID, l.GoodsReceiptID, l.PurchaseOrderLineID,
			l.QuantityReceived, l.UnitCost, l.AssetID,
			l.Notes, l.CreatedAt,
		); err != nil {
			return nil, mapDBError(err, "gr.line", "create goods receipt line")
		}
	}

	// 4) Bump PO line quantity_received. Done as one UPDATE per line
	//    because the values come from the usecase already resolved
	//    (it summed the line's prior + this receipt's contribution).
	for poLineID, newQty := range in.POLineQtyUpdates {
		if _, err := tx.Exec(ctx, `
			UPDATE warehouse.purchase_order_lines
			   SET quantity_received = $2
			 WHERE id = $1
		`, poLineID, newQty); err != nil {
			return nil, mapDBError(err, "gr.po_line_bump",
				"update PO line quantity_received")
		}
	}

	// 5) Optional PO status flip (approved → receiving, or → closed).
	if in.POStatusFlip != nil {
		p := in.POStatusFlip
		if _, err := tx.Exec(ctx, `
			UPDATE warehouse.purchase_orders SET
			    status      = $2,
			    closed_at   = $3,
			    updated_at  = NOW()
			WHERE id = $1
		`, p.ID, string(p.Status), p.ClosedAt); err != nil {
			return nil, mapDBError(err, "gr.po_status",
				"update PO status from receipt")
		}
	}

	// 6) Stock movements (audit trail). Records one per asset for
	//    serialized + one per non-serialized line. The usecase
	//    pre-computed these.
	for _, m := range in.Movements {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.stock_movements (
				id, warehouse_id, stock_item_id, asset_id, movement_type,
				quantity, reason, reference_type, reference_id,
				performed_by, performed_at
			) VALUES (uuid_generate_v4(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		`,
			m.WarehouseID, m.StockItemID, m.AssetID, string(m.MovementType),
			m.Quantity, nullableString(m.Reason), nullableString(m.ReferenceType),
			m.ReferenceID, m.PerformedBy, m.PerformedAt,
		); err != nil {
			return nil, mapDBError(err, "gr.movement",
				"record stock movement on receipt")
		}
	}

	// 7) Non-serialized stock_level deltas. UPSERT pattern matches
	//    the existing StockLevelRepository.UpsertDelta semantics:
	//    insert at (warehouse, item) with the delta as initial qty,
	//    OR bump the existing row by the delta. Keeps inventory
	//    counters consistent with the audit movements above.
	for _, d := range in.StockLevelDeltas {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.stock_levels (warehouse_id, stock_item_id, quantity, updated_at)
			VALUES ($1, $2, $3, NOW())
			ON CONFLICT (warehouse_id, stock_item_id)
			DO UPDATE SET
			    quantity   = warehouse.stock_levels.quantity + EXCLUDED.quantity,
			    updated_at = NOW()
		`, d.WarehouseID, d.StockItemID, d.Delta); err != nil {
			return nil, mapDBError(err, "gr.stock_level",
				"update stock level on receipt")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "gr.tx_commit",
			"commit goods receipt tx", err)
	}
	return &port.GoodsReceiptDetail{Receipt: in.Receipt, Lines: in.Lines}, nil
}

func (r *GoodsReceiptRepository) FindByID(
	ctx context.Context, id uuid.UUID,
) (*port.GoodsReceiptDetail, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, receipt_number, purchase_order_id, warehouse_id,
		       received_at, received_by, COALESCE(carrier_ref,''),
		       COALESCE(notes,''), created_at
		FROM warehouse.goods_receipts
		WHERE id = $1
	`, id)
	var g domain.GoodsReceipt
	if err := row.Scan(&g.ID, &g.ReceiptNumber, &g.PurchaseOrderID,
		&g.WarehouseID, &g.ReceivedAt, &g.ReceivedBy,
		&g.CarrierRef, &g.Notes, &g.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("gr.not_found", "goods receipt not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "gr.scan",
			"scan goods receipt", err)
	}
	lines, err := r.loadLines(ctx, id)
	if err != nil {
		return nil, err
	}
	return &port.GoodsReceiptDetail{Receipt: g, Lines: lines}, nil
}

func (r *GoodsReceiptRepository) ListForPO(
	ctx context.Context, poID uuid.UUID,
) ([]port.GoodsReceiptDetail, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, receipt_number, purchase_order_id, warehouse_id,
		       received_at, received_by, COALESCE(carrier_ref,''),
		       COALESCE(notes,''), created_at
		FROM warehouse.goods_receipts
		WHERE purchase_order_id = $1
		ORDER BY received_at DESC
	`, poID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "gr.list_for_po",
			"list goods receipts", err)
	}
	defer rows.Close()
	out := []port.GoodsReceiptDetail{}
	for rows.Next() {
		var g domain.GoodsReceipt
		if err := rows.Scan(&g.ID, &g.ReceiptNumber, &g.PurchaseOrderID,
			&g.WarehouseID, &g.ReceivedAt, &g.ReceivedBy,
			&g.CarrierRef, &g.Notes, &g.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "gr.list_scan",
				"scan goods receipt", err)
		}
		out = append(out, port.GoodsReceiptDetail{Receipt: g})
	}
	// Second pass: load lines per receipt. Not the most efficient
	// (could be a single JOIN), but the dashboard view typically only
	// expands one receipt at a time so the simple shape wins here.
	for i := range out {
		lines, err := r.loadLines(ctx, out[i].Receipt.ID)
		if err != nil {
			return nil, err
		}
		out[i].Lines = lines
	}
	return out, nil
}

func (r *GoodsReceiptRepository) loadLines(
	ctx context.Context, receiptID uuid.UUID,
) ([]domain.GoodsReceiptLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, goods_receipt_id, purchase_order_line_id,
		       quantity_received, unit_cost, asset_id,
		       COALESCE(notes,''), created_at
		FROM warehouse.goods_receipt_lines
		WHERE goods_receipt_id = $1
		ORDER BY created_at
	`, receiptID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "gr.lines_query",
			"list goods receipt lines", err)
	}
	defer rows.Close()
	out := []domain.GoodsReceiptLine{}
	for rows.Next() {
		var l domain.GoodsReceiptLine
		if err := rows.Scan(&l.ID, &l.GoodsReceiptID, &l.PurchaseOrderLineID,
			&l.QuantityReceived, &l.UnitCost, &l.AssetID,
			&l.Notes, &l.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "gr.lines_scan",
				"scan goods receipt line", err)
		}
		out = append(out, l)
	}
	return out, nil
}
