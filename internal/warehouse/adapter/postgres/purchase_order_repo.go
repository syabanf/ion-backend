// Wave 85 (Tier 3 starter) — postgres adapter for warehouse purchase
// orders. Header + lines + status transitions; goods receipt is a
// separate adapter (Wave 86).
package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PurchaseOrderRepository struct {
	pool *pgxpool.Pool
}

func NewPurchaseOrderRepository(pool *pgxpool.Pool) *PurchaseOrderRepository {
	return &PurchaseOrderRepository{pool: pool}
}

var _ port.PurchaseOrderRepository = (*PurchaseOrderRepository)(nil)

// poSelect — header projection. Lines are read separately by
// FindByID; List returns headers only because the dashboard table
// doesn't need to render lines per row.
const poSelect = `
SELECT id, po_number, supplier_id, branch_id, receiving_warehouse_id,
       status, subtotal, ppn_rate, total, expected_at,
       COALESCE(notes,''),
       created_by, submitted_by, submitted_at, approved_by, approved_at,
       closed_at, cancelled_at, COALESCE(cancelled_reason,''),
       created_at, updated_at
FROM warehouse.purchase_orders
`

func (r *PurchaseOrderRepository) Create(
	ctx context.Context, po *domain.PurchaseOrder, lines []domain.PurchaseOrderLine,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "po.tx_begin",
			"begin purchase order tx", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.purchase_orders (
			id, po_number, supplier_id, branch_id, receiving_warehouse_id,
			status, subtotal, ppn_rate, total, expected_at, notes,
			created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
	`,
		po.ID, po.PONumber, po.SupplierID, po.BranchID, po.ReceivingWarehouseID,
		string(po.Status), po.Subtotal, po.PPNRate, po.Total, po.ExpectedAt,
		nullableString(po.Notes), po.CreatedBy, po.CreatedAt,
	); err != nil {
		return mapDBError(err, "po", "create purchase order")
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.purchase_order_lines (
				id, purchase_order_id, line_no, stock_item_id,
				quantity_ordered, quantity_received, unit_cost, line_subtotal,
				notes, created_at
			) VALUES ($1,$2,$3,$4,$5,0,$6,$7,$8,$9)
		`,
			l.ID, l.PurchaseOrderID, l.LineNo, l.StockItemID,
			l.QuantityOrdered, l.UnitCost, l.LineSubtotal,
			nullableString(l.Notes), l.CreatedAt,
		); err != nil {
			return mapDBError(err, "po.line", "create purchase order line")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "po.tx_commit",
			"commit purchase order tx", err)
	}
	return nil
}

func (r *PurchaseOrderRepository) FindByID(
	ctx context.Context, id uuid.UUID,
) (*port.PurchaseOrderDetail, error) {
	row := r.pool.QueryRow(ctx, poSelect+" WHERE id = $1", id)
	po, err := scanPOHeader(row)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, purchase_order_id, line_no, stock_item_id,
		       quantity_ordered, quantity_received, unit_cost, line_subtotal,
		       COALESCE(notes,''), created_at
		FROM warehouse.purchase_order_lines
		WHERE purchase_order_id = $1
		ORDER BY line_no
	`, id)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "po.lines_query",
			"list PO lines", err)
	}
	defer rows.Close()
	lines := []domain.PurchaseOrderLine{}
	for rows.Next() {
		var l domain.PurchaseOrderLine
		if err := rows.Scan(&l.ID, &l.PurchaseOrderID, &l.LineNo, &l.StockItemID,
			&l.QuantityOrdered, &l.QuantityReceived, &l.UnitCost, &l.LineSubtotal,
			&l.Notes, &l.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "po.lines_scan",
				"scan PO line", err)
		}
		lines = append(lines, l)
	}
	return &port.PurchaseOrderDetail{PO: *po, Lines: lines}, nil
}

func (r *PurchaseOrderRepository) List(
	ctx context.Context, f port.PurchaseOrderListFilter,
) ([]domain.PurchaseOrder, int, error) {
	var (
		args   []any
		conds  []string
	)
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "status = $"+strconv.Itoa(len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		conds = append(conds, "branch_id = $"+strconv.Itoa(len(args)))
	}
	if f.SupplierID != nil {
		args = append(args, *f.SupplierID)
		conds = append(conds, "supplier_id = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM warehouse.purchase_orders"+where,
		args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "po.count",
			"count purchase orders", err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	rows, err := r.pool.Query(ctx,
		poSelect+where+" ORDER BY created_at DESC LIMIT $"+
			strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args)),
		args...,
	)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "po.list",
			"list purchase orders", err)
	}
	defer rows.Close()
	out := []domain.PurchaseOrder{}
	for rows.Next() {
		po, err := scanPOHeader(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *po)
	}
	return out, total, nil
}

func (r *PurchaseOrderRepository) UpdateStatus(
	ctx context.Context, id uuid.UUID, po *domain.PurchaseOrder,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.purchase_orders SET
		    status            = $2,
		    submitted_by      = $3,
		    submitted_at      = $4,
		    approved_by       = $5,
		    approved_at       = $6,
		    closed_at         = $7,
		    cancelled_at      = $8,
		    cancelled_reason  = NULLIF($9,''),
		    updated_at        = NOW()
		WHERE id = $1
	`, id, string(po.Status),
		po.SubmittedBy, po.SubmittedAt,
		po.ApprovedBy, po.ApprovedAt,
		po.ClosedAt, po.CancelledAt, po.CancelledReason,
	)
	if err != nil {
		return mapDBError(err, "po.status", "update purchase order status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("po.not_found", "purchase order not found")
	}
	return nil
}

func scanPOHeader(row pgx.Row) (*domain.PurchaseOrder, error) {
	var (
		p      domain.PurchaseOrder
		status string
	)
	if err := row.Scan(&p.ID, &p.PONumber, &p.SupplierID, &p.BranchID,
		&p.ReceivingWarehouseID, &status, &p.Subtotal, &p.PPNRate, &p.Total,
		&p.ExpectedAt, &p.Notes,
		&p.CreatedBy, &p.SubmittedBy, &p.SubmittedAt,
		&p.ApprovedBy, &p.ApprovedAt, &p.ClosedAt,
		&p.CancelledAt, &p.CancelledReason,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("po.not_found", "purchase order not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "po.scan",
			"scan purchase order", err)
	}
	p.Status = domain.POStatus(status)
	return &p, nil
}
