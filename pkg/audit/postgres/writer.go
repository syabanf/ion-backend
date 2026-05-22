// Package postgres provides the storage adapter for the audit Writer
// interface — INSERT into identity.audit_logs.
//
// Writes are append-only by table design (no UPDATE/DELETE path); a
// missing user_id is left NULL via the ON DELETE SET NULL FK so old
// rows survive a user purge. The Writer is intentionally narrow so any
// service can construct one from its own pool and pass it down through
// its use cases.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Writer persists audit rows via pgxpool. The pool can be shared with
// the service's main DB pool — the audit_logs table doesn't have heavy
// contention so no separate connection is needed.
type Writer struct {
	pool *pgxpool.Pool
}

func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{pool: pool}
}

var _ audit.Writer = (*Writer)(nil)

// Write inserts one row. Sets timestamp server-side via DEFAULT NOW()
// if the caller didn't supply one, so callers can pass a zero Time
// and get the canonical ingestion timestamp.
func (w *Writer) Write(ctx context.Context, e audit.Entry) error {
	if w == nil || w.pool == nil {
		return nil
	}
	// Treat zero UUID as NULL — the table allows it (ON DELETE SET NULL
	// implies the column is nullable).
	var userID any
	if e.UserID.String() == "00000000-0000-0000-0000-000000000000" {
		userID = nil
	} else {
		userID = e.UserID
	}
	var ts any
	if !e.Timestamp.IsZero() {
		ts = e.Timestamp
	}
	_, err := w.pool.Exec(ctx, `
		INSERT INTO identity.audit_logs
			(timestamp, user_id, module, record_type, record_id,
			 field_changed, before_value, after_value, reason)
		VALUES (COALESCE($1, NOW()), $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		ts, userID,
		e.Module, e.RecordType, e.RecordID,
		nullIfEmpty(e.FieldChanged),
		nullIfEmpty(e.Before),
		nullIfEmpty(e.After),
		nullIfEmpty(e.Reason),
	)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "audit.write", "insert audit", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
