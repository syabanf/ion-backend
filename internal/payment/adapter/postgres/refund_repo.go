package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// RefundRepository implements port.RefundRepository against
// `payment.refunds`.
type RefundRepository struct {
	pool *pgxpool.Pool
}

func NewRefundRepository(pool *pgxpool.Pool) *RefundRepository {
	return &RefundRepository{pool: pool}
}

var _ port.RefundRepository = (*RefundRepository)(nil)

const refundCols = `
	id, payment_intent_id, amount,
	COALESCE(reason, ''), status, external_refund_ref,
	requested_by, approved_by, approved_at, completed_at,
	created_at, updated_at
`

func (r *RefundRepository) Create(ctx context.Context, rf *domain.Refund) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payment.refunds
			(id, payment_intent_id, amount, reason, status,
			 external_refund_ref, requested_by, approved_by,
			 approved_at, completed_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		rf.ID, rf.PaymentIntentID, rf.Amount,
		nullableString(rf.Reason), string(rf.Status),
		rf.ExternalRefundRef, rf.RequestedBy, rf.ApprovedBy,
		rf.ApprovedAt, rf.CompletedAt, rf.CreatedAt, rf.UpdatedAt,
	)
	return mapDBError(err, "refund", "insert refund")
}

func (r *RefundRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Refund, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+refundCols+` FROM payment.refunds WHERE id = $1`, id)
	rf, err := scanRefund(row)
	if err != nil {
		return nil, err
	}
	return &rf, nil
}

func (r *RefundRepository) List(ctx context.Context, f port.RefundListFilter) ([]domain.Refund, int, error) {
	var wh []string
	var args []any
	if f.PaymentIntentID != nil {
		args = append(args, *f.PaymentIntentID)
		wh = append(wh, fmt.Sprintf("payment_intent_id = $%d", len(args)))
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
		`SELECT COUNT(*) FROM payment.refunds`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.refund_count", "count refunds", err)
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
	sql := `SELECT ` + refundCols + ` FROM payment.refunds` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.refund_list", "list refunds", err)
	}
	defer rows.Close()
	out := []domain.Refund{}
	for rows.Next() {
		rf, err := scanRefund(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rf)
	}
	return out, total, nil
}

func (r *RefundRepository) Update(ctx context.Context, rf *domain.Refund) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.refunds
		SET amount = $2,
		    reason = $3,
		    status = $4,
		    external_refund_ref = $5,
		    requested_by = $6,
		    approved_by = $7,
		    approved_at = $8,
		    completed_at = $9,
		    updated_at = NOW()
		WHERE id = $1
	`,
		rf.ID, rf.Amount, nullableString(rf.Reason), string(rf.Status),
		rf.ExternalRefundRef, rf.RequestedBy, rf.ApprovedBy,
		rf.ApprovedAt, rf.CompletedAt,
	)
	if err != nil {
		return mapDBError(err, "refund", "update refund")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("refund.not_found", "refund not found")
	}
	return nil
}

func (r *RefundRepository) SumCompletedForIntent(ctx context.Context, intentID uuid.UUID) (float64, error) {
	var sum float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM payment.refunds
		WHERE payment_intent_id = $1 AND status = 'completed'
	`, intentID).Scan(&sum)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.refund_sum", "sum refunds", err)
	}
	return sum, nil
}

func scanRefund(row pgx.Row) (domain.Refund, error) {
	var rf domain.Refund
	var status string
	err := row.Scan(
		&rf.ID, &rf.PaymentIntentID, &rf.Amount,
		&rf.Reason, &status, &rf.ExternalRefundRef,
		&rf.RequestedBy, &rf.ApprovedBy,
		&rf.ApprovedAt, &rf.CompletedAt,
		&rf.CreatedAt, &rf.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.Refund{}, derrors.NotFound("refund.not_found", "refund not found")
	}
	if err != nil {
		return domain.Refund{}, derrors.Wrap(derrors.KindInternal, "db.refund_scan", "scan refund", err)
	}
	rf.Status = domain.RefundStatus(status)
	return rf, nil
}
