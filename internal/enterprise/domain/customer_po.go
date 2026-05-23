package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CustomerPO — Wave 95 (Customer PO + IC-PO foundation)
// =====================================================================
//
// CustomerPO is the buyer-facing PO that closes a Won opportunity. A
// freshly-uploaded PO starts in `received`; Finance validates the BOQ
// version match + tax stance and flips it to `validated`; an authorized
// user (sales manager / commercial owner) then `accepts` it, which is
// the trigger event that auto-spawns one IntercompanyPO draft per
// distinct executing subsidiary the BOQ lines reference.
//
// State machine (TC-CPO-* / TC-SM-CPO-*):
//
//	received → validated → accepted (terminal positive)
//	received | validated → rejected (terminal negative)
//	received | validated → cancelled (terminal admin escape)
type CustomerPOStatus string

const (
	CustomerPOStatusReceived  CustomerPOStatus = "received"
	CustomerPOStatusValidated CustomerPOStatus = "validated"
	CustomerPOStatusAccepted  CustomerPOStatus = "accepted"
	CustomerPOStatusRejected  CustomerPOStatus = "rejected"
	CustomerPOStatusCancelled CustomerPOStatus = "cancelled"
)

type CustomerPO struct {
	ID                           uuid.UUID
	OpportunityID                uuid.UUID
	BOQVersionID                 uuid.UUID
	CustomerID                   *uuid.UUID
	CommercialOwnerSubsidiaryID  uuid.UUID
	PONumber                     string
	POValue                      *float64
	FileURL                      string
	FileHash                     string
	UploadedBy                   *uuid.UUID
	UploadedAt                   *time.Time
	Status                       CustomerPOStatus
	ValidatedAt                  *time.Time
	AcceptedAt                   *time.Time
	RejectedAt                   *time.Time
	CancelledAt                  *time.Time
	RejectionReason              string
	Notes                        string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// NewCustomerPO constructs a `received` PO row. The downstream
// acceptance / IC-PO automation depends on opportunity_id +
// boq_version_id + commercial_owner_subsidiary_id all being set; the
// constructor enforces that.
func NewCustomerPO(
	opportunityID, boqVersionID, commercialOwnerSubsidiaryID uuid.UUID,
	poNumber string,
) (*CustomerPO, error) {
	if opportunityID == uuid.Nil {
		return nil, derrors.Validation("customer_po.opportunity_required", "opportunity_id is required")
	}
	if boqVersionID == uuid.Nil {
		return nil, derrors.Validation("customer_po.boq_required", "boq_version_id is required")
	}
	if commercialOwnerSubsidiaryID == uuid.Nil {
		return nil, derrors.Validation(
			"customer_po.commercial_owner_required",
			"commercial_owner_subsidiary_id is required",
		)
	}
	poNumber = strings.TrimSpace(poNumber)
	if poNumber == "" {
		return nil, derrors.Validation("customer_po.po_number_required", "po_number is required")
	}
	now := time.Now().UTC()
	return &CustomerPO{
		ID:                          uuid.New(),
		OpportunityID:               opportunityID,
		BOQVersionID:                boqVersionID,
		CommercialOwnerSubsidiaryID: commercialOwnerSubsidiaryID,
		PONumber:                    poNumber,
		Status:                      CustomerPOStatusReceived,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}, nil
}

// Validate flips received → validated. Finance gate.
func (c *CustomerPO) Validate() error {
	if c.Status != CustomerPOStatusReceived {
		return derrors.Conflict(
			"customer_po.invalid_state_transition",
			"can only validate from received (current: "+string(c.Status)+")",
		)
	}
	now := time.Now().UTC()
	c.Status = CustomerPOStatusValidated
	c.ValidatedAt = &now
	c.UpdatedAt = now
	return nil
}

// Accept flips validated → accepted. This is the trigger event that
// the usecase layer uses to auto-spawn IntercompanyPO drafts.
func (c *CustomerPO) Accept() error {
	if c.Status != CustomerPOStatusValidated {
		return derrors.Conflict(
			"customer_po.invalid_state_transition",
			"can only accept from validated (current: "+string(c.Status)+")",
		)
	}
	now := time.Now().UTC()
	c.Status = CustomerPOStatusAccepted
	c.AcceptedAt = &now
	c.UpdatedAt = now
	return nil
}

// Reject is terminal — allowed from received or validated. Reason is
// required for audit (TC-CPO-006).
func (c *CustomerPO) Reject(reason string) error {
	if c.Status != CustomerPOStatusReceived && c.Status != CustomerPOStatusValidated {
		return derrors.Conflict(
			"customer_po.invalid_state_transition",
			"can only reject from received or validated (current: "+string(c.Status)+")",
		)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return derrors.Validation("customer_po.reject_reason_required", "rejection reason is required")
	}
	now := time.Now().UTC()
	c.Status = CustomerPOStatusRejected
	c.RejectedAt = &now
	c.RejectionReason = reason
	c.UpdatedAt = now
	return nil
}

// Cancel is the admin escape — allowed pre-accept only.
func (c *CustomerPO) Cancel() error {
	if c.Status != CustomerPOStatusReceived && c.Status != CustomerPOStatusValidated {
		return derrors.Conflict(
			"customer_po.invalid_state_transition",
			"can only cancel from received or validated (current: "+string(c.Status)+")",
		)
	}
	now := time.Now().UTC()
	c.Status = CustomerPOStatusCancelled
	c.CancelledAt = &now
	c.UpdatedAt = now
	return nil
}
