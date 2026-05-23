package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// MonthlySubmissionRepository implements port.MonthlySubmissionRepository
// against `partnership.monthly_submissions`.
type MonthlySubmissionRepository struct {
	pool *pgxpool.Pool
}

func NewMonthlySubmissionRepository(pool *pgxpool.Pool) *MonthlySubmissionRepository {
	return &MonthlySubmissionRepository{pool: pool}
}

var _ port.MonthlySubmissionRepository = (*MonthlySubmissionRepository)(nil)

const submissionCols = `
	id, agreement_id, reseller_account_id,
	period_year, period_month, status,
	gross_revenue, net_revenue, subscriber_count, churn_count,
	COALESCE(evidence_url, ''), COALESCE(evidence_hash, ''),
	submitted_by, submitted_at,
	confirmed_by, confirmed_at,
	COALESCE(returned_reason, ''), returned_at,
	created_at, updated_at
`

func (r *MonthlySubmissionRepository) Create(ctx context.Context, s *domain.MonthlySubmission) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO partnership.monthly_submissions
			(id, agreement_id, reseller_account_id,
			 period_year, period_month, status,
			 gross_revenue, net_revenue, subscriber_count, churn_count,
			 evidence_url, evidence_hash,
			 submitted_by, submitted_at,
			 confirmed_by, confirmed_at,
			 returned_reason, returned_at,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`,
		s.ID, s.AgreementID, s.ResellerAccountID,
		s.PeriodYear, s.PeriodMonth, string(s.Status),
		s.GrossRevenue, s.NetRevenue, s.SubscriberCount, s.ChurnCount,
		nullableString(s.EvidenceURL), nullableString(s.EvidenceHash),
		s.SubmittedBy, s.SubmittedAt,
		s.ConfirmedBy, s.ConfirmedAt,
		nullableString(s.ReturnedReason), s.ReturnedAt,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "submission", "insert monthly submission")
	}
	return nil
}

func (r *MonthlySubmissionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.MonthlySubmission, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+submissionCols+` FROM partnership.monthly_submissions WHERE id = $1`, id)
	s, err := scanSubmission(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *MonthlySubmissionRepository) FindByResellerPeriod(ctx context.Context, resellerID uuid.UUID, year, month int) (*domain.MonthlySubmission, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+submissionCols+`
		FROM partnership.monthly_submissions
		WHERE reseller_account_id = $1 AND period_year = $2 AND period_month = $3
	`, resellerID, year, month)
	s, err := scanSubmission(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *MonthlySubmissionRepository) List(ctx context.Context, f port.SubmissionListFilter) ([]domain.MonthlySubmission, int, error) {
	var wh []string
	var args []any
	if f.ResellerAccountID != nil {
		args = append(args, *f.ResellerAccountID)
		wh = append(wh, fmt.Sprintf("reseller_account_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.PeriodYear != nil {
		args = append(args, *f.PeriodYear)
		wh = append(wh, fmt.Sprintf("period_year = $%d", len(args)))
	}
	if f.PeriodMonth != nil {
		args = append(args, *f.PeriodMonth)
		wh = append(wh, fmt.Sprintf("period_month = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM partnership.monthly_submissions`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.submission_count", "count submissions", err)
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
	sql := `SELECT ` + submissionCols + ` FROM partnership.monthly_submissions` + where +
		` ORDER BY period_year DESC, period_month DESC, created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.submission_list", "list submissions", err)
	}
	defer rows.Close()

	out := []domain.MonthlySubmission{}
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

// UpdateFields persists the editable revenue + evidence columns. Used
// by PATCH on draft / returned submissions; the lifecycle columns
// (status, *_at, *_by, returned_reason) are owned by UpdateStatus.
func (r *MonthlySubmissionRepository) UpdateFields(ctx context.Context, s *domain.MonthlySubmission) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE partnership.monthly_submissions
		SET gross_revenue = $2,
		    net_revenue = $3,
		    subscriber_count = $4,
		    churn_count = $5,
		    evidence_url = $6,
		    evidence_hash = $7,
		    status = $8,
		    updated_at = NOW()
		WHERE id = $1
	`,
		s.ID,
		s.GrossRevenue, s.NetRevenue, s.SubscriberCount, s.ChurnCount,
		nullableString(s.EvidenceURL), nullableString(s.EvidenceHash),
		string(s.Status),
	)
	if err != nil {
		return mapDBError(err, "submission", "update submission fields")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("submission.not_found", "submission not found")
	}
	return nil
}

func (r *MonthlySubmissionRepository) UpdateStatus(ctx context.Context, s *domain.MonthlySubmission) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE partnership.monthly_submissions
		SET status = $2,
		    submitted_by = $3,
		    submitted_at = $4,
		    confirmed_by = $5,
		    confirmed_at = $6,
		    returned_reason = $7,
		    returned_at = $8,
		    updated_at = NOW()
		WHERE id = $1
	`,
		s.ID, string(s.Status),
		s.SubmittedBy, s.SubmittedAt,
		s.ConfirmedBy, s.ConfirmedAt,
		nullableString(s.ReturnedReason), s.ReturnedAt,
	)
	if err != nil {
		return mapDBError(err, "submission", "update submission status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("submission.not_found", "submission not found")
	}
	return nil
}

func (r *MonthlySubmissionRepository) CountConfirmedBefore(ctx context.Context, resellerID uuid.UUID, year, month int) (int, error) {
	// "Before (year, month)" = year < Y OR (year = Y AND month < M).
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM partnership.monthly_submissions
		WHERE reseller_account_id = $1
		  AND status = 'confirmed'
		  AND (period_year < $2 OR (period_year = $2 AND period_month < $3))
	`, resellerID, year, month).Scan(&n)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.submission_count_confirmed", "count confirmed submissions", err)
	}
	return n, nil
}

func scanSubmission(row pgx.Row) (domain.MonthlySubmission, error) {
	var s domain.MonthlySubmission
	var status string
	err := row.Scan(
		&s.ID, &s.AgreementID, &s.ResellerAccountID,
		&s.PeriodYear, &s.PeriodMonth, &status,
		&s.GrossRevenue, &s.NetRevenue, &s.SubscriberCount, &s.ChurnCount,
		&s.EvidenceURL, &s.EvidenceHash,
		&s.SubmittedBy, &s.SubmittedAt,
		&s.ConfirmedBy, &s.ConfirmedAt,
		&s.ReturnedReason, &s.ReturnedAt,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MonthlySubmission{}, derrors.NotFound("submission.not_found", "submission not found")
	}
	if err != nil {
		return domain.MonthlySubmission{}, derrors.Wrap(derrors.KindInternal, "db.submission_scan", "scan submission", err)
	}
	s.Status = domain.SubmissionStatus(status)
	return s, nil
}
