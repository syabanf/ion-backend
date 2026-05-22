package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type OrderRepository struct {
	pool *pgxpool.Pool
}

func NewOrderRepository(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

var _ port.OrderRepository = (*OrderRepository)(nil)

const orderSelect = `
SELECT id, order_number, lead_id, customer_id, product_id,
       monthly_price, otc_price, excess_charge, accept_excess_cable,
       nearest_node_id, branch_id, sales_id, status,
       COALESCE(otc_type, 'postpaid'),
       COALESCE(notes,''),
       created_at, updated_at
FROM crm.orders
`

func (r *OrderRepository) Create(ctx context.Context, o *domain.Order) error {
	otcType := string(o.OTCType)
	if otcType == "" {
		otcType = string(domain.OTCTypePostpaid)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crm.orders (
			id, order_number, lead_id, customer_id, product_id,
			monthly_price, otc_price, excess_charge, accept_excess_cable,
			nearest_node_id, branch_id, sales_id, status, otc_type, notes,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
	`,
		o.ID, o.OrderNumber, o.LeadID, o.CustomerID, o.ProductID,
		o.MonthlyPrice, o.OTCPrice, o.ExcessCharge, o.AcceptExcessCable,
		o.NearestNodeID, o.BranchID, o.SalesID, string(o.Status),
		otcType,
		nullableString(o.Notes), o.CreatedAt,
	)
	return mapDBError(err, "order.create", "create order")
}

func (r *OrderRepository) List(ctx context.Context, status string, limit, offset int) ([]domain.Order, int, error) {
	return r.list(ctx, listOptions{status: status, limit: limit, offset: offset})
}

// ListForCustomer returns orders scoped to a single customer. Used by the
// customer detail page (Gap B FE — OTC type pill) so we can render the
// active order without paging through every order in the system.
func (r *OrderRepository) ListForCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]domain.Order, int, error) {
	return r.list(ctx, listOptions{customerID: &customerID, limit: limit, offset: offset})
}

type listOptions struct {
	status     string
	customerID *uuid.UUID
	limit      int
	offset     int
}

func (r *OrderRepository) list(ctx context.Context, o listOptions) ([]domain.Order, int, error) {
	args := []any{}
	wheres := []string{}
	if o.status != "" {
		args = append(args, o.status)
		wheres = append(wheres, "status = $"+itoa(len(args)))
	}
	if o.customerID != nil {
		args = append(args, *o.customerID)
		wheres = append(wheres, "customer_id = $"+itoa(len(args)))
	}
	where := ""
	if len(wheres) > 0 {
		where = " WHERE " + strings.Join(wheres, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM crm.orders"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.order_count", "count orders", err)
	}
	limit, offset := o.limit, o.offset
	if limit <= 0 {
		limit = 50
	}
	sql := orderSelect + where + " ORDER BY created_at DESC LIMIT $" + itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.order_list", "list orders", err)
	}
	defer rows.Close()
	out := []domain.Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *o)
	}
	return out, total, nil
}

func (r *OrderRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	row := r.pool.QueryRow(ctx, orderSelect+" WHERE id = $1", id)
	return scanOrder(row)
}

func scanOrder(row pgx.Row) (*domain.Order, error) {
	var (
		o       domain.Order
		status  string
		otcType string
	)
	err := row.Scan(&o.ID, &o.OrderNumber, &o.LeadID, &o.CustomerID, &o.ProductID,
		&o.MonthlyPrice, &o.OTCPrice, &o.ExcessCharge, &o.AcceptExcessCable,
		&o.NearestNodeID, &o.BranchID, &o.SalesID, &status, &otcType, &o.Notes,
		&o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("order.not_found", "order not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.order_scan", "scan order", err)
	}
	o.Status = domain.OrderStatus(status)
	o.OTCType = domain.OTCType(otcType)
	return &o, nil
}
