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

type SLATemplateRepository struct {
	pool *pgxpool.Pool
}

func NewSLATemplateRepository(pool *pgxpool.Pool) *SLATemplateRepository {
	return &SLATemplateRepository{pool: pool}
}

var _ port.SLATemplateRepository = (*SLATemplateRepository)(nil)

const slaCols = `id, key, name, COALESCE(description,''), COALESCE(details, '{}'::jsonb), active, created_at, updated_at`

func (r *SLATemplateRepository) List(ctx context.Context, activeOnly bool) ([]domain.SLATemplate, error) {
	sql := `SELECT ` + slaCols + ` FROM enterprise.sla_templates`
	if activeOnly {
		sql += ` WHERE active = TRUE`
	}
	sql += ` ORDER BY key`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.sla_list", "list sla templates", err)
	}
	defer rows.Close()
	out := []domain.SLATemplate{}
	for rows.Next() {
		t, err := scanSLATemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *SLATemplateRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SLATemplate, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+slaCols+` FROM enterprise.sla_templates WHERE id = $1`, id)
	t, err := scanSLATemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *SLATemplateRepository) FindByKey(ctx context.Context, key string) (*domain.SLATemplate, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+slaCols+` FROM enterprise.sla_templates WHERE key = $1`, key)
	t, err := scanSLATemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *SLATemplateRepository) Create(ctx context.Context, t *domain.SLATemplate) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.sla_templates
			(id, key, name, description, details, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, t.ID, t.Key, t.Name, t.Description, t.Details, t.Active, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return mapDBError(err, "sla_template", "insert sla template")
	}
	return nil
}

func (r *SLATemplateRepository) Update(ctx context.Context, t *domain.SLATemplate) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.sla_templates
		SET name = $2, description = $3, details = $4, active = $5, updated_at = NOW()
		WHERE id = $1
	`, t.ID, t.Name, t.Description, t.Details, t.Active)
	if err != nil {
		return mapDBError(err, "sla_template", "update sla template")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("sla_template.not_found", "sla template not found")
	}
	return nil
}

func scanSLATemplate(row pgx.Row) (domain.SLATemplate, error) {
	var t domain.SLATemplate
	err := row.Scan(&t.ID, &t.Key, &t.Name, &t.Description, &t.Details, &t.Active, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SLATemplate{}, derrors.NotFound("sla_template.not_found", "sla template not found")
	}
	if err != nil {
		return domain.SLATemplate{}, derrors.Wrap(derrors.KindInternal, "db.sla_scan", "scan sla template", err)
	}
	return t, nil
}
