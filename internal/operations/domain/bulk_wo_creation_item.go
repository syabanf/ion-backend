package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkWOCreationItemStatus — per-item state machine for bulk WO creation.
//
//	queued      — imported
//	validating  — customer + template + duplicate-WO checks running
//	validated   — pre-flight passed; ready to create
//	created     — WO inserted, ID captured
//	failed      — terminal failure
//	duplicate   — customer already has an open WO of the same type
type BulkWOCreationItemStatus string

const (
	BWOItemQueued     BulkWOCreationItemStatus = "queued"
	BWOItemValidating BulkWOCreationItemStatus = "validating"
	BWOItemValidated  BulkWOCreationItemStatus = "validated"
	BWOItemCreated    BulkWOCreationItemStatus = "created"
	BWOItemFailed     BulkWOCreationItemStatus = "failed"
	BWOItemDuplicate  BulkWOCreationItemStatus = "duplicate"
)

// BulkWOCreationItem represents one customer's WO row inside a bulk job.
type BulkWOCreationItem struct {
	ID            uuid.UUID
	BulkJobID     uuid.UUID
	CustomerID    uuid.UUID
	WOTemplateID  *uuid.UUID
	WOType        string
	ScheduledAt   *time.Time
	Status        BulkWOCreationItemStatus
	CreatedWOID   *uuid.UUID
	ErrorMsg      string
	ProcessedAt   *time.Time
	CreatedAt     time.Time
}

// IsTerminal — true if the item won't be touched again.
func (it *BulkWOCreationItem) IsTerminal() bool {
	switch it.Status {
	case BWOItemCreated, BWOItemFailed, BWOItemDuplicate:
		return true
	}
	return false
}

// MarkValidating moves queued → validating.
func (it *BulkWOCreationItem) MarkValidating() error {
	if it.Status != BWOItemQueued {
		return derrors.Conflict("bwo_item.bad_state",
			"only queued items can be marked validating")
	}
	it.Status = BWOItemValidating
	return nil
}

// MarkValidated moves validating → validated.
func (it *BulkWOCreationItem) MarkValidated() error {
	if it.Status != BWOItemValidating {
		return derrors.Conflict("bwo_item.bad_state",
			"only validating items can be marked validated")
	}
	it.Status = BWOItemValidated
	return nil
}

// MarkCreated — terminal success with the new WO id.
func (it *BulkWOCreationItem) MarkCreated(woID uuid.UUID, at time.Time) error {
	if it.IsTerminal() {
		return derrors.Conflict("bwo_item.already_terminal", "item already finished")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	it.Status = BWOItemCreated
	it.CreatedWOID = &woID
	it.ProcessedAt = &t
	it.ErrorMsg = ""
	return nil
}

// MarkFailed — terminal failure.
func (it *BulkWOCreationItem) MarkFailed(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bwo_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BWOItemFailed
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}

// MarkDuplicate — terminal duplicate-WO skip (one customer already has an
// open WO of the same type).
func (it *BulkWOCreationItem) MarkDuplicate(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bwo_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BWOItemDuplicate
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}
