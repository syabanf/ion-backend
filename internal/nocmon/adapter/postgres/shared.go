// Package postgres implements driven adapters for the NOC monitoring
// bounded context against PostgreSQL.
//
// Same conventions as the reseller / enterprise postgres packages:
//   - One repo file per aggregate.
//   - Shared error mapping for unique / FK / check violations in
//     `mapDBError`.
//   - All SQL uses positional parameters ($1, $2, …).
//   - Queries scoped to the `nocmon.*` schema.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates Postgres SQLSTATEs into typed domain errors.
// Kept local (not imported from sibling packages) because this
// bounded context will move to its own service binary
// (cmd/nocmon-svc) — zero cross-context imports.
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

// nullableString turns "" into NULL on insert/update, mirroring the
// reseller / enterprise convention. Read-side uses COALESCE so the
// domain stays free of *string juggling.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
