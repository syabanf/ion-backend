// Package postgres implements driven adapters for the HRIS bounded
// context against PostgreSQL.
//
// Conventions:
//   - One repo file per aggregate (employee_repo, event_repo).
//   - Shared error mapping in mapDBError.
//   - All SQL uses positional parameters ($1, $2, ...).
//   - Queries scoped to the `hris.*` schema.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError mirrors the reseller/enterprise/warehouse pattern.
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

// nullableString returns *string for INSERTs where empty maps to NULL.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
