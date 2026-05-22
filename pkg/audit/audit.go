// Package audit writes immutable audit log entries.
//
// Per the PRD (Administration §11), every configuration change and every
// schema action must be auditable. The Writer is a single dependency that
// bounded contexts depend on through their port; the actual storage adapter
// inserts into the `identity.audit_logs` table.
package audit

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Entry is a single audit row. record_id is intentionally a string —
// polymorphic across record types per the PRD's audit_logs design.
type Entry struct {
	Timestamp    time.Time
	UserID       uuid.UUID
	Module       string // e.g. "identity", "admin"
	RecordType   string // e.g. "user", "schema"
	RecordID     string // stringified UUID or composite key
	FieldChanged string // optional — for field-level diffs
	Before       string // JSON-encoded before value (optional)
	After        string // JSON-encoded after value (optional)
	Reason       string // optional change reason
}

// Writer persists audit entries. Implementations live in pkg/audit/postgres
// or test fakes; the interface is the contract.
type Writer interface {
	Write(ctx context.Context, e Entry) error
}

// Nop is a writer that drops every entry. Useful in tests and as the
// default when a service hasn't wired a real writer yet — callers can
// always `audit.SafeWrite(ctx, s.audit, ...)` without nil-checks.
type Nop struct{}

func (Nop) Write(context.Context, Entry) error { return nil }

// SafeWrite is a fire-and-forget helper. Use it from use-case methods
// where you don't want a failing audit insert to break the upstream
// flow. Errors are silently dropped — pass a logger via the entry's
// Reason field if you want them visible.
//
// The context is detached from its cancellation tree so audit writes
// land even if the originating request shut down mid-mutation.
func SafeWrite(ctx context.Context, w Writer, e Entry) {
	if w == nil {
		return
	}
	_ = w.Write(detachCancel(ctx), e)
}

// detachCancel returns a context that inherits values from `parent`
// but is decoupled from its cancellation signal. Equivalent to
// `context.WithoutCancel` (Go 1.21+) — written manually to keep the
// package compilable on older targets.
func detachCancel(parent context.Context) context.Context {
	return valueOnlyCtx{parent}
}

type valueOnlyCtx struct{ ctx context.Context }

func (valueOnlyCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (valueOnlyCtx) Done() <-chan struct{}       { return nil }
func (valueOnlyCtx) Err() error                  { return nil }
func (c valueOnlyCtx) Value(k any) any           { return c.ctx.Value(k) }
