// Package postgres holds driven adapters for the network bounded context.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapInsertError translates Postgres errors that have stable handling into
// domain errors. Unknown errors stay as Internal wrapping the cause.
//
// SQLSTATE 23505 = unique_violation → Conflict
// SQLSTATE 23503 = foreign_key_violation → Validation (caller sent a bad FK)
// SQLSTATE 23514 = check_violation → Validation
func mapInsertError(err error, code, msg string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return derrors.Conflict(code+".duplicate", msg+" — already exists")
		case "23503":
			return derrors.Validation(code+".bad_fk", msg+" — referenced record not found")
		case "23514":
			return derrors.Validation(code+".invalid", msg+" — value violates a database constraint")
		}
	}
	return derrors.Wrap(derrors.KindInternal, code, msg, err)
}
