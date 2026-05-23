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

// SettlementRepository implements port.SettlementRepository against
// `partnership.settlements`.
type SettlementRepository struct {
	pool *pgxpool.Pool
}

func NewSettlementRepository(pool *pgxpool.Pool) *SettlementRepository {
	return &SettlementRepository{pool: pool}
}

var _ port.SettlementRepository = (*SettlementRepository)(nil)

// We carry submission's period via a JOIN at scan time so the row is
// self-describing without an extra round-trip; the period is stored
// on the row implicitly via the submission FK but we also surface it
// on the domain struct for the formula hash + dashboard views.
const settlementCols = `
	s.id, s.submission_id, s.agreement_id, s.agreement_terms_snapshot,
	s.gross_revenue, s.net_revenue, s.revshare_amount, s.tax_amount, s.payable_amount,
	s.formula_hash, s.status,
	COALESCE(s.pdf_url, ''), COALESCE(s.pdf_hash, ''),
	s.approved_by, s.approved_at, s.paid_at,
	s.created_at, s.updated_at,
	sub.period_year, sub.period_month
`

const settlementFrom = `
	FROM partnership.settlements s
	JOIN partnership.monthly_submissions sub ON sub.id = s.submission_id
`

func (r *SettlementRepository) Create(ctx context.Context, s *domain.Settlement) error {
	terms, err := mapToJSONB(s.AgreementTermsSnapshot)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO partnership.settlements
			(id, submission_id, agreement_id, agreement_terms_snapshot,
			 gross_revenue, net_revenue, revshare_amount, tax_amount, payable_amount,
			 formula_hash, status,
			 pdf_url, pdf_hash,
			 approved_by, approved_at, paid_at,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
	`,
		s.ID, s.SubmissionID, s.AgreementID, string(terms),
		s.GrossRevenue, s.NetRevenue, s.RevshareAmount, s.TaxAmount, s.PayableAmount,
		s.FormulaHash, string(s.Status),
		nullableString(s.PDFURL), nullableString(s.PDFHash),
		s.ApprovedBy, s.ApprovedAt, s.PaidAt,
		s.CreatedAt, s.UpdatedAt,
	); err != nil {
		return mapDBError(err, "settlement", "insert settlement")
	}
	return nil
}

func (r *SettlementRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Settlement, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+settlementCols+settlementFrom+` WHERE s.id = $1`, id)
	st, err := scanSettlement(row)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (r *SettlementRepository) FindBySubmission(ctx context.Context, submissionID uuid.UUID) (*domain.Settlement, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+settlementCols+settlementFrom+` WHERE s.submission_id = $1`, submissionID)
	st, err := scanSettlement(row)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (r *SettlementRepository) List(ctx context.Context, f port.SettlementListFilter) ([]domain.Settlement, int, error) {
	var wh []string
	var args []any
	if f.ResellerAccountID != nil {
		args = append(args, *f.ResellerAccountID)
		wh = append(wh, fmt.Sprintf("sub.reseller_account_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("s.status = $%d", len(args)))
	}
	if f.PeriodYear != nil {
		args = append(args, *f.PeriodYear)
		wh = append(wh, fmt.Sprintf("sub.period_year = $%d", len(args)))
	}
	if f.PeriodMonth != nil {
		args = append(args, *f.PeriodMonth)
		wh = append(wh, fmt.Sprintf("sub.period_month = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) `+settlementFrom+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.settlement_count", "count settlements", err)
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
	sql := `SELECT ` + settlementCols + settlementFrom + where +
		` ORDER BY s.created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.settlement_list", "list settlements", err)
	}
	defer rows.Close()

	out := []domain.Settlement{}
	for rows.Next() {
		st, err := scanSettlement(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, st)
	}
	return out, total, nil
}

func (r *SettlementRepository) UpdateStatus(ctx context.Context, s *domain.Settlement) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE partnership.settlements
		SET status = $2,
		    approved_by = $3,
		    approved_at = $4,
		    paid_at = $5,
		    updated_at = NOW()
		WHERE id = $1
	`,
		s.ID, string(s.Status),
		s.ApprovedBy, s.ApprovedAt, s.PaidAt,
	)
	if err != nil {
		return mapDBError(err, "settlement", "update settlement status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("settlement.not_found", "settlement not found")
	}
	return nil
}

func (r *SettlementRepository) UpdatePDF(ctx context.Context, id uuid.UUID, url, hash string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE partnership.settlements
		SET pdf_url = $2, pdf_hash = $3, updated_at = NOW()
		WHERE id = $1
	`, id, nullableString(url), nullableString(hash))
	if err != nil {
		return mapDBError(err, "settlement", "update settlement pdf")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("settlement.not_found", "settlement not found")
	}
	return nil
}

func scanSettlement(row pgx.Row) (domain.Settlement, error) {
	var s domain.Settlement
	var status string
	var termsRaw []byte
	err := row.Scan(
		&s.ID, &s.SubmissionID, &s.AgreementID, &termsRaw,
		&s.GrossRevenue, &s.NetRevenue, &s.RevshareAmount, &s.TaxAmount, &s.PayableAmount,
		&s.FormulaHash, &status,
		&s.PDFURL, &s.PDFHash,
		&s.ApprovedBy, &s.ApprovedAt, &s.PaidAt,
		&s.CreatedAt, &s.UpdatedAt,
		&s.PeriodYear, &s.PeriodMonth,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Settlement{}, derrors.NotFound("settlement.not_found", "settlement not found")
	}
	if err != nil {
		return domain.Settlement{}, derrors.Wrap(derrors.KindInternal, "db.settlement_scan", "scan settlement", err)
	}
	s.Status = domain.SettlementStatus(status)
	s.AgreementTermsSnapshot, err = jsonbToMap(termsRaw)
	if err != nil {
		return domain.Settlement{}, err
	}
	return s, nil
}
