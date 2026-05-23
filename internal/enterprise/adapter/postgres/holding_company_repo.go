package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// HoldingCompanyRepository implements `port.HoldingCompanyRepository`
// against `enterprise.holding_companies`. Wave 92 — read-only surface
// plus a Create entry point for the seed / admin paths.
type HoldingCompanyRepository struct {
	pool *pgxpool.Pool
}

func NewHoldingCompanyRepository(pool *pgxpool.Pool) *HoldingCompanyRepository {
	return &HoldingCompanyRepository{pool: pool}
}

var _ port.HoldingCompanyRepository = (*HoldingCompanyRepository)(nil)

const holdingCompanyCols = `
	id, name, npwp, legal_entity_type, created_at, updated_at
`

func (r *HoldingCompanyRepository) List(ctx context.Context) ([]domain.HoldingCompany, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+holdingCompanyCols+` FROM enterprise.holding_companies ORDER BY name`,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.holding_company_list", "list holding companies", err)
	}
	defer rows.Close()

	out := []domain.HoldingCompany{}
	for rows.Next() {
		h, err := scanHoldingCompany(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

func (r *HoldingCompanyRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.HoldingCompany, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+holdingCompanyCols+` FROM enterprise.holding_companies WHERE id = $1`, id,
	)
	h, err := scanHoldingCompany(row)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (r *HoldingCompanyRepository) Create(ctx context.Context, h *domain.HoldingCompany) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.holding_companies
			(id, name, npwp, legal_entity_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		h.ID, h.Name, h.NPWP, h.LegalEntityType, h.CreatedAt, h.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "holding_company", "insert holding company")
	}
	return nil
}

func scanHoldingCompany(row pgx.Row) (domain.HoldingCompany, error) {
	var h domain.HoldingCompany
	err := row.Scan(
		&h.ID, &h.Name, &h.NPWP, &h.LegalEntityType,
		&h.CreatedAt, &h.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.HoldingCompany{}, derrors.NotFound("holding_company.not_found", "holding company not found")
	}
	if err != nil {
		return domain.HoldingCompany{}, derrors.Wrap(derrors.KindInternal, "db.holding_company_scan", "scan holding company", err)
	}
	return h, nil
}
