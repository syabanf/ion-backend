package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ImportStatus tracks the per-import lifecycle. The usecase is the
// authoritative driver; the DB only enforces the enum values via CHECK.
//
// Lifecycle:
//
//	pending → processing → completed
//	                     → partial      (some rows persisted, some failed)
//	                     → failed       (header mismatch / nothing persisted)
type ImportStatus string

const (
	ImportStatusPending    ImportStatus = "pending"
	ImportStatusProcessing ImportStatus = "processing"
	ImportStatusCompleted  ImportStatus = "completed"
	ImportStatusFailed     ImportStatus = "failed"
	ImportStatusPartial    ImportStatus = "partial"
)

// ImportRowError is one row-level failure from CSV processing. The
// usecase aggregates these into the import row's error_summary jsonb.
type ImportRowError struct {
	Row    int    `json:"row"`
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason"`
}

// SubscriberImport is the audit-trail row for one CSV upload. The
// usecase mutates this in-flight as it processes rows; the repo
// persists the row at start (status=processing) and again at end
// (status=completed|partial|failed) so a crashed import leaves a
// "processing" tombstone the operator can act on rather than
// silently disappearing.
type SubscriberImport struct {
	ID                uuid.UUID
	ResellerAccountID uuid.UUID
	Source            string
	TotalRows         int
	OKRows            int
	ErrorRows         int
	RawUploadedURL    string
	Status            ImportStatus
	ErrorSummary      []ImportRowError
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
	CompletedAt       *time.Time
}

// NewSubscriberImport constructs a pending import. The usecase flips
// to processing → completed|partial|failed as it works through the
// rows.
func NewSubscriberImport(resellerID uuid.UUID, source string, createdBy *uuid.UUID) (*SubscriberImport, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("subscriber_import.reseller_required", "reseller_account_id is required")
	}
	now := time.Now().UTC()
	return &SubscriberImport{
		ID:                uuid.New(),
		ResellerAccountID: resellerID,
		Source:            source,
		Status:            ImportStatusPending,
		CreatedBy:         createdBy,
		CreatedAt:         now,
	}, nil
}

// Finalize sets the terminal status + counts + completed_at. Called by
// the usecase after row processing. We pick the status based on the
// counts so the caller can't accidentally write a contradictory state
// (e.g. ok=0, error=0, status=completed).
func (im *SubscriberImport) Finalize(total, ok, errs int, errors []ImportRowError, at time.Time) {
	im.TotalRows = total
	im.OKRows = ok
	im.ErrorRows = errs
	im.ErrorSummary = errors
	atUTC := at.UTC()
	im.CompletedAt = &atUTC
	switch {
	case ok == 0 && errs > 0:
		im.Status = ImportStatusFailed
	case errs == 0:
		im.Status = ImportStatusCompleted
	default:
		im.Status = ImportStatusPartial
	}
}

// MarkProcessing flips pending → processing. The usecase calls this
// right before it starts row work so an external observer can tell
// the difference between "accepted, not started" and "in progress".
func (im *SubscriberImport) MarkProcessing() {
	im.Status = ImportStatusProcessing
}
