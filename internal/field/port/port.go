// Package port defines the contracts between the field usecase layer and
// the world outside it. Same hexagonal pattern as the other contexts.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/domain"
)

// =====================================================================
// CRM gateway — driven port back to the CRM context.
//
// Round 1: in-process call to crm.usecase.Service. Round 2 swaps to HTTP
// without touching the field usecase.
// =====================================================================

// OrderProjection is the narrow read the field service needs from CRM
// when creating a WO from an order.
type OrderProjection struct {
	OrderID     uuid.UUID
	CustomerID  uuid.UUID
	FullName    string
	Phone       string
	Address     string
	BranchID    *uuid.UUID
	ProductCode string
	ProductName string
}

// ActivationProjection is what the activation hook needs when an
// install BAST is approved: the bits required to provision a RADIUS
// account (username + bandwidth profile) plus the customer to flip.
type ActivationProjection struct {
	CustomerID     uuid.UUID
	CustomerNumber string
	OrderID        uuid.UUID
	ProductID      *uuid.UUID
	ProductCode    string
	// SpeedMbps maps to a coarse bandwidth profile identifier. Round-3
	// uses the product code directly; round-4 will look up a structured
	// profile table.
	SpeedMbps int
}

type CRMGateway interface {
	OrderForWO(ctx context.Context, orderID uuid.UUID) (*OrderProjection, error)
	// ActivationProjectionForOrder returns the projection used by the
	// install-complete hook to provision RADIUS + flip the customer.
	ActivationProjectionForOrder(ctx context.Context, orderID uuid.UUID) (*ActivationProjection, error)
	// SetCustomerActive flips crm.customers.status from
	// 'pending_install' to 'active'. Idempotent on already-active rows.
	SetCustomerActive(ctx context.Context, customerID uuid.UUID) error
}

// ActivationGateway provisions a RADIUS account in TEMPORARY and then
// promotes it to PERMANENT_ACTIVE. Idempotent: if the account already
// exists the Provision call is a no-op, and PromoteToPermanent is a
// safe transition from TEMPORARY → PERMANENT_ACTIVE.
//
// Field-svc holds this gateway directly (round-3 in-process); round-4
// swaps to an HTTP call against network-svc.
type ActivationGateway interface {
	ProvisionAndActivate(ctx context.Context, in ActivationProjection) error
}

// BillingGateway is the cross-context check the BAST verify flow uses
// to enforce the payment-gates-NOC rule, plus the termination-complete
// signal that closes the billing-side termination_request when the
// device-retrieval BAST is approved.
//
// Returns true from IsOrderOTCPaid when the order's OTC invoice has been
// paid (or no OTC exists for the order — see the billing usecase
// comment for that case).
type BillingGateway interface {
	IsOrderOTCPaid(ctx context.Context, orderID uuid.UUID) (bool, error)
	// OnTerminationWOCompleted is called by field-svc after NOC approves
	// a termination-type BAST. The billing side looks up the
	// termination_request by wo_id, flips it to completed, and marks the
	// customer as 'terminated' (which also drives RADIUS DEACTIVATED via
	// the existing network gateway).
	OnTerminationWOCompleted(ctx context.Context, woID uuid.UUID) error
}

// =====================================================================
// Inputs / list filters
// =====================================================================

type CreateWOFromOrderInput struct {
	OrderID       uuid.UUID
	ScheduledDate *time.Time
	Priority      domain.Priority
	Notes         string
	CreatedBy     uuid.UUID
}

// CreateTerminationWOInput is what the billing service hands the field
// service when minting a termination WO. The caller pre-resolved
// customer + address — we don't need the CRM gateway round-trip.
type CreateTerminationWOInput struct {
	CustomerID uuid.UUID
	OrderID    *uuid.UUID
	Address    string
	BranchID   *uuid.UUID
	Notes      string
	CreatedBy  uuid.UUID
}

type WOListFilter struct {
	Status   string
	BranchID *uuid.UUID
	TeamID   *uuid.UUID
	Search   string
	Limit    int
	Offset   int
}

type WODetail struct {
	WO              domain.WorkOrder
	BranchName      string
	BranchCode      string
	TeamName        string
	TeamLeaderName  string
	Assignments     []AssignmentView
	ChecklistTemplate *domain.ChecklistTemplate
	ChecklistItems    []domain.ChecklistTemplateItem
	ChecklistResponses []domain.ChecklistResponse
	ResolutionItems   []domain.ResolutionItem
	ActiveBAST        *domain.BAST
}

type AssignmentView struct {
	Assignment    domain.Assignment
	TechnicianName string
	TechnicianEmail string
}

type AssignTechniciansInput struct {
	WOID         uuid.UUID
	LeadID       uuid.UUID // required
	LeadGrade    domain.TechGrade
	ObserverID   *uuid.UUID
	ObserverGrade *domain.TechGrade
	AssignedBy   uuid.UUID
}

type RouteToTeamInput struct {
	WOID   uuid.UUID
	TeamID uuid.UUID
	By     uuid.UUID
}

type UpdateWOStatusInput struct {
	WOID   uuid.UUID
	Status domain.WOStatus
	Notes  string
	By     uuid.UUID
}

type SubmitChecklistResponseInput struct {
	WOID           uuid.UUID
	TemplateItemID uuid.UUID
	ResponseText   string
	FileURL        string
	GPSLat         *float64
	GPSLng         *float64
	GPSAccuracyM   *float64
	SubmittedBy    uuid.UUID
}

type AddResolutionItemInput struct {
	WOID             uuid.UUID
	ItemLabel        string
	Category         domain.ResolutionCategory
	Finding          string
	ActionTaken      string
	ResolutionStatus domain.ResolutionStatus
	TimeSpentMinutes *int
	ResolvedBy       uuid.UUID
}

type SubmitBASTInput struct {
	WOID           uuid.UUID
	SignOffMode    domain.SignOffMode
	CustomerSigURL string
	OTPUsed        bool
	GPSLat         *float64
	GPSLng         *float64
	CompiledData   []byte // optional; service can compile if empty
	SubmittedBy    uuid.UUID
}

type VerifyBASTInput struct {
	BASTID    uuid.UUID
	Decision  domain.NOCStatus // approved or rejected
	Notes     string
	VerifiedBy uuid.UUID
}

type CreateTeamInput struct {
	Code         string
	Name         string
	BranchID     uuid.UUID
	TeamLeaderID *uuid.UUID
}

type AddTeamMemberInput struct {
	TeamID uuid.UUID
	UserID uuid.UUID
	Grade  domain.TechGrade
}

// =====================================================================
// Repositories (driven ports)
// =====================================================================

type WORepository interface {
	Create(ctx context.Context, w *domain.WorkOrder) error
	Update(ctx context.Context, w *domain.WorkOrder) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.WOStatus) error
	List(ctx context.Context, f WOListFilter) ([]WODetail, int, error) // header only — no children
	FindByID(ctx context.Context, id uuid.UUID) (*WODetail, error)     // full detail
}

type AssignmentRepository interface {
	UpsertPair(ctx context.Context, woID uuid.UUID, lead domain.Assignment, observer *domain.Assignment) error
	ListForWO(ctx context.Context, woID uuid.UUID) ([]AssignmentView, error)
}

type ChecklistRepository interface {
	FindTemplateFor(ctx context.Context, woType domain.WOType, productType, maintSubtype string) (*domain.ChecklistTemplate, []domain.ChecklistTemplateItem, error)
	// FindItem returns a single template item by id — used by the M5 r3
	// GPS gate so we can check gps_required without re-loading the
	// whole template.
	FindItem(ctx context.Context, id uuid.UUID) (*domain.ChecklistTemplateItem, error)
	UpsertResponse(ctx context.Context, r *domain.ChecklistResponse) (*domain.ChecklistResponse, error)
	ListResponses(ctx context.Context, woID uuid.UUID) ([]domain.ChecklistResponse, error)
}

// M5 r3 — UploadsGateway is the narrow projection of the upload service
// that the GPS gate needs. Implemented in cmd-wiring as a thin adapter
// to the in-process uploads.UseCase.
type UploadsGateway interface {
	GPSFor(ctx context.Context, objectURL string) (lat, lng *float64, err error)
}

// RadiusReader is the narrow projection field needs to display ONT
// credentials on-site. Round-3 reads via the in-process network usecase;
// round-4 swaps to an HTTP call. The password is never returned.
type RadiusReader interface {
	RadiusAccountFor(ctx context.Context, customerID uuid.UUID) (*RadiusAccountView, error)
}

type RadiusAccountView struct {
	Username           string
	BandwidthProfileID string
	VLANID             *int
	IPAddress          string
	Status             string
}

type ResolutionRepository interface {
	Add(ctx context.Context, r *domain.ResolutionItem) error
	ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.ResolutionItem, error)
}

type BASTRepository interface {
	Create(ctx context.Context, b *domain.BAST) error
	FindActiveForWO(ctx context.Context, woID uuid.UUID) (*domain.BAST, error) // non-rejected
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BAST, error)
	MarkVerified(ctx context.Context, id uuid.UUID, status domain.NOCStatus, by uuid.UUID, notes string) error
	// M5 r2 — OTP verification for remote sign-off.
	VerifyOTP(ctx context.Context, id uuid.UUID) error
}

// M5 r2 — Reschedule audit + SLA breach view.
type RescheduleRepository interface {
	Create(ctx context.Context, r *domain.Reschedule) error
	ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.Reschedule, error)
	ListSLABreaches(ctx context.Context, limit, offset int) ([]WODetail, int, error)
}

type TeamRepository interface {
	Create(ctx context.Context, t *domain.Team) error
	List(ctx context.Context, branchID *uuid.UUID, activeOnly bool) ([]TeamView, error)
	FindByID(ctx context.Context, id uuid.UUID) (*TeamView, error)
	AddMember(ctx context.Context, m *domain.TeamMember) error
	ListMembers(ctx context.Context, teamID uuid.UUID) ([]TeamMemberView, error)
}

type TeamView struct {
	Team           domain.Team
	BranchName     string
	BranchCode     string
	TeamLeaderName string
	MemberCount    int
}

type TeamMemberView struct {
	Member domain.TeamMember
	Name   string
	Email  string
}

// =====================================================================
// UseCase (driving contract)
// =====================================================================

type UseCase interface {
	// WO lifecycle
	CreateWOFromOrder(ctx context.Context, in CreateWOFromOrderInput) (*WODetail, error)
	CreateTerminationWO(ctx context.Context, in CreateTerminationWOInput) (*WODetail, error)
	ListWOs(ctx context.Context, f WOListFilter) ([]WODetail, int, error)
	GetWO(ctx context.Context, id uuid.UUID) (*WODetail, error)
	RouteToTeam(ctx context.Context, in RouteToTeamInput) (*WODetail, error)
	AssignTechnicians(ctx context.Context, in AssignTechniciansInput) (*WODetail, error)
	UpdateStatus(ctx context.Context, in UpdateWOStatusInput) (*WODetail, error)

	// Checklist + resolution
	SubmitChecklistResponse(ctx context.Context, in SubmitChecklistResponseInput) (*domain.ChecklistResponse, error)
	AddResolutionItem(ctx context.Context, in AddResolutionItemInput) (*domain.ResolutionItem, error)

	// BAST
	SubmitBAST(ctx context.Context, in SubmitBASTInput) (*domain.BAST, error)
	VerifyBAST(ctx context.Context, in VerifyBASTInput) (*domain.BAST, error)

	// Teams
	CreateTeam(ctx context.Context, in CreateTeamInput) (*TeamView, error)
	ListTeams(ctx context.Context, branchID *uuid.UUID, activeOnly bool) ([]TeamView, error)
	GetTeam(ctx context.Context, id uuid.UUID) (*TeamView, error)
	AddTeamMember(ctx context.Context, in AddTeamMemberInput) (*TeamMemberView, error)
	ListTeamMembers(ctx context.Context, teamID uuid.UUID) ([]TeamMemberView, error)

	// M5 r2 — Reschedule + OTP signoff + SLA queue
	RescheduleWO(ctx context.Context, in RescheduleWOInput) (*WODetail, error)
	ListRescheduleHistory(ctx context.Context, woID uuid.UUID) ([]domain.Reschedule, error)
	VerifyBASTOTP(ctx context.Context, in VerifyOTPInput) (*domain.BAST, error)
	ListSLABreaches(ctx context.Context, limit, offset int) ([]WODetail, int, error)

	// M5 r3+ — ONT credential surface (only available during active WO).
	// GetONTConfig is scoped twice: (1) the WO must currently be
	// in_progress, and (2) callerID must be the lead/observer
	// technician on the WO. Passing uuid.Nil skips the assignment
	// check (used by admin tooling that doesn't carry a caller).
	GetONTConfig(ctx context.Context, woID uuid.UUID, callerID uuid.UUID) (*RadiusAccountView, error)
}

// M5 r2 inputs
type RescheduleWOInput struct {
	WOID         uuid.UUID
	Reason       domain.RescheduleReason
	Notes        string
	NewDate      time.Time
	RescheduledBy uuid.UUID
}

type VerifyOTPInput struct {
	BASTID uuid.UUID
	Code   string
}
