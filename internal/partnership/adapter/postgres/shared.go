// Package postgres implements driven adapters for the partnership
// bounded context against PostgreSQL.
//
// Same conventions as the reseller / enterprise / warehouse postgres
// packages:
//   - One repo file per aggregate.
//   - Shared error mapping for unique / FK / check violations in
//     `mapDBError`.
//   - All SQL uses positional parameters ($1, $2, …) — never string
//     interpolation, even for "trusted" values.
//   - Queries scoped to the `partnership.*` schema.
package postgres

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates well-known Postgres errors into typed domain
// errors so HTTP can map to the right status without inspecting
// strings. Mirror of reseller/adapter/postgres/shared.go — kept
// separate (not imported) because this package will move to its own
// service binary and we want zero cross-context imports.
func mapDBError(err error, code, msg string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return derrors.Conflict(code+".duplicate", msg+" — already exists")
		case "23503":
			return derrors.Validation(code+".bad_fk", msg+" — referenced record not found")
		case "23514":
			return derrors.Validation(code+".invalid", msg+" — violates a database constraint")
		}
	}
	return derrors.Wrap(derrors.KindInternal, code, msg, err)
}

// jsonbToMap decodes a JSONB byte payload into map[string]any. Returns
// an empty map for nil/empty input. Used by every repo that reads a
// JSONB column.
func jsonbToMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.jsonb_decode", "decode jsonb", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// mapToJSONB encodes a Go map back into a JSONB-compatible byte slice
// for INSERT/UPDATE binding.
func mapToJSONB(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.jsonb_encode", "encode jsonb", err)
	}
	return b, nil
}

// nullableString returns nil for the empty string so we don't write
// empty strings into nullable TEXT columns. The scan side uses
// COALESCE(..., '') to read them back as empty strings.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
