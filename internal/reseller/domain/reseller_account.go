// Package domain holds the reseller bounded context's entities and
// value objects.
//
// Rules (same as identity / crm / warehouse / enterprise domains):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map
//     them to the right HTTP status without inspecting strings.
//
// Wave 94 scope: ResellerAccount, WholesaleSKU, WholesaleOrder (+
// lines), and PlatformSession. Together they cover the Reseller
// Onboarding, Wholesale Supply, and Reseller Platform TC families.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ResellerStatus tracks the onboarding + lifecycle state.
//
// Lifecycle:
//
//	pending_kyc → approved → suspended ↔ approved
//	any-non-terminal → terminated  (terminal)
//
// The DB enforces the enum via CHECK; the domain enforces the legal
// transitions in the methods below. The platform middleware refuses
// to issue / accept session tokens for any status other than
// `approved` — the gate lives in the usecase, not the domain, so
// suspension can be lifted without re-onboarding.
type ResellerStatus string

const (
	ResellerStatusPendingKYC ResellerStatus = "pending_kyc"
	ResellerStatusApproved   ResellerStatus = "approved"
	ResellerStatusSuspended  ResellerStatus = "suspended"
	ResellerStatusTerminated ResellerStatus = "terminated"
)

// ResellerAccount is the reseller tenant. Each row is its own platform
// tenant — every row in `reseller.wholesale_orders` and
// `reseller.platform_sessions` carries this account_id and the HTTP
// platform surface scopes every query by it.
//
// ParentSubsidiaryID is a free-form UUID for now — the holding-company
// entity model lands in a later wave; we keep the column as a plain
// pointer so existing rows can stay null until then.
type ResellerAccount struct {
	ID                 uuid.UUID
	ParentSubsidiaryID *uuid.UUID
	Name               string
	NPWP               string
	ContactEmail       string
	ContactPhone       string
	Status             ResellerStatus
	MarginPct          float64 // 0..1, e.g. 0.10 = 10%
	CreditLimit        float64
	Balance            float64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ApprovedAt         *time.Time
	ApprovedBy         *uuid.UUID

	// SuspendReason is captured on Suspend() and surfaced to the admin
	// dashboard. Cleared on the next ApproveKYC / reactivation; nil
	// otherwise.
	SuspendReason string
}

// NewResellerAccount constructs a fresh pending-KYC account.
//
// The default margin (10%) and zero credit are deliberately
// conservative — operators bump them via UpdateAccount in a later
// wave once KYC clears + credit terms are agreed.
func NewResellerAccount(name, npwp, contactEmail, contactPhone string) (*ResellerAccount, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.Validation("reseller.name_required", "name is required")
	}
	now := time.Now().UTC()
	return &ResellerAccount{
		ID:           uuid.New(),
		Name:         name,
		NPWP:         strings.TrimSpace(npwp),
		ContactEmail: strings.TrimSpace(contactEmail),
		ContactPhone: strings.TrimSpace(contactPhone),
		Status:       ResellerStatusPendingKYC,
		MarginPct:    0.10,
		CreditLimit:  0,
		Balance:      0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// ApproveKYC flips pending_kyc → approved and snapshots the approver.
// Idempotent on already-approved (no-op + nil return). Rejects every
// other source state so a terminated row can't be quietly resurrected.
func (a *ResellerAccount) ApproveKYC(approver uuid.UUID) error {
	switch a.Status {
	case ResellerStatusApproved:
		return nil
	case ResellerStatusPendingKYC, ResellerStatusSuspended:
		// Allow re-approval from suspended to support the
		// "suspension lifted" path without forcing a new onboarding.
	default:
		return errors.Conflict(
			"reseller.cannot_approve",
			"only pending_kyc or suspended accounts can be approved",
		)
	}
	now := time.Now().UTC()
	a.Status = ResellerStatusApproved
	a.ApprovedAt = &now
	a.ApprovedBy = &approver
	a.SuspendReason = ""
	a.UpdatedAt = now
	return nil
}

// Suspend pauses an approved account. Reason is required so the
// reseller-facing portal can show a human-readable explanation rather
// than just "access denied". Idempotent on already-suspended; rejects
// pending_kyc + terminated.
func (a *ResellerAccount) Suspend(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("reseller.suspend_reason_required", "reason is required")
	}
	if a.Status == ResellerStatusSuspended {
		a.SuspendReason = reason
		return nil
	}
	if a.Status != ResellerStatusApproved {
		return errors.Conflict(
			"reseller.cannot_suspend",
			"only approved accounts can be suspended",
		)
	}
	a.Status = ResellerStatusSuspended
	a.SuspendReason = reason
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// Terminate is the irreversible exit. Allowed from any non-terminal
// state; idempotent on already-terminated. The platform surface
// refuses every operation for a terminated tenant — the gate is in
// the usecase.
func (a *ResellerAccount) Terminate() error {
	if a.Status == ResellerStatusTerminated {
		return nil
	}
	a.Status = ResellerStatusTerminated
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// IsOperational reports whether the account can transact on the
// platform — used by the platform-tenant resolver to short-circuit
// suspended/terminated requests with a typed error.
func (a *ResellerAccount) IsOperational() bool {
	return a.Status == ResellerStatusApproved
}
