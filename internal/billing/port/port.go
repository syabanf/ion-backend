// Package port defines the contracts between the billing usecase layer
// and the world outside it.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
)

// =====================================================================
// Inputs
// =====================================================================

type LineItemInput struct {
	Description string
	ItemType    string
	Quantity    float64
	UnitPrice   float64
}

type CreateInvoiceInput struct {
	CustomerID  uuid.UUID
	OrderID     *uuid.UUID
	InvoiceType domain.InvoiceType
	Lines       []LineItemInput
	PPNRate     float64
	DueDate     time.Time
	Notes       string
	CreatedBy   *uuid.UUID
	// IssueImmediately: when true the invoice is created as 'issued' instead
	// of 'draft'. Auto-creating an OTC invoice from CRM uses this to skip
	// the manual issue step (finance staff still flip-to-paid manually).
	IssueImmediately bool
}

type RecordPaymentInput struct {
	InvoiceID            uuid.UUID
	Amount               float64
	PaymentMethod        string
	GatewayTransactionID string
	Notes                string
	ConfirmedBy          uuid.UUID
}

type InvoiceListFilter struct {
	Status      string
	InvoiceType string
	CustomerID  *uuid.UUID
	OrderID     *uuid.UUID
	Search      string
	Limit       int
	Offset      int
}

// InvoiceView is what we return from list/get — invoice + denormalized
// customer/order labels + the line items + payments.
type InvoiceView struct {
	Invoice           domain.Invoice
	CustomerName      string
	CustomerNumber    string
	OrderNumber       string
	Lines             []domain.LineItem
	Payments          []domain.Payment
	PaidAmount        float64 // sum of confirmed payments
	OutstandingAmount float64 // total - PaidAmount (>= 0)
}

// =====================================================================
// Repositories (driven ports)
// =====================================================================

type InvoiceRepository interface {
	Create(ctx context.Context, inv *domain.Invoice, lines []domain.LineItem) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.InvoiceStatus, paidAt *time.Time) error
	FindByID(ctx context.Context, id uuid.UUID) (*InvoiceView, error)
	FindOTCForOrder(ctx context.Context, orderID uuid.UUID) (*InvoiceView, error)
	List(ctx context.Context, f InvoiceListFilter) ([]InvoiceView, int, error)
}

type PaymentRepository interface {
	Create(ctx context.Context, p *domain.Payment) error
	SumConfirmedForInvoice(ctx context.Context, invoiceID uuid.UUID) (float64, error)
}

// =====================================================================
// UseCase (driving contract)
// =====================================================================

type UseCase interface {
	CreateInvoice(ctx context.Context, in CreateInvoiceInput) (*InvoiceView, error)
	IssueInvoice(ctx context.Context, id uuid.UUID) (*InvoiceView, error)
	CancelInvoice(ctx context.Context, id uuid.UUID) (*InvoiceView, error)
	GetInvoice(ctx context.Context, id uuid.UUID) (*InvoiceView, error)
	ListInvoices(ctx context.Context, f InvoiceListFilter) ([]InvoiceView, int, error)

	RecordPayment(ctx context.Context, in RecordPaymentInput) (*InvoiceView, error)

	// IsOrderOTCPaid is the cross-context check the field service calls
	// before letting NOC approve a BAST.
	IsOrderOTCPaid(ctx context.Context, orderID uuid.UUID) (bool, error)

	// M6 r2 — recurring + late fees + suspension + commission.
	GetPolicy(ctx context.Context) (*domain.Policy, error)
	UpdatePolicy(ctx context.Context, in UpdatePolicyInput) (*domain.Policy, error)
	RunBillingTick(ctx context.Context, now time.Time) (*TickReport, error)
	ListBillingCycles(ctx context.Context, f CycleFilter) ([]domain.BillingCycle, int, error)
	ListCommissions(ctx context.Context, f CommissionFilter) ([]domain.CommissionRecord, error)

	// M6 r3 — voluntary termination + referral.
	RequestVoluntaryTermination(ctx context.Context, in RequestTerminationInput) (*domain.TerminationRequest, error)
	CancelTerminationRequest(ctx context.Context, id uuid.UUID, by uuid.UUID, reason string) (*domain.TerminationRequest, error)
	ListTerminationRequests(ctx context.Context, f TerminationRequestFilter) ([]domain.TerminationRequest, int, error)
	GetTerminationRequest(ctx context.Context, id uuid.UUID) (*domain.TerminationRequest, error)
	ListReferralRewards(ctx context.Context, f ReferralRewardFilter) ([]domain.ReferralReward, error)

	// M6 r3 — customer-portal self-service.
	RequestTerminationOTP(ctx context.Context, in PortalRequestTerminationOTPInput) (*PortalRequestTerminationOTPOutput, error)
	ConfirmTermination(ctx context.Context, in PortalConfirmTerminationInput) (*PortalConfirmTerminationOutput, error)
}

// PortalConfirmTerminationOutput is the narrow projection the public
// confirm endpoint returns — the customer only needs to know that the
// request was filed and what state it's in.
type PortalConfirmTerminationOutput struct {
	TerminationID uuid.UUID
	Status        string
}

// =====================================================================
// M6 r2 driving inputs
// =====================================================================

type UpdatePolicyInput struct {
	LateFeeGraceDays            *int
	LateFeeAmount               *float64
	SuspendAfterDays            *int
	TerminateAfterSuspendedDays *int
	NotifyCustomerDaysBefore    *int
	UpdatedBy                   uuid.UUID
}

// TickReport — summary of one scheduler run. Surfaced via the manual
// /cycles/run endpoint so admins can verify what happened.
type TickReport struct {
	StartedAt           time.Time
	CompletedAt         time.Time
	RecurringGenerated  int
	RecurringSkipped    int
	LateFeesApplied     int
	CustomersSuspended  int
	CustomersRestored   int
	TerminationsTriggered int
	Errors              []string
}

type CycleFilter struct {
	CustomerID *uuid.UUID
	Status     string
	Limit      int
	Offset     int
}

type CommissionFilter struct {
	UserID    *uuid.UUID
	BranchID  *uuid.UUID
	OrderID   *uuid.UUID
	PartyType string
	Limit     int
}

// =====================================================================
// M6 r2 driven repos
// =====================================================================

type PolicyRepository interface {
	Get(ctx context.Context) (*domain.Policy, error)
	Update(ctx context.Context, in UpdatePolicyInput) (*domain.Policy, error)
}

type CycleRepository interface {
	Create(ctx context.Context, c *domain.BillingCycle) error
	List(ctx context.Context, f CycleFilter) ([]domain.BillingCycle, int, error)
	ExistsForPeriod(ctx context.Context, customerID uuid.UUID, periodStart time.Time) (bool, error)
}

type CommissionRepository interface {
	Create(ctx context.Context, rec *domain.CommissionRecord) error
	List(ctx context.Context, f CommissionFilter) ([]domain.CommissionRecord, error)
	ExistsForOrder(ctx context.Context, orderID uuid.UUID) (bool, error)
}

// =====================================================================
// M6 r2 — CRM gateway (read customer + order to drive recurring/commission)
// =====================================================================

// CRMGateway is the projection of CRM data the billing scheduler needs.
// In-process today; HTTP later.
type CRMGateway interface {
	// ActiveOrdersForRecurring returns orders whose customer is in
	// status 'active' so the scheduler knows who to bill this month.
	ActiveOrdersForRecurring(ctx context.Context) ([]RecurringOrder, error)
	// OrderWithCustomer returns the full order projection for
	// commission calculation.
	OrderWithCustomer(ctx context.Context, orderID uuid.UUID) (*RecurringOrder, error)
	// SetCustomerStatus drives suspension / restore / termination.
	SetCustomerStatus(ctx context.Context, customerID uuid.UUID, status string, reason string) error
	// ManagerOfSales walks user.reports_to up the chain until it finds
	// a user with role 'sales_manager'. Returns nil when none found.
	ManagerOfSales(ctx context.Context, salesUserID uuid.UUID) (*uuid.UUID, error)
	// SalesBranchOf reads sales user's branch_id.
	SalesBranchOf(ctx context.Context, salesUserID uuid.UUID) (*uuid.UUID, error)

	// M6 r3 — suspension + termination + referral surfaces.

	// SuspendedCustomers returns every customer currently in 'suspended'
	// status, with the timestamp of when they were last suspended. The
	// scheduler uses this to drive the restore pass + the auto-termination
	// pass.
	SuspendedCustomers(ctx context.Context) ([]SuspendedCustomer, error)
	// CustomerSummary is a small projection used by the voluntary
	// termination flow + auto-termination notice.
	CustomerSummary(ctx context.Context, customerID uuid.UUID) (*CustomerSummary, error)
	// RecordReferral attaches the referee customer to a referrer (resolved
	// via referral_code or referrer_customer_id). Returns the created or
	// existing row.
	RecordReferral(ctx context.Context, refereeID uuid.UUID, code string, referrerID *uuid.UUID) (*ReferralRow, error)
	// ReferralForReferee returns the referral row for a referee, or nil.
	ReferralForReferee(ctx context.Context, refereeID uuid.UUID) (*ReferralRow, error)
}

// SuspendedCustomer — minimal projection of a suspended customer row.
type SuspendedCustomer struct {
	CustomerID   uuid.UUID
	SuspendedAt  *time.Time
	OrderID      *uuid.UUID
	Address      string
	BranchID     *uuid.UUID
	LockInUntil  *time.Time
}

// CustomerSummary — projection used by voluntary termination + reward.
type CustomerSummary struct {
	CustomerID   uuid.UUID
	OrderID      *uuid.UUID
	Status       string
	Address      string
	BranchID     *uuid.UUID
	ActivatedAt  *time.Time
	LockInUntil  *time.Time
	MonthlyPrice float64
	OTCPrice     float64
}

// ReferralRow mirrors crm.referrals.
type ReferralRow struct {
	ID                 uuid.UUID
	ReferrerCustomerID *uuid.UUID
	RefereeCustomerID  uuid.UUID
	ReferrerCode       string
	Status             string
	RewardedAt         *time.Time
	CreatedAt          time.Time
}

type RecurringOrder struct {
	OrderID            uuid.UUID
	CustomerID         uuid.UUID
	CustomerStatus     string
	MonthlyPrice       float64
	ActivatedAt        *time.Time // first time customer became active
	SalesID            *uuid.UUID
	OrderBranchID      *uuid.UUID // the branch that sold this — typically sales_branch
	InfrastructureNode *uuid.UUID // the WO's installation_node_id; commission uses its branch
}

// =====================================================================
// M6 r2 — Network gateway (RADIUS hook for suspend/restore)
//
// Suspend flips the customer's RADIUS account to SUSPENDED; restore
// flips it back. Round-2 uses the in-process network usecase; round-4
// can swap to HTTP when network ships standalone.
// =====================================================================

type NetworkGateway interface {
	SuspendCustomer(ctx context.Context, customerID uuid.UUID, reason string) error
	RestoreCustomer(ctx context.Context, customerID uuid.UUID) error
	// DeactivateCustomer drives the terminal RADIUS state. Used by the
	// termination-completion hook.
	DeactivateCustomer(ctx context.Context, customerID uuid.UUID, reason string) error
}

// =====================================================================
// M6 r3 — Field gateway (mint termination WOs)
// =====================================================================

// FieldGateway is the narrow projection billing needs from field. Used
// by the auto-termination + voluntary termination flows. Round-3 in-
// process; round-4 swaps to HTTP.
type FieldGateway interface {
	CreateTerminationWO(ctx context.Context, in CreateTerminationWOInput) (uuid.UUID, error)
}

type CreateTerminationWOInput struct {
	CustomerID uuid.UUID
	OrderID    *uuid.UUID
	Address    string
	BranchID   *uuid.UUID
	Notes      string
	CreatedBy  uuid.UUID
}

// =====================================================================
// M6 r3 — Termination request + referral repos
// =====================================================================

type TerminationRequestRepository interface {
	Create(ctx context.Context, t *domain.TerminationRequest) error
	Update(ctx context.Context, t *domain.TerminationRequest) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.TerminationRequest, error)
	List(ctx context.Context, f TerminationRequestFilter) ([]domain.TerminationRequest, int, error)
	// FindOpenForCustomer returns any non-terminal row blocking a fresh request.
	FindOpenForCustomer(ctx context.Context, customerID uuid.UUID) (*domain.TerminationRequest, error)
	// FindByWOID is used by the termination-complete hook from field-svc.
	FindByWOID(ctx context.Context, woID uuid.UUID) (*domain.TerminationRequest, error)
}

type TerminationRequestFilter struct {
	CustomerID     *uuid.UUID
	Kind           string
	Status         string
	// FinalInvoiceID scopes to requests whose final invoice matches the
	// given id. The invoice detail page uses it to surface a back-link
	// to the originating termination request.
	FinalInvoiceID *uuid.UUID
	Limit          int
	Offset         int
}

type ReferralRewardRepository interface {
	Create(ctx context.Context, r *domain.ReferralReward) error
	List(ctx context.Context, f ReferralRewardFilter) ([]domain.ReferralReward, error)
	ExistsForReferral(ctx context.Context, referralID uuid.UUID) (bool, error)
}

type ReferralRewardFilter struct {
	ReferrerCustomerID *uuid.UUID
	Status             string
	Limit              int
}

// =====================================================================
// M6 r3 — Voluntary termination input
// =====================================================================

type RequestTerminationInput struct {
	CustomerID uuid.UUID
	Reason     string
	RequestedBy uuid.UUID
}
