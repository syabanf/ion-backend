package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PolicyRepository struct {
	pool *pgxpool.Pool
}

func NewPolicyRepository(pool *pgxpool.Pool) *PolicyRepository {
	return &PolicyRepository{pool: pool}
}

var _ port.PolicyRepository = (*PolicyRepository)(nil)

func (r *PolicyRepository) Get(ctx context.Context) (*domain.Policy, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT late_fee_grace_days, late_fee_amount,
		       suspend_after_days, terminate_after_suspended_days,
		       notify_customer_days_before, updated_by, updated_at
		FROM billing.policies WHERE id = 1
	`)
	var p domain.Policy
	err := row.Scan(&p.LateFeeGraceDays, &p.LateFeeAmount,
		&p.SuspendAfterDays, &p.TerminateAfterSuspendedDays,
		&p.NotifyCustomerDaysBefore, &p.UpdatedBy, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Should not happen — the migration seeds it. Return defaults if it does.
		return &domain.Policy{LateFeeGraceDays: 3, LateFeeAmount: 25000,
			SuspendAfterDays: 14, TerminateAfterSuspendedDays: 30,
			NotifyCustomerDaysBefore: 7}, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "policy.get", "read policy", err)
	}
	return &p, nil
}

// Update applies a partial-patch: only non-nil fields change.
func (r *PolicyRepository) Update(ctx context.Context, in port.UpdatePolicyInput) (*domain.Policy, error) {
	// Read-modify-write so partial updates only touch supplied fields.
	cur, err := r.Get(ctx)
	if err != nil {
		return nil, err
	}
	if in.LateFeeGraceDays != nil {
		cur.LateFeeGraceDays = *in.LateFeeGraceDays
	}
	if in.LateFeeAmount != nil {
		cur.LateFeeAmount = *in.LateFeeAmount
	}
	if in.SuspendAfterDays != nil {
		cur.SuspendAfterDays = *in.SuspendAfterDays
	}
	if in.TerminateAfterSuspendedDays != nil {
		cur.TerminateAfterSuspendedDays = *in.TerminateAfterSuspendedDays
	}
	if in.NotifyCustomerDaysBefore != nil {
		cur.NotifyCustomerDaysBefore = *in.NotifyCustomerDaysBefore
	}
	by := in.UpdatedBy
	_, err = r.pool.Exec(ctx, `
		UPDATE billing.policies SET
		  late_fee_grace_days = $1,
		  late_fee_amount = $2,
		  suspend_after_days = $3,
		  terminate_after_suspended_days = $4,
		  notify_customer_days_before = $5,
		  updated_by = $6,
		  updated_at = NOW()
		WHERE id = 1
	`, cur.LateFeeGraceDays, cur.LateFeeAmount, cur.SuspendAfterDays,
		cur.TerminateAfterSuspendedDays, cur.NotifyCustomerDaysBefore, by)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "policy.update", "update policy", err)
	}
	return r.Get(ctx)
}
