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

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// AgreementRepository implements port.AgreementRepository against
// `partnership.agreements`.
type AgreementRepository struct {
	pool *pgxpool.Pool
}

func NewAgreementRepository(pool *pgxpool.Pool) *AgreementRepository {
	return &AgreementRepository{pool: pool}
}

var _ port.AgreementRepository = (*AgreementRepository)(nil)

const agreementCols = `
	id, reseller_account_id, terms_json,
	revshare_pct, ramp_months, compliance_threshold_pct,
	effective_from, effective_to,
	signed_by, signed_at,
	created_at, updated_at
`

func (r *AgreementRepository) Create(ctx context.Context, a *domain.Agreement) error {
	terms, err := mapToJSONB(a.TermsJSON)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO partnership.agreements
			(id, reseller_account_id, terms_json,
			 revshare_pct, ramp_months, compliance_threshold_pct,
			 effective_from, effective_to,
			 signed_by, signed_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		a.ID, a.ResellerAccountID, string(terms),
		a.RevsharePct, a.RampMonths, a.ComplianceThresholdPct,
		a.EffectiveFrom, a.EffectiveTo,
		a.SignedBy, a.SignedAt,
		a.CreatedAt, a.UpdatedAt,
	); err != nil {
		return mapDBError(err, "agreement", "insert agreement")
	}
	return nil
}

func (r *AgreementRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Agreement, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+agreementCols+` FROM partnership.agreements WHERE id = $1`, id)
	a, err := scanAgreement(row)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AgreementRepository) FindActive(ctx context.Context, resellerID uuid.UUID, at time.Time) (*domain.Agreement, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+agreementCols+`
		FROM partnership.agreements
		WHERE reseller_account_id = $1
		  AND effective_from <= $2
		  AND (effective_to IS NULL OR effective_to >= $2)
		ORDER BY effective_from DESC
		LIMIT 1
	`, resellerID, at)
	a, err := scanAgreement(row)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AgreementRepository) List(ctx context.Context, f port.AgreementListFilter) ([]domain.Agreement, int, error) {
	var wh []string
	var args []any
	if f.ResellerAccountID != nil {
		args = append(args, *f.ResellerAccountID)
		wh = append(wh, fmt.Sprintf("reseller_account_id = $%d", len(args)))
	}
	if f.ActiveAt != nil {
		args = append(args, *f.ActiveAt)
		wh = append(wh, fmt.Sprintf("effective_from <= $%d", len(args)))
		args = append(args, *f.ActiveAt)
		wh = append(wh, fmt.Sprintf("(effective_to IS NULL OR effective_to >= $%d)", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM partnership.agreements`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.agreement_count", "count agreements", err)
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
	sql := `SELECT ` + agreementCols + ` FROM partnership.agreements` + where +
		` ORDER BY effective_from DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.agreement_list", "list agreements", err)
	}
	defer rows.Close()

	out := []domain.Agreement{}
	for rows.Next() {
		a, err := scanAgreement(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, nil
}

func (r *AgreementRepository) Update(ctx context.Context, a *domain.Agreement) error {
	terms, err := mapToJSONB(a.TermsJSON)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE partnership.agreements
		SET terms_json = $2::jsonb,
		    revshare_pct = $3,
		    ramp_months = $4,
		    compliance_threshold_pct = $5,
		    effective_from = $6,
		    effective_to = $7,
		    signed_by = $8,
		    signed_at = $9,
		    updated_at = NOW()
		WHERE id = $1
	`,
		a.ID, string(terms),
		a.RevsharePct, a.RampMonths, a.ComplianceThresholdPct,
		a.EffectiveFrom, a.EffectiveTo,
		a.SignedBy, a.SignedAt,
	)
	if err != nil {
		return mapDBError(err, "agreement", "update agreement")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("agreement.not_found", "agreement not found")
	}
	return nil
}

func (r *AgreementRepository) ListResellersWithActiveAgreement(ctx context.Context, at time.Time) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT reseller_account_id
		FROM partnership.agreements
		WHERE effective_from <= $1
		  AND (effective_to IS NULL OR effective_to >= $1)
	`, at)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.agreement_active_resellers", "list active resellers", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.agreement_active_resellers_scan", "scan reseller id", err)
		}
		out = append(out, id)
	}
	return out, nil
}

func scanAgreement(row pgx.Row) (domain.Agreement, error) {
	var a domain.Agreement
	var termsRaw []byte
	err := row.Scan(
		&a.ID, &a.ResellerAccountID, &termsRaw,
		&a.RevsharePct, &a.RampMonths, &a.ComplianceThresholdPct,
		&a.EffectiveFrom, &a.EffectiveTo,
		&a.SignedBy, &a.SignedAt,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Agreement{}, derrors.NotFound("agreement.not_found", "agreement not found")
	}
	if err != nil {
		return domain.Agreement{}, derrors.Wrap(derrors.KindInternal, "db.agreement_scan", "scan agreement", err)
	}
	a.TermsJSON, err = jsonbToMap(termsRaw)
	if err != nil {
		return domain.Agreement{}, err
	}
	return a, nil
}
