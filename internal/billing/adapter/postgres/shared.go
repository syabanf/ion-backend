// Package postgres holds driven adapters for the billing context.
package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	derrors "github.com/ion-core/backend/pkg/errors"
)

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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
