package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type CycleRepository struct {
	pool *pgxpool.Pool
}

func NewCycleRepository(pool *pgxpool.Pool) *CycleRepository {
	return &CycleRepository{pool: pool}
}

var _ port.CycleRepository = (*CycleRepository)(nil)

func (r *CycleRepository) Create(ctx context.Context, c *domain.BillingCycle) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.billing_cycles
		  (id, customer_id, order_id, period_start, period_end,
		   invoice_id, status, notes, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`,
		c.ID, c.CustomerID, c.OrderID, c.PeriodStart, c.PeriodEnd,
		c.InvoiceID, string(c.Status), nullableString(c.Notes), c.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "cycle.create", "create billing cycle")
	}
	return nil
}

func (r *CycleRepository) ExistsForPeriod(ctx context.Context, customerID uuid.UUID, periodStart time.Time) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM billing.billing_cycles
		WHERE customer_id = $1 AND period_start = $2::date
	`, customerID, periodStart).Scan(&n)
	if err != nil {
		return false, derrors.Wrap(derrors.KindInternal, "cycle.exists", "check cycle", err)
	}
	return n > 0, nil
}

func (r *CycleRepository) List(ctx context.Context, f port.CycleFilter) ([]domain.BillingCycle, int, error) {
	var (
		args  []any
		conds []string
	)
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		conds = append(conds, "customer_id = $"+itoa(len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "status = $"+itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM billing.billing_cycles"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cycle.count", "count cycles", err)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	sql := `
		SELECT id, customer_id, order_id, period_start, period_end,
		       invoice_id, status, COALESCE(notes,''), created_at
		FROM billing.billing_cycles` + where +
		" ORDER BY period_start DESC, created_at DESC LIMIT $" +
		itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cycle.list", "list cycles", err)
	}
	defer rows.Close()
	out := []domain.BillingCycle{}
	for rows.Next() {
		var (
			c      domain.BillingCycle
			status string
		)
		if err := rows.Scan(&c.ID, &c.CustomerID, &c.OrderID,
			&c.PeriodStart, &c.PeriodEnd, &c.InvoiceID,
			&status, &c.Notes, &c.CreatedAt); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "cycle.scan", "scan cycle", err)
		}
		c.Status = domain.CycleStatus(status)
		out = append(out, c)
	}
	return out, total, nil
}

