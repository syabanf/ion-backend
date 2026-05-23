// Wave 116 — ValidationResultRepository implementation.
//
// Append-only writes to platform.schema_validation_results; the read
// path returns the latest row for a given schema_version_id ordered by
// validated_at desc. Postgres jsonb columns (errors / warnings) are
// stored as JSON arrays of strings — we marshal/unmarshal at the
// adapter boundary so callers stay in []string land.

package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ValidationResultRepository struct {
	pool *pgxpool.Pool
}

func NewValidationResultRepository(pool *pgxpool.Pool) *ValidationResultRepository {
	return &ValidationResultRepository{pool: pool}
}

var _ port.ValidationResultRepository = (*ValidationResultRepository)(nil)

func (r *ValidationResultRepository) Insert(ctx context.Context, row *port.ValidationResultRow) error {
	if row == nil {
		return derrors.Validation("validation.row_required", "row is required")
	}
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	errsJSON, err := json.Marshal(row.Errors)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "validation.marshal_errors", "marshal errors", err)
	}
	warnsJSON, err := json.Marshal(row.Warnings)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "validation.marshal_warnings", "marshal warnings", err)
	}
	triggeredBy := row.TriggeredBy
	if triggeredBy == "" {
		triggeredBy = "manual"
	}
	validatorVersion := row.ValidatorVersion
	if validatorVersion == "" {
		validatorVersion = "v1.0"
	}
	_, dbErr := r.pool.Exec(ctx, `
		INSERT INTO platform.schema_validation_results
			(id, schema_version_id, validated_at, is_valid, errors, warnings, validator_version, triggered_by)
		VALUES ($1, $2, COALESCE($3, NOW()), $4, $5::jsonb, $6::jsonb, $7, $8)
	`,
		row.ID, row.SchemaVersionID, nullableTime(row.ValidatedAt),
		row.IsValid, string(errsJSON), string(warnsJSON),
		validatorVersion, triggeredBy,
	)
	if dbErr != nil {
		return mapDBError(dbErr, "validation", "insert validation result")
	}
	return nil
}

func (r *ValidationResultRepository) LatestForSchema(ctx context.Context, schemaVersionID uuid.UUID) (*port.ValidationResultRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, schema_version_id, validated_at, is_valid, errors, warnings,
		       validator_version, triggered_by
		FROM platform.schema_validation_results
		WHERE schema_version_id = $1
		ORDER BY validated_at DESC
		LIMIT 1
	`, schemaVersionID)

	var (
		out       port.ValidationResultRow
		errsRaw   []byte
		warnsRaw  []byte
	)
	err := row.Scan(
		&out.ID, &out.SchemaVersionID, &out.ValidatedAt, &out.IsValid,
		&errsRaw, &warnsRaw, &out.ValidatorVersion, &out.TriggeredBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("validation.not_found", "no validation result for schema")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "validation.scan", "scan validation result", err)
	}
	if len(errsRaw) > 0 {
		if err := json.Unmarshal(errsRaw, &out.Errors); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "validation.unmarshal_errors", "unmarshal errors", err)
		}
	}
	if len(warnsRaw) > 0 {
		if err := json.Unmarshal(warnsRaw, &out.Warnings); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "validation.unmarshal_warnings", "unmarshal warnings", err)
		}
	}
	return &out, nil
}

// nullableTime returns nil for a zero time so the SQL COALESCE picks
// NOW() instead of inserting epoch.
func nullableTime(t interface{}) interface{} {
	if v, ok := t.(interface{ IsZero() bool }); ok && v.IsZero() {
		return nil
	}
	return t
}
