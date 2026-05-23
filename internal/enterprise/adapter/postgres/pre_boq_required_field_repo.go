package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PreBOQRequiredFieldRepository is the Wave 106 driven port for
// `enterprise.pre_boq_required_fields`. Read-only at this wave — the
// admin write surface lands when the FE settings page does. The seed
// data ships in migration 0071.
type PreBOQRequiredFieldRepository struct {
	pool *pgxpool.Pool
}

func NewPreBOQRequiredFieldRepository(pool *pgxpool.Pool) *PreBOQRequiredFieldRepository {
	return &PreBOQRequiredFieldRepository{pool: pool}
}

var _ port.PreBOQRequiredFieldRepository = (*PreBOQRequiredFieldRepository)(nil)

// ListAll returns every config row in `position` order. The list is
// small (~5 rows) so we don't paginate; the validator walks it once
// per Pre-BOQ submit.
func (r *PreBOQRequiredFieldRepository) ListAll(ctx context.Context) ([]domain.PreBOQRequiredField, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, field_key, label, field_type, required, position, created_at, updated_at
		FROM enterprise.pre_boq_required_fields
		ORDER BY position ASC, field_key ASC
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.pre_boq_required_fields_list",
			"list pre_boq required fields", err)
	}
	defer rows.Close()
	out := []domain.PreBOQRequiredField{}
	for rows.Next() {
		var f domain.PreBOQRequiredField
		if err := rows.Scan(
			&f.ID, &f.FieldKey, &f.Label, &f.FieldType, &f.Required, &f.Position,
			&f.CreatedAt, &f.UpdatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.pre_boq_required_fields_scan",
				"scan pre_boq required field", err)
		}
		out = append(out, f)
	}
	return out, nil
}
