package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WholesaleOrderRepository implements port.WholesaleOrderRepository
// against `reseller.wholesale_orders` + `reseller.wholesale_order_lines`.
// Create runs a single transaction so a partially-saved order (header
// without lines, or vice versa) can never exist.
type WholesaleOrderRepository struct {
	pool *pgxpool.Pool
}

func NewWholesaleOrderRepository(pool *pgxpool.Pool) *WholesaleOrderRepository {
	return &WholesaleOrderRepository{pool: pool}
}

var _ port.WholesaleOrderRepository = (*WholesaleOrderRepository)(nil)

const orderCols = `
	id, reseller_account_id, supplier_subsidiary_id,
	COALESCE(order_no, ''), status,
	subtotal, total,
	created_at, updated_at,
	approved_at, fulfilled_at
`

// Create inserts the header + every line in one tx. The order_no is
// generated here (per-day counter) so the domain stays clock-free.
func (r *WholesaleOrderRepository) Create(ctx context.Context, o *domain.WholesaleOrder) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.wholesale_order_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if o.OrderNo == "" {
		o.OrderNo = generateOrderNo(o.CreatedAt)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO reseller.wholesale_orders
			(id, reseller_account_id, supplier_subsidiary_id, order_no, status,
			 subtotal, total,
			 created_at, updated_at, approved_at, fulfilled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		o.ID, o.ResellerAccountID, o.SupplierSubsidiaryID, o.OrderNo, string(o.Status),
		o.Subtotal, o.Total,
		o.CreatedAt, o.UpdatedAt, o.ApprovedAt, o.FulfilledAt,
	); err != nil {
		return mapDBError(err, "wholesale_order", "insert wholesale order")
	}

	for _, l := range o.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO reseller.wholesale_order_lines
				(id, order_id, sku_id, qty, unit_price, line_total)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, l.ID, o.ID, l.SKUID, l.Qty, l.UnitPrice, l.LineTotal); err != nil {
			return mapDBError(err, "wholesale_order_line", "insert order line")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.wholesale_order_commit", "commit order tx", err)
	}
	return nil
}

func (r *WholesaleOrderRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM reseller.wholesale_orders WHERE id = $1`, id)
	o, err := scanOrder(row)
	if err != nil {
		return nil, err
	}
	lines, err := r.loadLines(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Lines = lines
	return &o, nil
}

func (r *WholesaleOrderRepository) loadLines(ctx context.Context, orderID uuid.UUID) ([]domain.WholesaleOrderLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, order_id, sku_id, qty, COALESCE(unit_price, 0), COALESCE(line_total, 0)
		FROM reseller.wholesale_order_lines
		WHERE order_id = $1
		ORDER BY id
	`, orderID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wholesale_order_lines", "load lines", err)
	}
	defer rows.Close()
	out := []domain.WholesaleOrderLine{}
	for rows.Next() {
		var l domain.WholesaleOrderLine
		if err := rows.Scan(&l.ID, &l.OrderID, &l.SKUID, &l.Qty, &l.UnitPrice, &l.LineTotal); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.wholesale_order_line_scan", "scan line", err)
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *WholesaleOrderRepository) List(ctx context.Context, f port.WholesaleOrderListFilter) ([]domain.WholesaleOrder, int, error) {
	var wh []string
	var args []any
	if f.ResellerAccountID != uuid.Nil {
		args = append(args, f.ResellerAccountID)
		wh = append(wh, fmt.Sprintf("reseller_account_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.wholesale_orders`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wholesale_order_count", "count orders", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + orderCols + ` FROM reseller.wholesale_orders` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wholesale_order_list", "list orders", err)
	}
	defer rows.Close()
	out := []domain.WholesaleOrder{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	// Lines aren't loaded on list — the list view is summary-only.
	// Callers that need lines call FindByID per row.
	return out, total, nil
}

func (r *WholesaleOrderRepository) UpdateStatus(ctx context.Context, o *domain.WholesaleOrder) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.wholesale_orders
		SET status = $2,
		    approved_at = $3,
		    fulfilled_at = $4,
		    updated_at = NOW()
		WHERE id = $1
	`,
		o.ID, string(o.Status), o.ApprovedAt, o.FulfilledAt,
	)
	if err != nil {
		return mapDBError(err, "wholesale_order", "update wholesale order status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wholesale_order.not_found", "wholesale order not found")
	}
	return nil
}

func scanOrder(row pgx.Row) (domain.WholesaleOrder, error) {
	var o domain.WholesaleOrder
	var status string
	err := row.Scan(
		&o.ID, &o.ResellerAccountID, &o.SupplierSubsidiaryID,
		&o.OrderNo, &status,
		&o.Subtotal, &o.Total,
		&o.CreatedAt, &o.UpdatedAt,
		&o.ApprovedAt, &o.FulfilledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WholesaleOrder{}, derrors.NotFound("wholesale_order.not_found", "wholesale order not found")
	}
	if err != nil {
		return domain.WholesaleOrder{}, derrors.Wrap(derrors.KindInternal, "db.wholesale_order_scan", "scan order", err)
	}
	o.Status = domain.WholesaleOrderStatus(status)
	return o, nil
}

// generateOrderNo returns a WO-YYYYMMDD-XXXXXXXX number derived from
// the order's creation time + a hex slice of the id. Good enough for
// human-readable display while we don't have a per-day counter table.
// Uniqueness is enforced by the DB; a collision (vanishingly small)
// would surface as a Conflict via mapDBError.
func generateOrderNo(at time.Time) string {
	suffix := uuid.New().String()
	suffix = strings.ReplaceAll(suffix, "-", "")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("WO-%s-%s", at.UTC().Format("20060102"), strings.ToUpper(suffix))
}
