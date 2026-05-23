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

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CompanyTaxProfileRepository implements `port.CompanyTaxProfileRepository`
// against `tax.company_tax_profiles`.
type CompanyTaxProfileRepository struct {
	pool *pgxpool.Pool
}

func NewCompanyTaxProfileRepository(pool *pgxpool.Pool) *CompanyTaxProfileRepository {
	return &CompanyTaxProfileRepository{pool: pool}
}

var _ port.CompanyTaxProfileRepository = (*CompanyTaxProfileRepository)(nil)

const profileCols = `
	id, subsidiary_id, name, npwp, is_pkp,
	ppn_rate, pph23_rate, pph_final_rate,
	effective_from, effective_to,
	created_at, updated_at
`

func (r *CompanyTaxProfileRepository) Create(ctx context.Context, p *domain.CompanyTaxProfile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tax.company_tax_profiles
			(id, subsidiary_id, name, npwp, is_pkp,
			 ppn_rate, pph23_rate, pph_final_rate,
			 effective_from, effective_to,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`,
		p.ID, p.SubsidiaryID, p.Name, p.NPWP, p.IsPKP,
		p.PPNRate, p.PPh23Rate, p.PPhFinalRate,
		p.EffectiveFrom, p.EffectiveTo,
		p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "tax_profile", "insert company tax profile")
	}
	return nil
}

func (r *CompanyTaxProfileRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.CompanyTaxProfile, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+profileCols+` FROM tax.company_tax_profiles WHERE id = $1`, id)
	p, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// FindActiveBySubsidiary returns the profile with the latest
// effective_from <= at AND (effective_to IS NULL OR effective_to >=
// at). NotFound when nothing matches.
func (r *CompanyTaxProfileRepository) FindActiveBySubsidiary(
	ctx context.Context,
	subsidiaryID uuid.UUID,
	at time.Time,
) (*domain.CompanyTaxProfile, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+profileCols+`
		FROM tax.company_tax_profiles
		WHERE subsidiary_id = $1
		  AND effective_from <= $2
		  AND (effective_to IS NULL OR effective_to >= $2)
		ORDER BY effective_from DESC
		LIMIT 1
	`, subsidiaryID, at)
	p, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *CompanyTaxProfileRepository) Update(ctx context.Context, p *domain.CompanyTaxProfile) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tax.company_tax_profiles
		SET name = $2,
		    npwp = $3,
		    is_pkp = $4,
		    ppn_rate = $5,
		    pph23_rate = $6,
		    pph_final_rate = $7,
		    effective_from = $8,
		    effective_to = $9,
		    updated_at = NOW()
		WHERE id = $1
	`,
		p.ID, p.Name, p.NPWP, p.IsPKP,
		p.PPNRate, p.PPh23Rate, p.PPhFinalRate,
		p.EffectiveFrom, p.EffectiveTo,
	)
	if err != nil {
		return mapDBError(err, "tax_profile", "update company tax profile")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("tax_profile.not_found", "company tax profile not found")
	}
	return nil
}

func (r *CompanyTaxProfileRepository) List(
	ctx context.Context,
	f port.CompanyTaxProfileFilter,
) ([]domain.CompanyTaxProfile, int, error) {
	var wh []string
	var args []any
	if f.SubsidiaryID != nil {
		args = append(args, *f.SubsidiaryID)
		wh = append(wh, fmt.Sprintf("subsidiary_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tax.company_tax_profiles`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal,
			"db.tax_profile_count", "count company tax profiles", err)
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
	sql := `SELECT ` + profileCols +
		` FROM tax.company_tax_profiles` + where +
		` ORDER BY subsidiary_id, effective_from DESC LIMIT $` +
		fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal,
			"db.tax_profile_list", "list company tax profiles", err)
	}
	defer rows.Close()

	out := []domain.CompanyTaxProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, nil
}

func scanProfile(row pgx.Row) (domain.CompanyTaxProfile, error) {
	var p domain.CompanyTaxProfile
	err := row.Scan(
		&p.ID, &p.SubsidiaryID, &p.Name, &p.NPWP, &p.IsPKP,
		&p.PPNRate, &p.PPh23Rate, &p.PPhFinalRate,
		&p.EffectiveFrom, &p.EffectiveTo,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CompanyTaxProfile{}, derrors.NotFound(
			"tax_profile.not_found", "company tax profile not found")
	}
	if err != nil {
		return domain.CompanyTaxProfile{}, derrors.Wrap(derrors.KindInternal,
			"db.tax_profile_scan", "scan company tax profile", err)
	}
	return p, nil
}
