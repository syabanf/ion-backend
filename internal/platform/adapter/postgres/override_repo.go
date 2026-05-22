package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// OverrideRepository is the pgx-backed implementation of
// port.OverrideRepository.
type OverrideRepository struct {
	pool *pgxpool.Pool
}

func NewOverrideRepository(pool *pgxpool.Pool) *OverrideRepository {
	return &OverrideRepository{pool: pool}
}

var _ port.OverrideRepository = (*OverrideRepository)(nil)

const overrideCols = `
	id, customer_id, schema_kind, schema_id, schema_code, patch, reason,
	valid_from, valid_until, revision, created_by, created_at, updated_at
`

// Upsert writes the override row using INSERT … ON CONFLICT to handle
// the (customer_id, schema_kind) unique constraint atomically. On
// conflict, we bump revision and refresh the patch / pin / reason —
// matches the "one override per kind per customer" rule from migration
// 0032's UNIQUE (customer_id, schema_kind).
func (r *OverrideRepository) Upsert(ctx context.Context, o *domain.CustomerSchemaOverride) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO platform.customer_schema_overrides
			(id, customer_id, schema_kind, schema_id, schema_code, patch, reason,
			 valid_from, valid_until, revision, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (customer_id, schema_kind) DO UPDATE
		SET schema_id   = EXCLUDED.schema_id,
		    schema_code = EXCLUDED.schema_code,
		    patch       = EXCLUDED.patch,
		    reason      = EXCLUDED.reason,
		    valid_from  = EXCLUDED.valid_from,
		    valid_until = EXCLUDED.valid_until,
		    revision    = platform.customer_schema_overrides.revision + 1,
		    updated_at  = NOW()
	`,
		o.ID, o.CustomerID, string(o.SchemaKind), o.SchemaID, o.SchemaCode,
		[]byte(o.Patch), o.Reason, o.ValidFrom, o.ValidUntil, o.Revision,
		o.CreatedBy, o.CreatedAt, o.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "schema_override", "upsert override")
	}
	return nil
}

func (r *OverrideRepository) FindByCustomerAndKind(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*domain.CustomerSchemaOverride, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+overrideCols+`
		FROM platform.customer_schema_overrides
		WHERE customer_id = $1 AND schema_kind = $2
	`,
		customerID, string(kind),
	)
	o, err := scanOverride(row)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OverrideRepository) ListByCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.CustomerSchemaOverride, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+overrideCols+`
		FROM platform.customer_schema_overrides
		WHERE customer_id = $1
		ORDER BY schema_kind
	`, customerID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "schema_override.list", "list overrides", err)
	}
	defer rows.Close()
	out := []domain.CustomerSchemaOverride{}
	for rows.Next() {
		o, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *OverrideRepository) Delete(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM platform.customer_schema_overrides
		WHERE customer_id = $1 AND schema_kind = $2
	`,
		customerID, string(kind),
	)
	if err != nil {
		return mapDBError(err, "schema_override", "delete override")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("schema_override.not_found", "override not found")
	}
	return nil
}

func scanOverride(row pgx.Row) (domain.CustomerSchemaOverride, error) {
	var (
		o        domain.CustomerSchemaOverride
		kind     string
		patchRaw []byte
	)
	err := row.Scan(
		&o.ID, &o.CustomerID, &kind, &o.SchemaID, &o.SchemaCode,
		&patchRaw, &o.Reason, &o.ValidFrom, &o.ValidUntil, &o.Revision,
		&o.CreatedBy, &o.CreatedAt, &o.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomerSchemaOverride{}, derrors.NotFound("schema_override.not_found", "override not found")
	}
	if err != nil {
		return domain.CustomerSchemaOverride{}, derrors.Wrap(derrors.KindInternal, "schema_override.scan", "scan override", err)
	}
	o.SchemaKind = domain.SchemaKind(kind)
	if len(patchRaw) > 0 {
		buf := make([]byte, len(patchRaw))
		copy(buf, patchRaw)
		o.Patch = buf
	} else {
		o.Patch = []byte("{}")
	}
	return o, nil
}
