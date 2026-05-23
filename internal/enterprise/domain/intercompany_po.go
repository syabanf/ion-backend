package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// IntercompanyPO — Wave 95
// =====================================================================
//
// IntercompanyPO is the inter-subsidiary PO drafted automatically when
// a CustomerPO is accepted. One IC-PO is created per distinct executing
// subsidiary the BOQ lines reference (commercial_owner_subsidiary_id !=
// executing_subsidiary_id; otherwise the subsidiary is fulfilling its
// own order and no IC-PO is needed).
//
// State machine (TC-IC-* / TC-SM-ICPO-*):
//
//	draft → issued → accepted (terminal positive)
//	                 ↘ rejected (terminal negative; reason required)
//	draft | issued → cancelled (terminal admin escape)
//	issued | accepted → superseded (chained to a newer IC-PO via supersedes_id)
type IntercompanyPOStatus string

const (
	IntercompanyPOStatusDraft      IntercompanyPOStatus = "draft"
	IntercompanyPOStatusIssued     IntercompanyPOStatus = "issued"
	IntercompanyPOStatusAccepted   IntercompanyPOStatus = "accepted"
	IntercompanyPOStatusRejected   IntercompanyPOStatus = "rejected"
	IntercompanyPOStatusSuperseded IntercompanyPOStatus = "superseded"
	IntercompanyPOStatusCancelled  IntercompanyPOStatus = "cancelled"
)

type IntercompanyPO struct {
	ID                          uuid.UUID
	CustomerPOID                uuid.UUID
	BOQVersionID                uuid.UUID
	CommercialOwnerSubsidiaryID uuid.UUID
	ExecutingSubsidiaryID       uuid.UUID
	ICPONumber                  string
	Status                      IntercompanyPOStatus
	Total                       *float64
	TaxSnapshotHash             string
	IssuedAt                    *time.Time
	AcceptedAt                  *time.Time
	AcceptedBy                  *uuid.UUID
	RejectedAt                  *time.Time
	RejectionReason             string
	CancelledAt                 *time.Time
	SupersededAt                *time.Time
	SupersedesID                *uuid.UUID
	Notes                       string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

// IntercompanyPOLine is a snapshot of the BOQ line under the IC-PO.
// We copy SKU + description + qty + price so the IC-PO ledger stays
// stable even if the BOQ is later edited or superseded.
type IntercompanyPOLine struct {
	ID             uuid.UUID
	ICPOID         uuid.UUID
	BOQLineID      *uuid.UUID
	SKUOrServiceID *uuid.UUID
	Description    string
	Qty            float64
	UnitPrice      float64
	LineTotal      float64
	TaxAmount      float64
	CreatedAt      time.Time
}

// GenerateICPONumber returns a sortable number based on date + suffix.
// Format: ICPO-YYYYMMDD-<8 hex>.
func GenerateICPONumber(t time.Time) string {
	return "ICPO-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// NewIntercompanyPO constructs a `draft` IC-PO. Caller supplies the
// commercial-owner + executing IDs (both required; can't be equal —
// you can't IC-PO yourself).
func NewIntercompanyPO(
	customerPOID, boqVersionID,
	commercialOwnerSubsidiaryID, executingSubsidiaryID uuid.UUID,
	icPONumber string,
) (*IntercompanyPO, error) {
	if customerPOID == uuid.Nil {
		return nil, derrors.Validation("intercompany_po.customer_po_required", "customer_po_id is required")
	}
	if boqVersionID == uuid.Nil {
		return nil, derrors.Validation("intercompany_po.boq_required", "boq_version_id is required")
	}
	if commercialOwnerSubsidiaryID == uuid.Nil {
		return nil, derrors.Validation(
			"intercompany_po.commercial_owner_required",
			"commercial_owner_subsidiary_id is required",
		)
	}
	if executingSubsidiaryID == uuid.Nil {
		return nil, derrors.Validation(
			"intercompany_po.executing_required",
			"executing_subsidiary_id is required",
		)
	}
	if commercialOwnerSubsidiaryID == executingSubsidiaryID {
		return nil, derrors.Validation(
			"intercompany_po.self_ic_forbidden",
			"commercial_owner_subsidiary_id and executing_subsidiary_id must be different — you cannot IC-PO yourself",
		)
	}
	icPONumber = strings.TrimSpace(icPONumber)
	if icPONumber == "" {
		return nil, derrors.Validation("intercompany_po.number_required", "ic_po_number is required")
	}
	now := time.Now().UTC()
	return &IntercompanyPO{
		ID:                          uuid.New(),
		CustomerPOID:                customerPOID,
		BOQVersionID:                boqVersionID,
		CommercialOwnerSubsidiaryID: commercialOwnerSubsidiaryID,
		ExecutingSubsidiaryID:       executingSubsidiaryID,
		ICPONumber:                  icPONumber,
		Status:                      IntercompanyPOStatusDraft,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}, nil
}

// Validate re-runs the same invariants as the constructor on the
// header — guards against post-construction mutation drift.
func (i *IntercompanyPO) Validate() error {
	if i.CommercialOwnerSubsidiaryID == i.ExecutingSubsidiaryID {
		return derrors.Validation(
			"intercompany_po.self_ic_forbidden",
			"commercial_owner_subsidiary_id and executing_subsidiary_id must be different",
		)
	}
	return nil
}

// Issue flips draft → issued, stamping the issued_at timestamp.
func (i *IntercompanyPO) Issue() error {
	if i.Status != IntercompanyPOStatusDraft {
		return derrors.Conflict(
			"intercompany_po.invalid_state_transition",
			"can only issue from draft (current: "+string(i.Status)+")",
		)
	}
	now := time.Now().UTC()
	i.Status = IntercompanyPOStatusIssued
	i.IssuedAt = &now
	i.UpdatedAt = now
	return nil
}

// Accept flips issued → accepted; the by-user UUID is captured so the
// downstream InternalTransaction recognition can attribute the event.
func (i *IntercompanyPO) Accept(byUserID *uuid.UUID) error {
	if i.Status != IntercompanyPOStatusIssued {
		return derrors.Conflict(
			"intercompany_po.invalid_state_transition",
			"can only accept from issued (current: "+string(i.Status)+")",
		)
	}
	now := time.Now().UTC()
	i.Status = IntercompanyPOStatusAccepted
	i.AcceptedAt = &now
	i.AcceptedBy = byUserID
	i.UpdatedAt = now
	return nil
}

// Reject is terminal — allowed from issued. Reason mandatory.
func (i *IntercompanyPO) Reject(reason string) error {
	if i.Status != IntercompanyPOStatusIssued {
		return derrors.Conflict(
			"intercompany_po.invalid_state_transition",
			"can only reject from issued (current: "+string(i.Status)+")",
		)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return derrors.Validation("intercompany_po.reject_reason_required", "rejection reason is required")
	}
	now := time.Now().UTC()
	i.Status = IntercompanyPOStatusRejected
	i.RejectedAt = &now
	i.RejectionReason = reason
	i.UpdatedAt = now
	return nil
}

// Cancel is the admin escape — allowed pre-accept (draft | issued).
func (i *IntercompanyPO) Cancel() error {
	if i.Status != IntercompanyPOStatusDraft && i.Status != IntercompanyPOStatusIssued {
		return derrors.Conflict(
			"intercompany_po.invalid_state_transition",
			"can only cancel from draft or issued (current: "+string(i.Status)+")",
		)
	}
	now := time.Now().UTC()
	i.Status = IntercompanyPOStatusCancelled
	i.CancelledAt = &now
	i.UpdatedAt = now
	return nil
}

// Supersede flips issued | accepted → superseded, chained via
// supersedes_id to the new IC-PO that replaces it (TC-IC supersede
// chain test).
func (i *IntercompanyPO) Supersede(newID uuid.UUID) error {
	if i.Status != IntercompanyPOStatusIssued && i.Status != IntercompanyPOStatusAccepted {
		return derrors.Conflict(
			"intercompany_po.invalid_state_transition",
			"can only supersede from issued or accepted (current: "+string(i.Status)+")",
		)
	}
	if newID == uuid.Nil {
		return derrors.Validation("intercompany_po.supersede_id_required", "new IC-PO id is required to supersede")
	}
	now := time.Now().UTC()
	i.Status = IntercompanyPOStatusSuperseded
	i.SupersededAt = &now
	// supersedes_id on the OLD row points at the NEW row that replaced
	// it — so a `SELECT supersedes_id` chain walks forward in time.
	i.SupersedesID = &newID
	i.UpdatedAt = now
	return nil
}
