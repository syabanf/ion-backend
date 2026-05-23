package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 103 — Per-EWO checklist progress (technician mobile)
//
// EWOChecklistProgress models the technician's per-EWO instance state
// for a single checklist item. Distinct from the legacy
// domain.EWOChecklistItem (pre_launch.go) which captures the static
// template + admin-side overrides; progress rows carry mobile-specific
// concerns (idempotency key, photo URL + hash, blocked/skipped reasons).
//
// State machine:
//
//   pending --MarkDone-->     done
//   pending --MarkSkipped-->  skipped
//   pending --MarkBlocked-->  blocked
//   skipped --MarkDone-->     done    (technician revisits a skipped item)
//   blocked --MarkDone-->     done    (after the blocker clears)
//
// `done` is terminal at this layer — flipping back is an admin action
// outside the mobile surface.
// =====================================================================

// ChecklistItemStatus is the lifecycle of a single progress row. Mirrors
// the migration check constraint exactly.
type ChecklistItemStatus string

const (
	ChecklistItemPending ChecklistItemStatus = "pending"
	ChecklistItemDone    ChecklistItemStatus = "done"
	ChecklistItemSkipped ChecklistItemStatus = "skipped"
	ChecklistItemBlocked ChecklistItemStatus = "blocked"
)

// EWOChecklistProgress is the persisted record. The repo layer is
// responsible for the (ewo_id, idempotency_key) uniqueness — the domain
// only enforces the in-row invariants.
type EWOChecklistProgress struct {
	ID              uuid.UUID
	EWOID           uuid.UUID
	ChecklistItemID *uuid.UUID
	ItemLabel       string
	Status          ChecklistItemStatus
	CompletedBy     *uuid.UUID
	CompletedAt     *time.Time
	PhotoURL        *string
	PhotoHash       *string
	Notes           string
	IdempotencyKey  *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewEWOChecklistProgress constructs a fresh `pending` row. The caller
// supplies the idempotency_key (or nil for non-mobile writes). Either
// checklist_item_id or item_label must be supplied so the row is
// addressable from the mobile UI — domain enforces "at least one of"
// rather than requiring both.
func NewEWOChecklistProgress(
	ewoID uuid.UUID,
	checklistItemID *uuid.UUID,
	itemLabel string,
	idempotencyKey *string,
) (*EWOChecklistProgress, error) {
	if ewoID == uuid.Nil {
		return nil, derrors.Validation(
			"checklist_progress.ewo_required",
			"ewo_id is required",
		)
	}
	itemLabel = strings.TrimSpace(itemLabel)
	if checklistItemID == nil && itemLabel == "" {
		return nil, derrors.Validation(
			"checklist_progress.item_required",
			"checklist_item_id or item_label must be provided",
		)
	}
	now := time.Now().UTC()
	return &EWOChecklistProgress{
		ID:              uuid.New(),
		EWOID:           ewoID,
		ChecklistItemID: checklistItemID,
		ItemLabel:       itemLabel,
		Status:          ChecklistItemPending,
		IdempotencyKey:  idempotencyKey,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// MarkDone transitions the row to `done`. Photo is optional but if a
// URL is provided, a matching hash is required — we use the hash to
// verify the upload was not tampered with at audit time.
func (p *EWOChecklistProgress) MarkDone(
	by uuid.UUID,
	at time.Time,
	photoURL *string,
	photoHash *string,
) error {
	if p.Status == ChecklistItemDone {
		// Idempotent re-mark — return nil. The repo layer separately
		// handles replay via idempotency_key, but here we accept a
		// "you already marked this done" call without erroring.
		return nil
	}
	switch p.Status {
	case ChecklistItemPending, ChecklistItemSkipped, ChecklistItemBlocked:
		// allowed
	default:
		return derrors.Validation(
			"checklist_progress.invalid_status_for_done",
			"status must be pending, skipped, or blocked to mark done",
		)
	}
	if by == uuid.Nil {
		return derrors.Validation(
			"checklist_progress.completed_by_required",
			"completed_by is required",
		)
	}
	if photoURL != nil && *photoURL != "" {
		if photoHash == nil || strings.TrimSpace(*photoHash) == "" {
			return derrors.Validation(
				"checklist_progress.photo_hash_required",
				"photo_hash is required when photo_url is provided",
			)
		}
	} else {
		// If URL is empty/nil, drop the hash too so we don't carry a
		// dangling proof.
		photoURL = nil
		photoHash = nil
	}
	p.Status = ChecklistItemDone
	by2 := by
	p.CompletedBy = &by2
	tt := at.UTC()
	p.CompletedAt = &tt
	p.PhotoURL = photoURL
	p.PhotoHash = photoHash
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkSkipped transitions a pending row to `skipped`. Reason is captured
// in the row's Notes column so a TL reviewer can see why.
func (p *EWOChecklistProgress) MarkSkipped(by uuid.UUID, at time.Time, reason string) error {
	if p.Status != ChecklistItemPending {
		return derrors.Validation(
			"checklist_progress.invalid_status_for_skip",
			"only pending items can be skipped",
		)
	}
	if by == uuid.Nil {
		return derrors.Validation(
			"checklist_progress.completed_by_required",
			"completed_by is required",
		)
	}
	p.Status = ChecklistItemSkipped
	by2 := by
	p.CompletedBy = &by2
	tt := at.UTC()
	p.CompletedAt = &tt
	p.Notes = strings.TrimSpace(reason)
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkBlocked transitions a pending row to `blocked`. The reason is
// stored in Notes — separate from a "skipped" intent because blocking
// implies the work isn't done by choice (waiting on a dependency).
func (p *EWOChecklistProgress) MarkBlocked(by uuid.UUID, at time.Time, reason string) error {
	if p.Status != ChecklistItemPending {
		return derrors.Validation(
			"checklist_progress.invalid_status_for_block",
			"only pending items can be blocked",
		)
	}
	if by == uuid.Nil {
		return derrors.Validation(
			"checklist_progress.completed_by_required",
			"completed_by is required",
		)
	}
	if strings.TrimSpace(reason) == "" {
		return derrors.Validation(
			"checklist_progress.block_reason_required",
			"reason is required to block a checklist item",
		)
	}
	p.Status = ChecklistItemBlocked
	by2 := by
	p.CompletedBy = &by2
	tt := at.UTC()
	p.CompletedAt = &tt
	p.Notes = strings.TrimSpace(reason)
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// Validate enforces the per-row invariants. Called from the repo before
// every write so a corrupt in-memory mutation can't sneak past.
func (p *EWOChecklistProgress) Validate() error {
	switch p.Status {
	case ChecklistItemPending, ChecklistItemDone, ChecklistItemSkipped, ChecklistItemBlocked:
		// allowed
	default:
		return derrors.Validation(
			"checklist_progress.invalid_status",
			"unknown status value",
		)
	}
	if p.Status == ChecklistItemDone {
		if p.CompletedBy == nil || *p.CompletedBy == uuid.Nil {
			return derrors.Validation(
				"checklist_progress.completed_by_required",
				"completed_by must be set on a done row",
			)
		}
		if p.CompletedAt == nil {
			return derrors.Validation(
				"checklist_progress.completed_at_required",
				"completed_at must be set on a done row",
			)
		}
	}
	if p.PhotoURL != nil && *p.PhotoURL != "" {
		if p.PhotoHash == nil || strings.TrimSpace(*p.PhotoHash) == "" {
			return derrors.Validation(
				"checklist_progress.photo_hash_required",
				"photo_hash is required when photo_url is set",
			)
		}
	}
	return nil
}
