package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type CommissionRepository struct {
	pool *pgxpool.Pool
}

func NewCommissionRepository(pool *pgxpool.Pool) *CommissionRepository {
	return &CommissionRepository{pool: pool}
}

var _ port.CommissionRepository = (*CommissionRepository)(nil)

func (r *CommissionRepository) Create(ctx context.Context, rec *domain.CommissionRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.commission_records
		  (id, order_id, customer_id, invoice_id, payment_id, party_type,
		   user_id, branch_id, amount, percentage, base_amount, notes, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`,
		rec.ID, rec.OrderID, rec.CustomerID, rec.InvoiceID, rec.PaymentID,
		string(rec.PartyType), rec.UserID, rec.BranchID,
		rec.Amount, rec.Percentage, rec.BaseAmount, nullableString(rec.Notes), rec.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "commission.create", "create commission record")
	}
	return nil
}

func (r *CommissionRepository) ExistsForOrder(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM billing.commission_records WHERE order_id = $1`,
		orderID).Scan(&n); err != nil {
		return false, derrors.Wrap(derrors.KindInternal, "commission.exists", "check commissions", err)
	}
	return n > 0, nil
}

func (r *CommissionRepository) List(ctx context.Context, f port.CommissionFilter) ([]domain.CommissionRecord, error) {
	var (
		args  []any
		conds []string
	)
	if f.UserID != nil {
		args = append(args, *f.UserID)
		conds = append(conds, "user_id = $"+itoa(len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		conds = append(conds, "branch_id = $"+itoa(len(args)))
	}
	if f.OrderID != nil {
		args = append(args, *f.OrderID)
		conds = append(conds, "order_id = $"+itoa(len(args)))
	}
	if f.PartyType != "" {
		args = append(args, f.PartyType)
		conds = append(conds, "party_type = $"+itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	sql := `
		SELECT id, order_id, customer_id, invoice_id, payment_id, party_type,
		       user_id, branch_id, amount, percentage, base_amount,
		       COALESCE(notes,''), created_at
		FROM billing.commission_records` + where +
		" ORDER BY created_at DESC LIMIT $" + itoa(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "commission.list", "list commissions", err)
	}
	defer rows.Close()
	out := []domain.CommissionRecord{}
	for rows.Next() {
		var (
			c     domain.CommissionRecord
			party string
		)
		if err := rows.Scan(&c.ID, &c.OrderID, &c.CustomerID, &c.InvoiceID,
			&c.PaymentID, &party, &c.UserID, &c.BranchID,
			&c.Amount, &c.Percentage, &c.BaseAmount, &c.Notes, &c.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "commission.scan", "scan commission", err)
		}
		c.PartyType = domain.PartyType(party)
		out = append(out, c)
	}
	return out, nil
}
