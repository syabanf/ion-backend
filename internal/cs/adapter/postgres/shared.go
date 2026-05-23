// Package postgres implements driven adapters for the customer-service
// bounded context against PostgreSQL.
//
// Same conventions as the reseller / hris / warehouse postgres
// packages:
//   - One repo file per aggregate.
//   - Shared error mapping for unique / FK / check violations in
//     `mapDBError`.
//   - All SQL uses positional parameters ($1, $2, …) — never string
//     interpolation, even for "trusted" values.
//   - Queries scoped to the `cs.*` schema.
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
// service in a future split and we want zero cross-context imports.
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

// jsonbBytes marshals a map for jsonb columns. Returns "{}" for nil so
// the column never receives a NULL when the column is NOT NULL.
func jsonbBytes(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(m)
}

// jsonbBytesNullable returns nil interface for empty maps so the
// column can be NULL when appropriate (used for nullable jsonb cols).
func jsonbBytesNullable(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// unmarshalJSONBMap reads a possibly-null jsonb column into a map.
func unmarshalJSONBMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
