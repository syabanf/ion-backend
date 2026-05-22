// M6 r3 domain types: termination requests + referral rewards.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// TerminationKind mirrors billing.termination_requests.kind.
type TerminationKind string

const (
	TerminationKindVoluntary TerminationKind = "voluntary"
	TerminationKindAuto      TerminationKind = "auto"
)

// TerminationStatus mirrors billing.termination_requests.status.
type TerminationStatus string

const (
	TerminationStatusRequested        TerminationStatus = "requested"
	TerminationStatusAwaitingPayment  TerminationStatus = "awaiting_payment"
	TerminationStatusWOPending        TerminationStatus = "wo_pending"
	TerminationStatusWOCreated        TerminationStatus = "wo_created"
	TerminationStatusCompleted        TerminationStatus = "completed"
	TerminationStatusCancelled        TerminationStatus = "cancelled"
)

// TerminationRequest is the audit + state row driving both voluntary
// and auto-termination. We deliberately persist outstanding_at_request
// and penalty_amount so the request is a self-describing snapshot even
// after the underlying invoice state changes.
type TerminationRequest struct {
	ID                   uuid.UUID
	CustomerID           uuid.UUID
	OrderID              *uuid.UUID
	Kind                 TerminationKind
	Status               TerminationStatus
	Reason               string
	RequestedByUserID    *uuid.UUID
	FinalInvoiceID       *uuid.UUID
	PenaltyAmount        float64
	OutstandingAtRequest float64
	WOID                 *uuid.UUID
	RequestedAt          time.Time
	CompletedAt          *time.Time
	Notes                string
}

// ReferralRewardStatus mirrors billing.referral_rewards.status.
type ReferralRewardStatus string

const (
	ReferralRewardAccrued ReferralRewardStatus = "accrued"
	ReferralRewardPaid    ReferralRewardStatus = "paid"
	ReferralRewardVoid    ReferralRewardStatus = "void"
)

// ReferralRewardPercentOfMonthly is the round-3 fixed share of one
// monthly_price that a referrer accrues when the referee's first OTC
// is paid in full. Held in code rather than policy because it's a
// product decision rather than an operational knob.
const ReferralRewardPercentOfMonthly = 50.0

type ReferralReward struct {
	ID                  uuid.UUID
	ReferralID          uuid.UUID
	ReferrerCustomerID  *uuid.UUID
	RefereeCustomerID   uuid.UUID
	OrderID             *uuid.UUID
	InvoiceID           *uuid.UUID
	Amount              float64
	Status              ReferralRewardStatus
	PaidAt              *time.Time
	Notes               string
	CreatedAt           time.Time
}
