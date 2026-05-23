// Package postgres implements driven adapters for the invoicesvc
// bounded context against PostgreSQL.
//
// Conventions (mirrors payment / reseller postgres packages):
//   - One repo file per aggregate; one cross-context reader file for
//     the SQL-only InvoiceReader.
//   - Shared error mapping for unique / FK / check violations.
//   - All SQL uses positional parameters ($1, $2, …).
//   - Queries scoped to the `invoicesvc.*` schema except for the
//     InvoiceReader, which deliberately reaches into `billing.*` /
//     `enterprise.*` to keep invoicesvc free of cross-context Go
//     imports.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// mapDBError translates well-known Postgres errors into typed domain
// errors. Mirror of payment/adapter/postgres/shared.go.
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

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableUUID(u *string) any {
	if u == nil || *u == "" {
		return nil
	}
	return *u
}
