package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// AddOnCategory partitions catalog items by what activation triggers.
//
//	digital  — RADIUS bandwidth profile / static IP push; no field WO.
//	physical — needs a WO (router install, cable extension).
//	service  — pure billing line, no system action.
type AddOnCategory string

const (
	AddOnCategoryDigital  AddOnCategory = "digital"
	AddOnCategoryPhysical AddOnCategory = "physical"
	AddOnCategoryService  AddOnCategory = "service"
)

// AddOnStatus lifecycle.
//
//	pending_install — purchased, physical install pending (TC-AOB-003).
//	active          — billable; included in next-cycle invoice.
//	expired         — past valid_until without renewal.
//	cancelled       — customer-requested removal (TC-AOB-005); does NOT
//	                  refund the current cycle, just stops next-cycle
//	                  billing.
type AddOnStatus string

const (
	AddOnStatusPendingInstall AddOnStatus = "pending_install"
	AddOnStatusActive         AddOnStatus = "active"
	AddOnStatusExpired        AddOnStatus = "expired"
	AddOnStatusCancelled      AddOnStatus = "cancelled"
)

// AddOnPurchase mirrors billing.add_on_purchases. A purchase rolls up
// into a customer's next recurring invoice as a separate line item
// (TC-AOB-006). The fields here mirror what the recurring scheduler in
// usecase/r3.go needs to reconcile against crm.customer_addons.
type AddOnPurchase struct {
	ID            uuid.UUID
	CustomerID    uuid.UUID
	AddOnSKU      string
	AddOnName     string
	Category      AddOnCategory
	Quantity      int
	UnitPrice     float64
	Total         float64
	InvoiceID     *uuid.UUID
	Status        AddOnStatus
	ValidFrom     *time.Time
	ValidUntil    *time.Time
	CancelledAt   *time.Time
	CancelReason  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewAddOnPurchase constructs a row in pending_install (physical) or
// active (digital/service) state. quantity defaults to 1. total =
// unit_price × qty.
func NewAddOnPurchase(
	customerID uuid.UUID,
	sku, name string,
	category AddOnCategory,
	quantity int,
	unitPrice float64,
) (*AddOnPurchase, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("addon.customer_required", "customer_id is required")
	}
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return nil, errors.Validation("addon.sku_required", "sku is required")
	}
	if quantity <= 0 {
		quantity = 1
	}
	if unitPrice < 0 {
		return nil, errors.Validation("addon.price_invalid", "unit_price must be >= 0")
	}
	switch category {
	case AddOnCategoryDigital, AddOnCategoryPhysical, AddOnCategoryService:
	default:
		category = AddOnCategoryService
	}
	now := time.Now().UTC()
	status := AddOnStatusActive
	if category == AddOnCategoryPhysical {
		status = AddOnStatusPendingInstall
	}
	return &AddOnPurchase{
		ID:         uuid.New(),
		CustomerID: customerID,
		AddOnSKU:   sku,
		AddOnName:  name,
		Category:   category,
		Quantity:   quantity,
		UnitPrice:  unitPrice,
		Total:      unitPrice * float64(quantity),
		Status:     status,
		ValidFrom:  &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Activate flips pending_install → active. Called by the field-svc
// completion hook once the physical add-on is installed (TC-AOB-003).
func (a *AddOnPurchase) Activate() error {
	if a.Status == AddOnStatusActive {
		return errors.Conflict("addon.already_active", "add-on already active")
	}
	if a.Status != AddOnStatusPendingInstall {
		return errors.Conflict("addon.bad_state", "only pending_install add-ons can activate")
	}
	now := time.Now().UTC()
	a.Status = AddOnStatusActive
	a.UpdatedAt = now
	return nil
}

// Cancel records the customer's request to remove the add-on. Per
// TC-AOB-005, mid-cycle cancel does NOT refund — the recurring
// scheduler simply omits the line on the next cycle. reason is
// required so the audit trail has a why.
func (a *AddOnPurchase) Cancel(reason string) error {
	if a.Status == AddOnStatusCancelled {
		return errors.Conflict("addon.already_cancelled", "add-on already cancelled")
	}
	if a.Status == AddOnStatusExpired {
		return errors.Conflict("addon.expired", "cannot cancel an expired add-on")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("addon.cancel_reason_required", "cancel reason is required")
	}
	now := time.Now().UTC()
	a.Status = AddOnStatusCancelled
	a.CancelledAt = &now
	a.CancelReason = reason
	a.UpdatedAt = now
	return nil
}

// Expire records valid_until-driven expiry. Called by the recurring
// scheduler when the add-on's valid_until window has elapsed.
func (a *AddOnPurchase) Expire() error {
	if a.Status == AddOnStatusExpired {
		return nil
	}
	if a.Status == AddOnStatusCancelled {
		return errors.Conflict("addon.cancelled", "cannot expire a cancelled add-on")
	}
	a.Status = AddOnStatusExpired
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// IsBillable reports whether the add-on contributes to the next-cycle
// recurring invoice. Pure read; no side effects.
func (a *AddOnPurchase) IsBillable() bool {
	return a.Status == AddOnStatusActive
}
