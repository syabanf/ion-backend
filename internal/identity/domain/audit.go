package domain

import (
	"time"

	"github.com/google/uuid"
)

// AuditEntry is the read-side projection of identity.audit_logs.
//
// The write side lives in pkg/audit (writer interface); here we model the
// shape callers consume when rendering audit history. Before/After are
// kept as raw strings — the UI is responsible for rendering them as a
// diff (the prior build's QA fed back that this needs to be readable).
type AuditEntry struct {
	ID           uuid.UUID
	Timestamp    time.Time
	UserID       *uuid.UUID
	UserFullName string // joined from users at read time
	Module       string
	RecordType   string
	RecordID     string
	FieldChanged string
	Before       string
	After        string
	Description  string
	Reason       string
}

// AuditFilter narrows audit-log queries. All fields optional.
type AuditFilter struct {
	UserID     *uuid.UUID
	Module     string
	RecordType string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}
