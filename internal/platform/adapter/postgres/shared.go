// Package postgres implements driven adapters for the platform
// bounded context against PostgreSQL.
//
// Same conventions as enterprise / crm / warehouse:
//   - One repo per aggregate.
//   - Shared error mapping for unique / FK / check violations.
//   - All SQL uses positional parameters ($1, $2, …).
//   - Queries scoped to the platform schema (`platform.*`).
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates well-known Postgres errors into typed domain
// errors so the HTTP adapter can pick the right status code without
// inspecting strings.
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
