// Package domain holds the Operations module bounded-context types.
//
// Wave 125 introduces the BulkJob aggregate — the executor-aware counterpart
// to the Wave 71 `operations.bulk_operations` table. A BulkJob owns a set
// of typed items (plan_change / odp_migration / wo_creation), tracks per-
// item counters, and drives an 8-state status machine that's safe to
// resume mid-run (the executor is idempotent on item status).
package domain

import (
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkJobKind — supported executor flavours. Each kind ships with its own
// item table (operations.bulk_<kind>_items) and a dedicated executor in
// the usecase layer.
type BulkJobKind string

const (
	BulkJobPlanChange             BulkJobKind = "plan_change"
	BulkJobODPMigration           BulkJobKind = "odp_migration"
	BulkJobWOCreation             BulkJobKind = "wo_creation"
	BulkJobWOCancellation         BulkJobKind = "wo_cancellation"
	BulkJobCustomerSegmentExport  BulkJobKind = "customer_segment_export"
)

// IsValidBulkJobKind — kind whitelist mirroring the DB CHECK.
func IsValidBulkJobKind(k BulkJobKind) bool {
	switch k {
	case BulkJobPlanChange, BulkJobODPMigration, BulkJobWOCreation,
		BulkJobWOCancellation, BulkJobCustomerSegmentExport:
		return true
	}
	return false
}

// BulkJobStatus — 8-state status machine for a bulk job.
//
//	pending     — CSV imported, no items processed yet
//	validating  — pre-flight validation in progress
//	running     — executor is iterating queued items
//	completed   — every queued item finished as 'succeeded'
//	failed      — every queued item finished as 'failed'
//	partial     — mixed outcomes (≥1 succeeded AND ≥1 failed/skipped)
//	cancelled   — operator-requested halt; remaining items skipped
//
// completed / failed / partial / cancelled are terminal.
type BulkJobStatus string

const (
	BulkJobStatusPending    BulkJobStatus = "pending"
	BulkJobStatusValidating BulkJobStatus = "validating"
	BulkJobStatusRunning    BulkJobStatus = "running"
	BulkJobStatusCompleted  BulkJobStatus = "completed"
	BulkJobStatusFailed     BulkJobStatus = "failed"
	BulkJobStatusPartial    BulkJobStatus = "partial"
	BulkJobStatusCancelled  BulkJobStatus = "cancelled"
)

// BulkJob is the aggregate root for an executor run.
type BulkJob struct {
	ID              uuid.UUID
	Kind            BulkJobKind
	Status          BulkJobStatus
	TotalItems      int
	ProcessedItems  int
	SucceededItems  int
	FailedItems     int
	SkippedItems    int
	StartedAt       *time.Time
	CompletedAt     *time.Time
	ErrorSummary    map[string]any
	DryRun          bool
	CreatedBy       *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewBulkJob constructs a fresh BulkJob in pending status. The caller is
// responsible for persisting; the domain only validates inputs.
func NewBulkJob(kind BulkJobKind, dryRun bool, createdBy *uuid.UUID) (*BulkJob, error) {
	if !IsValidBulkJobKind(kind) {
		return nil, derrors.Validation("bulk_job.kind_invalid",
			"kind must be plan_change|odp_migration|wo_creation|wo_cancellation|customer_segment_export")
	}
	now := time.Now().UTC()
	return &BulkJob{
		ID:        uuid.New(),
		Kind:      kind,
		Status:    BulkJobStatusPending,
		DryRun:    dryRun,
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// IsTerminal — true if status is one of the four terminal states.
func (j *BulkJob) IsTerminal() bool {
	switch j.Status {
	case BulkJobStatusCompleted, BulkJobStatusFailed,
		BulkJobStatusPartial, BulkJobStatusCancelled:
		return true
	}
	return false
}

// MarkValidating moves pending → validating. Idempotent on validating
// (a second caller from a re-run gets nil, not an error).
func (j *BulkJob) MarkValidating() error {
	if j.Status == BulkJobStatusValidating {
		return nil
	}
	if j.Status != BulkJobStatusPending {
		return derrors.Conflict("bulk_job.bad_state",
			"only pending jobs can transition to validating")
	}
	j.Status = BulkJobStatusValidating
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkRunning moves pending|validating → running and stamps started_at
// the first time. Idempotent on running.
func (j *BulkJob) MarkRunning() error {
	if j.Status == BulkJobStatusRunning {
		return nil
	}
	switch j.Status {
	case BulkJobStatusPending, BulkJobStatusValidating:
	default:
		return derrors.Conflict("bulk_job.bad_state",
			"only pending|validating jobs can transition to running")
	}
	j.Status = BulkJobStatusRunning
	if j.StartedAt == nil {
		now := time.Now().UTC()
		j.StartedAt = &now
	}
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkCompleted moves running → completed. Caller has already checked the
// counters (no failures, no skips).
func (j *BulkJob) MarkCompleted() error {
	return j.toTerminal(BulkJobStatusCompleted, nil)
}

// MarkFailed moves running → failed with an optional reason captured in
// ErrorSummary.
func (j *BulkJob) MarkFailed(reason string) error {
	var summary map[string]any
	if reason != "" {
		summary = map[string]any{"reason": reason}
	}
	return j.toTerminal(BulkJobStatusFailed, summary)
}

// MarkPartial moves running → partial. The mixed outcome is the most
// common terminal state in practice.
func (j *BulkJob) MarkPartial() error {
	return j.toTerminal(BulkJobStatusPartial, nil)
}

// MarkCancelled moves running|pending|validating → cancelled. Operator
// action: remaining queued items will be left unprocessed.
func (j *BulkJob) MarkCancelled() error {
	if j.IsTerminal() {
		return derrors.Conflict("bulk_job.already_terminal",
			"job is already in a terminal state")
	}
	now := time.Now().UTC()
	j.Status = BulkJobStatusCancelled
	j.CompletedAt = &now
	j.UpdatedAt = now
	return nil
}

func (j *BulkJob) toTerminal(s BulkJobStatus, summary map[string]any) error {
	if j.IsTerminal() {
		return derrors.Conflict("bulk_job.already_terminal",
			"job is already in a terminal state")
	}
	now := time.Now().UTC()
	j.Status = s
	j.CompletedAt = &now
	j.UpdatedAt = now
	if summary != nil {
		j.ErrorSummary = summary
	}
	return nil
}

// RecordItem bumps the per-item counters. Returns the new processed total
// so the caller can decide whether to finalize. `succeeded` and `skipped`
// are mutually exclusive; failure is the default if both are false.
func (j *BulkJob) RecordItem(succeeded bool, skipped bool) {
	j.ProcessedItems++
	switch {
	case succeeded:
		j.SucceededItems++
	case skipped:
		j.SkippedItems++
	default:
		j.FailedItems++
	}
	j.UpdatedAt = time.Now().UTC()
}

// Finalize picks the terminal status from current counters. Call after the
// last item has been processed.
//
//	all succeeded            → completed
//	all failed (no success)  → failed
//	mix                      → partial
//	zero items               → completed (treated as a no-op success)
func (j *BulkJob) Finalize() error {
	if j.IsTerminal() {
		return nil // idempotent re-run
	}
	switch {
	case j.TotalItems == 0:
		return j.MarkCompleted()
	case j.SucceededItems == j.TotalItems:
		return j.MarkCompleted()
	case j.SucceededItems == 0:
		return j.MarkFailed("")
	default:
		return j.MarkPartial()
	}
}
