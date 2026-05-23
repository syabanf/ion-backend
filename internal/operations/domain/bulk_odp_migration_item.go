package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkODPMigrationItemStatus — per-item state machine for ODP migration.
//
//	queued       — imported
//	validating   — capacity + window checks running
//	validated    — pre-flight passed
//	staged       — migration WO created, awaiting field execution
//	migrated     — port reassigned, customer moved
//	failed       — terminal failure
//	rolled_back  — recovery action; original port restored
type BulkODPMigrationItemStatus string

const (
	BOMItemQueued      BulkODPMigrationItemStatus = "queued"
	BOMItemValidating  BulkODPMigrationItemStatus = "validating"
	BOMItemValidated   BulkODPMigrationItemStatus = "validated"
	BOMItemStaged      BulkODPMigrationItemStatus = "staged"
	BOMItemMigrated    BulkODPMigrationItemStatus = "migrated"
	BOMItemFailed      BulkODPMigrationItemStatus = "failed"
	BOMItemRolledBack  BulkODPMigrationItemStatus = "rolled_back"
)

// BulkODPMigrationItem represents one customer's ODP-to-ODP migration row.
type BulkODPMigrationItem struct {
	ID                   uuid.UUID
	BulkJobID            uuid.UUID
	CustomerID           uuid.UUID
	FromOLTPortID        *uuid.UUID
	ToOLTPortID          uuid.UUID
	ScheduledWindowStart *time.Time
	ScheduledWindowEnd   *time.Time
	Status               BulkODPMigrationItemStatus
	WOID                 *uuid.UUID
	ErrorMsg             string
	ProcessedAt          *time.Time
	CreatedAt            time.Time
}

// IsTerminal — true if the item won't be touched again.
func (it *BulkODPMigrationItem) IsTerminal() bool {
	switch it.Status {
	case BOMItemMigrated, BOMItemFailed, BOMItemRolledBack:
		return true
	}
	return false
}

// MarkValidating moves queued → validating.
func (it *BulkODPMigrationItem) MarkValidating() error {
	if it.Status != BOMItemQueued {
		return derrors.Conflict("bom_item.bad_state",
			"only queued items can be marked validating")
	}
	it.Status = BOMItemValidating
	return nil
}

// MarkValidated moves validating → validated.
func (it *BulkODPMigrationItem) MarkValidated() error {
	if it.Status != BOMItemValidating {
		return derrors.Conflict("bom_item.bad_state",
			"only validating items can be marked validated")
	}
	it.Status = BOMItemValidated
	return nil
}

// MarkStaged moves validated → staged. The companion WO id is captured so
// the field crew can find it.
func (it *BulkODPMigrationItem) MarkStaged(woID uuid.UUID) error {
	if it.Status != BOMItemValidated && it.Status != BOMItemQueued {
		return derrors.Conflict("bom_item.bad_state",
			"only validated/queued items can be marked staged")
	}
	it.Status = BOMItemStaged
	it.WOID = &woID
	return nil
}

// MarkMigrated — terminal success.
func (it *BulkODPMigrationItem) MarkMigrated(at time.Time) error {
	if it.IsTerminal() {
		return derrors.Conflict("bom_item.already_terminal", "item already finished")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	it.Status = BOMItemMigrated
	it.ProcessedAt = &t
	it.ErrorMsg = ""
	return nil
}

// MarkFailed — terminal failure.
func (it *BulkODPMigrationItem) MarkFailed(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bom_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BOMItemFailed
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}

// MarkRolledBack — terminal rollback.
func (it *BulkODPMigrationItem) MarkRolledBack(reason string) error {
	if it.IsTerminal() {
		return derrors.Conflict("bom_item.already_terminal", "item already finished")
	}
	now := time.Now().UTC()
	it.Status = BOMItemRolledBack
	it.ErrorMsg = strings.TrimSpace(reason)
	it.ProcessedAt = &now
	return nil
}
