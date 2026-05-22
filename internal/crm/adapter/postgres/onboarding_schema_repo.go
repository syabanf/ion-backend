package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type OnboardingSchemaRepository struct {
	pool *pgxpool.Pool
}

func NewOnboardingSchemaRepository(pool *pgxpool.Pool) *OnboardingSchemaRepository {
	return &OnboardingSchemaRepository{pool: pool}
}

var _ port.OnboardingSchemaRepository = (*OnboardingSchemaRepository)(nil)

const schemaSelect = `
SELECT id, customer_type, product_type, version, content,
       active, COALESCE(notes,''), created_by, created_at, updated_at
FROM crm.onboarding_schemas
`

// FindActive returns the currently active schema for the given pair.
// PRD-leaning behaviour: only one row per (customer_type, product_type)
// should have active=TRUE. If multiple are flagged active (data drift),
// we pick the highest version so newer trumps older.
func (r *OnboardingSchemaRepository) FindActive(ctx context.Context, customerType, productType string) (*domain.OnboardingSchema, error) {
	row := r.pool.QueryRow(ctx, schemaSelect+`
		WHERE customer_type = $1 AND product_type = $2 AND active
		ORDER BY version DESC
		LIMIT 1
	`, customerType, productType)
	return scanSchema(row)
}

func (r *OnboardingSchemaRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.OnboardingSchema, error) {
	row := r.pool.QueryRow(ctx, schemaSelect+" WHERE id = $1", id)
	return scanSchema(row)
}

func (r *OnboardingSchemaRepository) List(ctx context.Context) ([]domain.OnboardingSchema, error) {
	rows, err := r.pool.Query(ctx,
		schemaSelect+" ORDER BY customer_type, product_type, version DESC")
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.schema_list", "list schemas", err)
	}
	defer rows.Close()
	out := []domain.OnboardingSchema{}
	for rows.Next() {
		s, err := scanSchema(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

func scanSchema(row pgx.Row) (*domain.OnboardingSchema, error) {
	var (
		s   domain.OnboardingSchema
		raw []byte
	)
	err := row.Scan(&s.ID, &s.CustomerType, &s.ProductType, &s.Version, &raw,
		&s.Active, &s.Notes, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("schema.not_found", "onboarding schema not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.schema_scan", "scan schema", err)
	}
	c, err := domain.UnmarshalSchemaContent(raw)
	if err != nil {
		return nil, err
	}
	s.Content = c
	return &s, nil
}
