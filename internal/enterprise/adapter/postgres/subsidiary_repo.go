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

// SubsidiaryRepository implements `port.SubsidiaryRepository` against
// `enterprise.subsidiaries`. Wave 92 — read surface for the picker
// plus a Create entry point for the seed / admin paths.
type SubsidiaryRepository struct {
	pool *pgxpool.Pool
}

func NewSubsidiaryRepository(pool *pgxpool.Pool) *SubsidiaryRepository {
	return &SubsidiaryRepository{pool: pool}
}

var _ port.SubsidiaryRepository = (*SubsidiaryRepository)(nil)

// Wave 94b — `is_pkp` / `ppn_rate` were dropped from this table in
// migration 0063. Tax stance now lives on `tax.company_tax_profiles`
// (effective-window-aware). See `domain/subsidiary.go` header.
const subsidiaryCols = `
	id, holding_company_id, name, npwp,
	role,
	created_at, updated_at
`

// ListByHolding returns subsidiaries optionally filtered by holding.
// nil filter = return all (cross-holding admin view).
func (r *SubsidiaryRepository) ListByHolding(ctx context.Context, holdingCompanyID *uuid.UUID) ([]domain.Subsidiary, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if holdingCompanyID != nil {
		rows, err = r.pool.Query(ctx,
			`SELECT `+subsidiaryCols+`
			   FROM enterprise.subsidiaries
			  WHERE holding_company_id = $1
			  ORDER BY name`,
			*holdingCompanyID,
		)
	} else {
		rows, err = r.pool.Query(ctx,
			`SELECT `+subsidiaryCols+`
			   FROM enterprise.subsidiaries
			  ORDER BY holding_company_id, name`,
		)
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.subsidiary_list", "list subsidiaries", err)
	}
	defer rows.Close()

	out := []domain.Subsidiary{}
	for rows.Next() {
		s, err := scanSubsidiary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *SubsidiaryRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Subsidiary, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+subsidiaryCols+` FROM enterprise.subsidiaries WHERE id = $1`, id,
	)
	s, err := scanSubsidiary(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SubsidiaryRepository) Create(ctx context.Context, s *domain.Subsidiary) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.subsidiaries
			(id, holding_company_id, name, npwp,
			 role,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		s.ID, s.HoldingCompanyID, s.Name, s.NPWP,
		string(s.Role),
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "subsidiary", "insert subsidiary")
	}
	return nil
}

func scanSubsidiary(row pgx.Row) (domain.Subsidiary, error) {
	var s domain.Subsidiary
	var role string
	err := row.Scan(
		&s.ID, &s.HoldingCompanyID, &s.Name, &s.NPWP,
		&role,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Subsidiary{}, derrors.NotFound("subsidiary.not_found", "subsidiary not found")
	}
	if err != nil {
		return domain.Subsidiary{}, derrors.Wrap(derrors.KindInternal, "db.subsidiary_scan", "scan subsidiary", err)
	}
	s.Role = domain.SubsidiaryRole(role)
	return s, nil
}
