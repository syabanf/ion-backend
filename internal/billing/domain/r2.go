// M6 r2 domain types: billing policy, billing cycle, commission record.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Policy is the singleton config row in billing.policies. The scheduler
// reads it at every tick so updates propagate immediately.
type Policy struct {
	LateFeeGraceDays           int
	LateFeeAmount              float64
	SuspendAfterDays           int
	TerminateAfterSuspendedDays int
	NotifyCustomerDaysBefore   int
	UpdatedBy                  *uuid.UUID
	UpdatedAt                  time.Time
}

// CycleStatus mirrors the billing_cycles CHECK.
type CycleStatus string

const (
	CycleStatusGenerated CycleStatus = "generated"
	CycleStatusSkipped   CycleStatus = "skipped"
	CycleStatusFailed    CycleStatus = "failed"
)

type BillingCycle struct {
	ID          uuid.UUID
	CustomerID  uuid.UUID
	OrderID     uuid.UUID
	PeriodStart time.Time
	PeriodEnd   time.Time
	InvoiceID   *uuid.UUID
	Status      CycleStatus
	Notes       string
	CreatedAt   time.Time
}

// PartyType mirrors the commission_records CHECK.
type PartyType string

const (
	PartySalesPerson           PartyType = "sales_person"
	PartySalesManager          PartyType = "sales_manager"
	PartySalesBranch           PartyType = "sales_branch"
	PartyInfrastructureBranch  PartyType = "infrastructure_branch"
	PartyCompany               PartyType = "company"
)

// DefaultCommissionPercents is the round-2 hardcoded split applied to
// the order's monthly_price at first-payment. Sums to 100.
//
// Per PRD M6 §commission: the infrastructure_branch share is only
// allocated when the order is cross-branch (sales_branch != node's
// branch). When the order is same-branch, that 10% folds into the
// company bucket. The service applies that rule.
var DefaultCommissionPercents = map[PartyType]float64{
	PartySalesPerson:          15.0,
	PartySalesManager:         5.0,
	PartySalesBranch:          10.0,
	PartyInfrastructureBranch: 10.0,
	PartyCompany:              60.0,
}

type CommissionRecord struct {
	ID         uuid.UUID
	OrderID    uuid.UUID
	CustomerID uuid.UUID
	InvoiceID  *uuid.UUID
	PaymentID  *uuid.UUID
	PartyType  PartyType
	UserID     *uuid.UUID
	BranchID   *uuid.UUID
	Amount     float64
	Percentage float64
	BaseAmount float64
	Notes      string
	CreatedAt  time.Time
}
