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

type ReferralRewardRepository struct {
	pool *pgxpool.Pool
}

func NewReferralRewardRepository(pool *pgxpool.Pool) *ReferralRewardRepository {
	return &ReferralRewardRepository{pool: pool}
}

var _ port.ReferralRewardRepository = (*ReferralRewardRepository)(nil)

func (r *ReferralRewardRepository) Create(ctx context.Context, x *domain.ReferralReward) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.referral_rewards
		  (id, referral_id, referrer_customer_id, referee_customer_id,
		   order_id, invoice_id, amount, status, paid_at, notes, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`,
		x.ID, x.ReferralID, x.ReferrerCustomerID, x.RefereeCustomerID,
		x.OrderID, x.InvoiceID, x.Amount, string(x.Status),
		x.PaidAt, nullableString(x.Notes), x.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "referral.create", "create referral reward")
	}
	return nil
}

func (r *ReferralRewardRepository) ExistsForReferral(ctx context.Context, referralID uuid.UUID) (bool, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM billing.referral_rewards WHERE referral_id = $1`,
		referralID,
	).Scan(&n); err != nil {
		return false, derrors.Wrap(derrors.KindInternal, "referral.exists", "check referral", err)
	}
	return n > 0, nil
}

func (r *ReferralRewardRepository) List(ctx context.Context, f port.ReferralRewardFilter) ([]domain.ReferralReward, error) {
	var (
		args  []any
		conds []string
	)
	if f.ReferrerCustomerID != nil {
		args = append(args, *f.ReferrerCustomerID)
		conds = append(conds, "referrer_customer_id = $"+itoa(len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "status = $"+itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	sql := `
		SELECT id, referral_id, referrer_customer_id, referee_customer_id,
		       order_id, invoice_id, amount, status, paid_at,
		       COALESCE(notes,''), created_at
		FROM billing.referral_rewards` + where +
		" ORDER BY created_at DESC LIMIT $" + itoa(len(args)+1)
	args = append(args, f.Limit)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "referral.list", "list referrals", err)
	}
	defer rows.Close()
	out := []domain.ReferralReward{}
	for rows.Next() {
		var (
			x      domain.ReferralReward
			status string
		)
		if err := rows.Scan(&x.ID, &x.ReferralID, &x.ReferrerCustomerID,
			&x.RefereeCustomerID, &x.OrderID, &x.InvoiceID, &x.Amount,
			&status, &x.PaidAt, &x.Notes, &x.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "referral.scan", "scan referral", err)
		}
		x.Status = domain.ReferralRewardStatus(status)
		out = append(out, x)
	}
	return out, nil
}
