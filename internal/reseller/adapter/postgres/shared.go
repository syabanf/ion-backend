// Package postgres implements driven adapters for the reseller
// bounded context against PostgreSQL.
//
// Same conventions as the enterprise / warehouse / crm postgres
// packages:
//   - One repo file per aggregate.
//   - Shared error mapping for unique / FK / check violations in
//     `mapDBError`.
//   - All SQL uses positional parameters ($1, $2, …) — never string
//     interpolation, even for "trusted" values.
//   - Queries scoped to the `reseller.*` schema.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates well-known Postgres errors into typed domain
// errors so HTTP can map to the right status without inspecting
// strings. Mirror of enterprise/adapter/postgres/shared.go — kept
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
