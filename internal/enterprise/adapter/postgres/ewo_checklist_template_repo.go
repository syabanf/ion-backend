package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type EWOChecklistTemplateRepository struct {
	pool *pgxpool.Pool
}

func NewEWOChecklistTemplateRepository(pool *pgxpool.Pool) *EWOChecklistTemplateRepository {
	return &EWOChecklistTemplateRepository{pool: pool}
}

var _ port.EWOChecklistTemplateRepository = (*EWOChecklistTemplateRepository)(nil)

const ewoChecklistTemplateCols = `
	id, code, name, COALESCE(description, ''), active, items,
	created_by, created_at, updated_at
`

func (r *EWOChecklistTemplateRepository) List(ctx context.Context, activeOnly bool) ([]port.EWOChecklistTemplate, error) {
	q := `SELECT ` + ewoChecklistTemplateCols + ` FROM enterprise.ewo_checklist_templates`
	if activeOnly {
		q += ` WHERE active = TRUE`
	}
	q += ` ORDER BY code`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_template_list", "list", err)
	}
	defer rows.Close()
	out := []port.EWOChecklistTemplate{}
	for rows.Next() {
		t, err := scanEWOChecklistTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *EWOChecklistTemplateRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.EWOChecklistTemplate, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+ewoChecklistTemplateCols+` FROM enterprise.ewo_checklist_templates WHERE id = $1`,
		id,
	)
	t, err := scanEWOChecklistTemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *EWOChecklistTemplateRepository) FindByCode(ctx context.Context, code string) (*port.EWOChecklistTemplate, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+ewoChecklistTemplateCols+` FROM enterprise.ewo_checklist_templates WHERE code = $1`,
		code,
	)
	t, err := scanEWOChecklistTemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *EWOChecklistTemplateRepository) Upsert(ctx context.Context, t port.EWOChecklistTemplate) (*port.EWOChecklistTemplate, error) {
	if t.Code == "" {
		return nil, derrors.Validation("ewo_template.code_required", "code is required")
	}
	if t.Name == "" {
		return nil, derrors.Validation("ewo_template.name_required", "name is required")
	}
	if len(t.ItemsJSON) == 0 {
		t.ItemsJSON = []byte("[]")
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO enterprise.ewo_checklist_templates
			(code, name, description, active, items, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (code) DO UPDATE
		   SET name = EXCLUDED.name,
		       description = EXCLUDED.description,
		       active = EXCLUDED.active,
		       items = EXCLUDED.items,
		       updated_at = NOW()
		RETURNING `+ewoChecklistTemplateCols,
		t.Code, t.Name, t.Description, t.Active, t.ItemsJSON, t.CreatedBy,
	)
	out, err := scanEWOChecklistTemplate(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *EWOChecklistTemplateRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM enterprise.ewo_checklist_templates WHERE id = $1`, id)
	if err != nil {
		return mapDBError(err, "ewo_checklist_template", "delete")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo_template.not_found", "template not found")
	}
	return nil
}

func scanEWOChecklistTemplate(row pgx.Row) (port.EWOChecklistTemplate, error) {
	var (
		t  port.EWOChecklistTemplate
		ca time.Time
		ua time.Time
	)
	err := row.Scan(
		&t.ID, &t.Code, &t.Name, &t.Description, &t.Active, &t.ItemsJSON,
		&t.CreatedBy, &ca, &ua,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.EWOChecklistTemplate{}, derrors.NotFound("ewo_template.not_found", "template not found")
	}
	if err != nil {
		return port.EWOChecklistTemplate{}, derrors.Wrap(derrors.KindInternal, "db.ewo_template_scan", "scan", err)
	}
	t.CreatedAt = ca.UTC().Format(time.RFC3339)
	t.UpdatedAt = ua.UTC().Format(time.RFC3339)
	return t, nil
}
