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

// ComplianceEvaluationRepository implements
// port.ComplianceEvaluationRepository against
// `partnership.compliance_evaluations`.
type ComplianceEvaluationRepository struct {
	pool *pgxpool.Pool
}

func NewComplianceEvaluationRepository(pool *pgxpool.Pool) *ComplianceEvaluationRepository {
	return &ComplianceEvaluationRepository{pool: pool}
}

var _ port.ComplianceEvaluationRepository = (*ComplianceEvaluationRepository)(nil)

const complianceCols = `
	id, reseller_account_id, agreement_id,
	period_year, period_month,
	threshold_pct, achieved_pct, status,
	COALESCE(reason, ''), evaluated_at
`

func (r *ComplianceEvaluationRepository) Create(ctx context.Context, e *domain.ComplianceEvaluation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO partnership.compliance_evaluations
			(id, reseller_account_id, agreement_id,
			 period_year, period_month,
			 threshold_pct, achieved_pct, status, reason, evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`,
		e.ID, e.ResellerAccountID, e.AgreementID,
		e.PeriodYear, e.PeriodMonth,
		e.ThresholdPct, e.AchievedPct, string(e.Status),
		nullableString(e.Reason), e.EvaluatedAt,
	)
	if err != nil {
		return mapDBError(err, "compliance", "insert compliance evaluation")
	}
	return nil
}

func (r *ComplianceEvaluationRepository) FindByResellerPeriod(ctx context.Context, resellerID uuid.UUID, year, month int) (*domain.ComplianceEvaluation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+complianceCols+`
		FROM partnership.compliance_evaluations
		WHERE reseller_account_id = $1 AND period_year = $2 AND period_month = $3
	`, resellerID, year, month)
	e, err := scanCompliance(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *ComplianceEvaluationRepository) List(ctx context.Context, f port.ComplianceListFilter) ([]domain.ComplianceEvaluation, int, error) {
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
		`SELECT COUNT(*) FROM partnership.compliance_evaluations`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.compliance_count", "count evaluations", err)
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
	sql := `SELECT ` + complianceCols + ` FROM partnership.compliance_evaluations` + where +
		` ORDER BY period_year DESC, period_month DESC, evaluated_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.compliance_list", "list evaluations", err)
	}
	defer rows.Close()

	out := []domain.ComplianceEvaluation{}
	for rows.Next() {
		e, err := scanCompliance(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, nil
}

func scanCompliance(row pgx.Row) (domain.ComplianceEvaluation, error) {
	var e domain.ComplianceEvaluation
	var status string
	err := row.Scan(
		&e.ID, &e.ResellerAccountID, &e.AgreementID,
		&e.PeriodYear, &e.PeriodMonth,
		&e.ThresholdPct, &e.AchievedPct, &status,
		&e.Reason, &e.EvaluatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ComplianceEvaluation{}, derrors.NotFound("compliance.not_found", "compliance evaluation not found")
	}
	if err != nil {
		return domain.ComplianceEvaluation{}, derrors.Wrap(derrors.KindInternal, "db.compliance_scan", "scan compliance", err)
	}
	e.Status = domain.ComplianceStatus(status)
	return e, nil
}
