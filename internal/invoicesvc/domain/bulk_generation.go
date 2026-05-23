package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// BulkJobKind — supported async run flavours.
type BulkJobKind string

const (
	BulkJobMonthlyCycle BulkJobKind = "monthly_cycle"
	BulkJobAddOn        BulkJobKind = "add_on"
	BulkJobAdjustment   BulkJobKind = "adjustment"
	BulkJobCorrection   BulkJobKind = "correction"
)

// JobStatus — bulk run lifecycle.
//
//	pending     — queued; no items have started.
//	running     — at least one item is in-flight.
//	completed   — every queued item finished as 'generated'.
//	failed      — every queued item finished as 'failed'.
//	partial     — mixed outcomes (≥1 generated AND ≥1 failed/skipped).
//
// 'completed' / 'failed' / 'partial' are terminal.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusPartial   JobStatus = "partial"
)

// ItemStatus — per-customer item outcome.
type ItemStatus string

const (
	ItemStatusQueued    ItemStatus = "queued"
	ItemStatusGenerated ItemStatus = "generated"
	ItemStatusFailed    ItemStatus = "failed"
	ItemStatusSkipped   ItemStatus = "skipped"
)

// BulkGenerationJob — the queue entry.
type BulkGenerationJob struct {
	ID             uuid.UUID
	Kind           BulkJobKind
	TargetFilter   map[string]any
	Status         JobStatus
	TotalExpected  int
	TotalGenerated int
	TotalFailed    int
	StartedAt      *time.Time
	CompletedAt    *time.Time
	ErrorSummary   map[string]any
	CreatedBy      *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BulkGenerationItem — one row per customer in the queue.
type BulkGenerationItem struct {
	ID          uuid.UUID
	JobID       uuid.UUID
	CustomerID  *uuid.UUID
	InvoiceID   *uuid.UUID
	Status      ItemStatus
	ErrorMsg    string
	GeneratedAt *time.Time
}

// NewBulkGenerationJob validates + constructs a pending job. TargetFilter
// is opaque to the domain — the runner adapter knows how to translate it
// into the actual customer set.
func NewBulkGenerationJob(kind BulkJobKind, targetFilter map[string]any, createdBy *uuid.UUID) (*BulkGenerationJob, error) {
	switch kind {
	case BulkJobMonthlyCycle, BulkJobAddOn, BulkJobAdjustment, BulkJobCorrection:
	default:
		return nil, errors.Validation("bulk_job.kind_invalid",
			"kind must be monthly_cycle | add_on | adjustment | correction")
	}
	now := time.Now().UTC()
	return &BulkGenerationJob{
		ID:           uuid.New(),
		Kind:         kind,
		TargetFilter: targetFilter,
		Status:       JobStatusPending,
		CreatedBy:    createdBy,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Start moves pending → running. Idempotent: re-calling on a running job
// is a no-op (returns nil) so a retry of the runner doesn't conflict.
func (j *BulkGenerationJob) Start(at time.Time) error {
	if j.Status == JobStatusRunning {
		return nil
	}
	if j.Status != JobStatusPending {
		return errors.Conflict("bulk_job.bad_state",
			"only pending jobs can start")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	j.Status = JobStatusRunning
	j.StartedAt = &t
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// Finish rolls up the per-item outcomes and picks the terminal status.
// All-generated → completed; all-failed → failed; mixed → partial.
// Skipped items count toward 'partial' (they aren't successes).
func (j *BulkGenerationJob) Finish(items []BulkGenerationItem, at time.Time) error {
	if j.IsTerminal() {
		return errors.Conflict("bulk_job.already_terminal",
			"job already finished")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	bucket := ItemStatusBucket(items)
	j.TotalGenerated = bucket.Generated
	j.TotalFailed = bucket.Failed + bucket.Skipped
	switch {
	case bucket.Generated > 0 && bucket.Failed == 0 && bucket.Skipped == 0:
		j.Status = JobStatusCompleted
	case bucket.Generated == 0 && (bucket.Failed > 0 || bucket.Skipped > 0):
		j.Status = JobStatusFailed
	default:
		j.Status = JobStatusPartial
	}
	j.CompletedAt = &t
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// IsTerminal reports whether the job has finished.
func (j *BulkGenerationJob) IsTerminal() bool {
	return j.Status == JobStatusCompleted ||
		j.Status == JobStatusFailed ||
		j.Status == JobStatusPartial
}

// StatusBucket aggregates item counts by status.
type StatusBucket struct {
	Queued    int
	Generated int
	Failed    int
	Skipped   int
}

// ItemStatusBucket counts items per status. Exposed so callers can make
// the partial/failed/completed decision without re-implementing the
// counting + selection logic.
func ItemStatusBucket(items []BulkGenerationItem) StatusBucket {
	var b StatusBucket
	for _, it := range items {
		switch it.Status {
		case ItemStatusQueued:
			b.Queued++
		case ItemStatusGenerated:
			b.Generated++
		case ItemStatusFailed:
			b.Failed++
		case ItemStatusSkipped:
			b.Skipped++
		}
	}
	return b
}

// MarkItemGenerated transitions a queued item → generated. Re-calling on
// an already-finished item is a conflict so the runner knows it lost a
// race.
func (it *BulkGenerationItem) MarkGenerated(invoiceID uuid.UUID, at time.Time) error {
	if it.Status != ItemStatusQueued {
		return errors.Conflict("bulk_item.bad_state",
			"only queued items can be marked generated")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	it.Status = ItemStatusGenerated
	it.InvoiceID = &invoiceID
	it.GeneratedAt = &t
	it.ErrorMsg = ""
	return nil
}

// MarkItemFailed records the error and pins the item to 'failed'.
func (it *BulkGenerationItem) MarkFailed(msg string) error {
	if it.Status != ItemStatusQueued {
		return errors.Conflict("bulk_item.bad_state",
			"only queued items can be marked failed")
	}
	it.Status = ItemStatusFailed
	it.ErrorMsg = strings.TrimSpace(msg)
	return nil
}

// MarkItemSkipped is for the "this customer doesn't need an invoice"
// case (e.g. they're already cancelled / on hold).
func (it *BulkGenerationItem) MarkSkipped(reason string) error {
	if it.Status != ItemStatusQueued {
		return errors.Conflict("bulk_item.bad_state",
			"only queued items can be marked skipped")
	}
	it.Status = ItemStatusSkipped
	it.ErrorMsg = strings.TrimSpace(reason)
	return nil
}
