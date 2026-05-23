package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SubscriberStatus tracks the per-subscriber lifecycle.
//
// Lifecycle:
//
//	active ↔ suspended  →  terminated  (terminal)
//
// The DB enforces the enum via CHECK; the domain enforces the legal
// transitions in the methods below. Terminated is irreversible — once
// a subscriber is terminated the reseller has to onboard a new row
// rather than resurrect this one (matches the customer-billing TCs:
// terminated rows are kept for historical invoice traceability).
type SubscriberStatus string

const (
	SubscriberStatusActive     SubscriberStatus = "active"
	SubscriberStatusSuspended  SubscriberStatus = "suspended"
	SubscriberStatusTerminated SubscriberStatus = "terminated"
)

// Subscriber is one B2B2C end-customer owned by a reseller. Every read
// MUST be tenant-scoped via reseller_account_id — the repo refuses any
// List/Find that omits a non-nil tenant filter so a missing WHERE
// clause becomes a refusal rather than a cross-tenant leak.
//
// ServicePlanID / SubAreaID are plain UUIDs — they reference rows
// owned by other bounded contexts (catalog / network area). Wave 102
// keeps them opt-in nullables so onboarding can happen without those
// modules wired up. The Display layer resolves them later.
type Subscriber struct {
	ID                uuid.UUID
	ResellerAccountID uuid.UUID
	CustomerName      string
	CustomerEmail     string
	CustomerPhone     string
	AddressLine       string
	SubAreaID         *uuid.UUID
	ServicePlanID     *uuid.UUID
	MonthlyFee        float64
	Status            SubscriberStatus
	Notes             string
	ActivatedAt       time.Time
	SuspendedAt       *time.Time
	TerminatedAt      *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time

	// SuspendReason is captured on Suspend() and cleared on Reactivate.
	// Kept off the DB schema (the schema has no column for it) so this
	// is purely an in-flight value when the usecase surfaces a fresh
	// suspension reason; persistence would require a column add.
	SuspendReason string
}

// NewSubscriber constructs an active subscriber attached to a reseller.
// The caller (usecase) is responsible for validating the tenant id
// matches the resolved tenant from the request context before calling.
func NewSubscriber(resellerID uuid.UUID, name, email, phone string, monthlyFee float64) (*Subscriber, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("subscriber.reseller_required", "reseller_account_id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.Validation("subscriber.name_required", "customer_name is required")
	}
	if monthlyFee < 0 {
		return nil, errors.Validation("subscriber.fee_negative", "monthly_fee must be >= 0")
	}
	now := time.Now().UTC()
	return &Subscriber{
		ID:                uuid.New(),
		ResellerAccountID: resellerID,
		CustomerName:      name,
		CustomerEmail:     strings.TrimSpace(email),
		CustomerPhone:     strings.TrimSpace(phone),
		MonthlyFee:        monthlyFee,
		Status:            SubscriberStatusActive,
		ActivatedAt:       now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Suspend moves active → suspended. Reason is required so the
// reseller portal can show a human-readable explanation. Idempotent
// on already-suspended; refuses terminated.
func (s *Subscriber) Suspend(reason string, at time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("subscriber.suspend_reason_required", "reason is required")
	}
	switch s.Status {
	case SubscriberStatusSuspended:
		s.SuspendReason = reason
		return nil
	case SubscriberStatusTerminated:
		return errors.Conflict("subscriber.cannot_suspend", "terminated subscribers cannot be suspended")
	}
	atUTC := at.UTC()
	s.Status = SubscriberStatusSuspended
	s.SuspendReason = reason
	s.SuspendedAt = &atUTC
	s.UpdatedAt = atUTC
	return nil
}

// Reactivate moves suspended → active. Idempotent on already-active;
// refuses terminated (irreversible by design).
func (s *Subscriber) Reactivate(at time.Time) error {
	switch s.Status {
	case SubscriberStatusActive:
		return nil
	case SubscriberStatusTerminated:
		return errors.Conflict("subscriber.cannot_reactivate", "terminated subscribers cannot be reactivated")
	}
	atUTC := at.UTC()
	s.Status = SubscriberStatusActive
	s.SuspendReason = ""
	s.SuspendedAt = nil
	s.UpdatedAt = atUTC
	return nil
}

// Terminate is the irreversible exit. Allowed from any state;
// idempotent on already-terminated.
func (s *Subscriber) Terminate(at time.Time) error {
	if s.Status == SubscriberStatusTerminated {
		return nil
	}
	atUTC := at.UTC()
	s.Status = SubscriberStatusTerminated
	s.TerminatedAt = &atUTC
	s.UpdatedAt = atUTC
	return nil
}

// IsActive reports whether the subscriber is collecting service. Used
// by the dashboard counts so the boundary is one place rather than
// scattered status equality checks.
func (s *Subscriber) IsActive() bool {
	return s.Status == SubscriberStatusActive
}
