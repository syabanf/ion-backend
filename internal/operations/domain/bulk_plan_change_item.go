package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkPlanChangeItemStatus — per-item state machine for plan change.
//
//	queued      — imported, not yet inspected
//	validating  — pre-flight check in progress
//	validated   — pre-flight passed; ready to apply
//	processing  — executor has picked it up
//	succeeded   — plan_change_requests row applied
//	failed      — terminal failure (error_msg populated)
//	skipped     — no-op (e.g. customer already on target plan)
type BulkPlanChangeItemStatus string

const (
	BPCItemQueued     BulkPlanChangeItemStatus = "queued"
	BPCItemValidating BulkPlanChangeItemStatus = "validating"
	BPCItemValidated  BulkPlanChangeItemStatus = "validated"
	BPCItemProcessing BulkPlanChangeItemStatus = "processing"
	BPCItemSucceeded  BulkPlanChangeItemStatus = "succeeded"
	BPCItemFailed     BulkPlanChangeItemStatus = "failed"
	BPCItemSkipped    BulkPlanChangeItemStatus = "skipped"
)

// BulkPlanChangeItem represents one customer's plan-change row inside a
// bulk job. The current_plan_id snapshot lets the executor compute the
// upgrade/downgrade direction at run time without re-querying CRM.
type BulkPlanChangeItem struct {
	ID            uuid.UUID
	BulkJobID     uuid.UUID
	CustomerID    uuid.UUID
	CurrentPlanID *uuid.UUID
	TargetPlanID  uuid.UUID
	EffectiveAt   *time.Time
	Status        BulkPlanChangeItemStatus
	ErrorMsg      string
	ProcessedAt   *time.Time
	CreatedAt     time.Time
}

// IsTerminal — true if the item won't be touched again.
func (it *BulkPlanChangeItem) IsTerminal() bool {
	switch it.Status {
	case BPCItemSucceeded, BPCItemFailed, BPCItemSkipped:
		return true
	}
	return false
}

// MarkValidating moves queued → validating.
func (it *BulkPlanChangeItem) MarkValidating() error {
	if it.Status != BPCItemQueued {
		return derrors.Conflict("bpc_item.bad_state",
			"only queued items can be marked validating")
	}
	it.Status = BPCItemValidating
	return nil
}

// MarkValidated moves validating → validated.
func (it *BulkPlanChangeItem) MarkValidated() error {
	if it.Status != BPCItemValidating {
		return derrors.Conflict("bpc_item.bad_state",
			"only validating items can be marked validated")
	}
	it.Status = BPCItemValidated
	return nil
}

// MarkProcessing moves validated → processing.
func (it *BulkPlanChangeItem) MarkProcessing() error {
	if it.Status != BPCItemValidated && it.Status != BPCItemQueued {
		return derrors.Conflict("bpc_item.bad_state",
			"only validated/queued items can be marked processing")
	}
	it.Status = BPCItemProcessing
	return nil
}

// MarkSucceeded — terminal success.
func (it *BulkPlanChangeItem) MarkSucceeded(at time.Time) error {
	if it.IsTerminal() {
		return derrors.Conflict("bpc_item.already_terminal", "item already finished")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	it.Status = BPCItemSucceeded
	it.ProcessedAt = &t
	it.ErrorMsg = ""
	return nil
}

// MarkFailed — terminal failure with a reason.
func (it *BulkPlanChangeItem) MarkFailed(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bpc_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BPCItemFailed
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}

// MarkSkipped — terminal no-op outcome.
func (it *BulkPlanChangeItem) MarkSkipped(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bpc_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BPCItemSkipped
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}
