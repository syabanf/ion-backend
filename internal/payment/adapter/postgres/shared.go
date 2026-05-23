// Package postgres implements driven adapters for the payment
// bounded context against PostgreSQL.
//
// Conventions (mirrors reseller / enterprise postgres packages):
//   - One repo file per aggregate.
//   - Shared error mapping for unique / FK / check violations in
//     `mapDBError`.
//   - All SQL uses positional parameters ($1, $2, …) — never string
//     interpolation, even for "trusted" values.
//   - Queries scoped to the `payment.*` schema.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates well-known Postgres errors into typed domain
// errors so HTTP can map to the right status without inspecting
// strings. Mirror of reseller/adapter/postgres/shared.go — kept
// separate so this package can move to its own service without
// cross-context imports.
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

// nullableString returns nil for empty strings so we don't write empty
// strings into nullable TEXT columns. The scan side uses COALESCE so
// the domain stays free of string-pointer juggling.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
