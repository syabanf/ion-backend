// Package port defines the contracts between the CRM usecase layer and the
// world outside it. Same hexagonal pattern as the other contexts.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/domain"
)

// =====================================================================
// Driving port (UseCase contract)
// =====================================================================

// --- Products ---

type CreateProductInput struct {
	Code         string
	Name         string
	SpeedMbps    int
	MonthlyPrice float64
	OTCPrice     float64
	// Wave 77 (TC-PRD-014/016/018/022): per-kind schema slot FKs.
	// All optional; nil → resolver falls through to customer-type default.
	OnboardingSchemaID  *uuid.UUID
	BillingSchemaID     *uuid.UUID
	ServiceSchemaID     *uuid.UUID
	CommissionSchemaID  *uuid.UUID
	SuspensionSchemaID  *uuid.UUID
}

// UpdateProductInput allows partial updates including schema slot
// reassignment. Use the Clear*Schema flags to explicitly null a slot
// (clearing falls the resolver back to the customer-type default).
type UpdateProductInput struct {
	ID                  uuid.UUID
	Name                *string
	SpeedMbps           *int
	MonthlyPrice        *float64
	OTCPrice            *float64
	TempWindowHrs       *int
	Active              *bool
	OnboardingSchemaID  *uuid.UUID
	ClearOnboarding     bool
	BillingSchemaID     *uuid.UUID
	ClearBilling        bool
	ServiceSchemaID     *uuid.UUID
	ClearService        bool
	CommissionSchemaID  *uuid.UUID
	ClearCommission     bool
	SuspensionSchemaID  *uuid.UUID
	ClearSuspension     bool
}

type ProductListFilter struct {
	Search     string
	ActiveOnly bool
	Limit      int
	Offset     int
}

// --- Leads ---

// CreateLeadInput is what the HTTP layer hands to the usecase for a new lead.
// The usecase is responsible for running the coverage check and stamping
// the snapshot/verdict onto the lead.
type CreateLeadInput struct {
	FullName           string
	Phone              string
	Email              string
	NIK                string
	Address            string
	GPSLat             *float64
	GPSLng             *float64
	ProductID          *uuid.UUID
	SalesID            *uuid.UUID
	Source             string
	Notes              string
	AcceptExcessCable  bool
	CreatedBy          *uuid.UUID
	// Wave 76 additions.
	LeadType           string     // 'broadband' (default) | 'enterprise'
	ReferrerCustomerID *uuid.UUID // required when Source = 'referral'
}

// UpdateLeadInput allows partial updates to a lead in flight.
type UpdateLeadInput struct {
	ID                 uuid.UUID
	FullName           *string
	Phone              *string
	Email              *string
	NIK                *string
	Address            *string
	GPSLat             *float64
	GPSLng             *float64
	ClearGPS           bool
	ProductID          *uuid.UUID
	ClearProduct       bool
	SalesID            *uuid.UUID
	ClearSales         bool
	Notes              *string
	AcceptExcessCable  *bool
	Status             *domain.LeadStatus
	// Note: LeadType is immutable post-create per TC-CRM-003.
}

// LeadListFilter — server-side filtering.
type LeadListFilter struct {
	Status   string
	BranchID *uuid.UUID
	SalesID  *uuid.UUID
	Search   string
	Limit    int
	Offset   int
}

// LeadWithDocs is the rich shape used for detail views.
type LeadWithDocs struct {
	Lead         domain.Lead
	ProductName  string
	ProductCode  string
	BranchName   string
	BranchCode   string
	SalesName    string
	ReferrerName string // Wave 76 (TC-CRM-010): joined from crm.customers
	Documents    []domain.OrderDocument
}

// --- Conversion ---

// ConvertLeadInput is intentionally tiny — most data is already on the lead.
// `PerformedBy` records who hit the convert button.
type ConvertLeadInput struct {
	LeadID      uuid.UUID
	PerformedBy uuid.UUID
}

// ConvertLeadOutput returns the fresh customer + order so the UI can navigate.
type ConvertLeadOutput struct {
	Customer domain.Customer
	Order    domain.Order
}

// --- Documents ---

type UpdateDocumentInput struct {
	ID        uuid.UUID
	Submitted *bool
	FileURL   *string
	Notes     *string
}

// =====================================================================
// Driven ports (Repository contracts)
// =====================================================================

type ProductRepository interface {
	Create(ctx context.Context, p *domain.Product) error
	List(ctx context.Context, f ProductListFilter) ([]domain.Product, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Product, error)
	Update(ctx context.Context, p *domain.Product) error
}

type LeadRepository interface {
	Create(ctx context.Context, l *domain.Lead, docs []domain.OrderDocument) error
	Update(ctx context.Context, l *domain.Lead) error
	List(ctx context.Context, f LeadListFilter) ([]LeadWithDocs, int, error) // total for paging
	FindByID(ctx context.Context, id uuid.UUID) (*LeadWithDocs, error)
	MarkConverted(ctx context.Context, leadID, customerID, orderID uuid.UUID, at time.Time) error
}

type DocumentRepository interface {
	Update(ctx context.Context, id uuid.UUID, in UpdateDocumentInput) (*domain.OrderDocument, error)
	ListForLead(ctx context.Context, leadID uuid.UUID) ([]domain.OrderDocument, error)
}

type CustomerRepository interface {
	Create(ctx context.Context, c *domain.Customer) error
	List(ctx context.Context, status string, limit, offset int) ([]domain.Customer, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Customer, error)
	// Wave 80b (TC-SCH-011/015/023/026, TC-PRD-025): persist locked
	// schema version IDs onto an existing customer. Used at lead
	// conversion right after the resolver snapshots each kind.
	// Nil entries are no-ops; non-nil values overwrite.
	UpdateLockedSchemaVersions(ctx context.Context, customerID uuid.UUID, locks LockedSchemaVersions) error
}

// LockedSchemaVersions is the payload UpdateLockedSchemaVersions accepts.
// Each pointer is independent — only non-nil kinds get persisted, so
// callers can lock a subset of kinds without disturbing the rest.
type LockedSchemaVersions struct {
	Onboarding  *uuid.UUID
	Billing     *uuid.UUID
	Service     *uuid.UUID
	Commission  *uuid.UUID
	Suspension  *uuid.UUID
}

type OrderRepository interface {
	Create(ctx context.Context, o *domain.Order) error
	List(ctx context.Context, status string, limit, offset int) ([]domain.Order, int, error)
	ListForCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]domain.Order, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Order, error)
}

// CoverageGateway is a *driven* port back to the network bounded context.
// We model it as a port instead of importing network directly so this
// context can ship in its own binary later without an in-process call.
type CoverageGateway interface {
	Check(ctx context.Context, lat, lng float64) (*CoverageDecision, error)
}

// SchemaResolverGateway is a *driven* port back to the platform bounded
// context. Wave 80b (TC-SCH-011/015/023/026, TC-PRD-025) uses it at lead
// conversion to snapshot the resolved schema version for each of the 5
// kinds onto the new customer record. Failures here are non-fatal —
// conversion still succeeds; the customer just falls through to the
// existing DEFAULT-code resolver path on subsequent reads. The audit
// log captures the gap.
//
// Each kind is its own RPC because the platform service's resolver
// signature is per-kind. Returns the resolved SchemaDefinition.ID (the
// row in platform.schema_definitions) — that's what gets persisted to
// crm.customers.locked_<kind>_schema_version_id.
type SchemaResolverGateway interface {
	ResolveVersionForCustomer(
		ctx context.Context,
		customerID uuid.UUID,
		kind string, // 'onboarding' | 'billing' | 'service' | 'commission' | 'suspension'
		productSchemaSlotID *uuid.UUID,
	) (*uuid.UUID, error)
}

// BillingGateway is a *driven* port to the billing context, invoked from
// ConvertLead so that creating a new order auto-spawns its OTC invoice.
// Implementations may be in-process today and HTTP later. Failures are
// non-fatal to convert (we don't want a billing outage to block customer
// creation); the service logs and continues.
type BillingGateway interface {
	CreateOTCForOrder(ctx context.Context, in OTCRequest) error
}

// OTCRequest is the narrow projection billing needs to issue an OTC invoice.
//
// `OTCType` selects the dispatch path (Gap B):
//   - "free"     — no invoice generated; billing logs an audit-only skip.
//   - "prepaid"  — invoice issued immediately; activation gates on payment.
//   - "postpaid" — invoice deferred to the activation hook (default; matches
//     pre-Gap-B behaviour).
//
// Empty string is treated as "postpaid" so older callers don't trip on the
// new field.
type OTCRequest struct {
	OrderID      uuid.UUID
	CustomerID   uuid.UUID
	OTCType      string
	OTCAmount    float64
	ExcessAmount float64 // 0 when accept_excess_cable=false
	ProductLabel string  // e.g. "BB-30 · 30 Mbps Home" for the line description
}

// CoverageDecision is the narrow projection of network.CoverageResult we
// actually need. We keep it small so the gateway can be backed by HTTP or
// in-process equally — and so the CRM domain doesn't bleed into network's.
type CoverageDecision struct {
	Verdict          domain.CoverageVerdict
	Snapshot         []byte // jsonb payload to persist as-is
	NearestNodeID    *uuid.UUID
	BranchID         *uuid.UUID
	CableDistanceM   *float64
	ExcessCharge     *float64
}

// =====================================================================
// UseCase (driving contract — what the HTTP layer calls)
// =====================================================================

type UseCase interface {
	// Products
	ListProducts(ctx context.Context, f ProductListFilter) ([]domain.Product, error)
	CreateProduct(ctx context.Context, in CreateProductInput) (*domain.Product, error)
	UpdateProduct(ctx context.Context, in UpdateProductInput) (*domain.Product, error)
	GetProduct(ctx context.Context, id uuid.UUID) (*domain.Product, error)

	// Leads
	CreateLead(ctx context.Context, in CreateLeadInput) (*LeadWithDocs, error)
	UpdateLead(ctx context.Context, in UpdateLeadInput) (*LeadWithDocs, error)
	ListLeads(ctx context.Context, f LeadListFilter) ([]LeadWithDocs, int, error)
	GetLead(ctx context.Context, id uuid.UUID) (*LeadWithDocs, error)

	// Documents
	UpdateDocument(ctx context.Context, in UpdateDocumentInput) (*domain.OrderDocument, error)

	// Conversion
	ConvertLead(ctx context.Context, in ConvertLeadInput) (*ConvertLeadOutput, error)

	// Customers / Orders
	ListCustomers(ctx context.Context, status string, limit, offset int) ([]domain.Customer, int, error)
	GetCustomer(ctx context.Context, id uuid.UUID) (*domain.Customer, error)
	ListOrders(ctx context.Context, status string, limit, offset int) ([]domain.Order, int, error)
	ListOrdersForCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]domain.Order, int, error)
	GetOrder(ctx context.Context, id uuid.UUID) (*domain.Order, error)

	// M4 r2 — Onboarding schemas
	ListOnboardingSchemas(ctx context.Context) ([]domain.OnboardingSchema, error)
	GetOnboardingSchema(ctx context.Context, id uuid.UUID) (*domain.OnboardingSchema, error)

	// M4 r2 — Sales dashboard
	SalesDashboard(ctx context.Context, in SalesDashboardInput) (*SalesDashboardView, error)
}

// =====================================================================
// M4 r2 — Onboarding schema repo (driven port)
// =====================================================================

type OnboardingSchemaRepository interface {
	// FindActive returns the active schema for the (customer_type, product_type)
	// pair. Returns NotFound if no active schema exists.
	FindActive(ctx context.Context, customerType, productType string) (*domain.OnboardingSchema, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.OnboardingSchema, error)
	List(ctx context.Context) ([]domain.OnboardingSchema, error)
}

// =====================================================================
// M4 r2 — Sales user lookup gateway
//
// The CRM service needs to know a user's sales_type at lead creation
// time to enforce the type-vs-lead match. We don't want to import
// identity directly, so an interface here keeps the boundary clean.
// =====================================================================

type SalesUserGateway interface {
	// SalesTypeFor returns the sales_type ('broadband'|'enterprise'|'both')
	// of the given user. Returns NotFound when the user is not a sales rep.
	SalesTypeFor(ctx context.Context, userID uuid.UUID) (string, error)
}

// =====================================================================
// M4 r2 — Sales dashboard
//
// SalesDashboardInput scopes the view. When `MineUserID` is set we
// return rows where the lead's sales_id == that user; otherwise the
// view is unscoped (operations_admin / sales_manager).
// =====================================================================

type SalesDashboardInput struct {
	MineUserID *uuid.UUID
}

type SalesDashboardView struct {
	LeadsByStatus map[domain.LeadStatus]int
	ConvertedThisMonth int
	OrdersThisMonth    int
	TotalOTCMonth      float64 // sum of converted orders' OTC + monthly snapshot
	RecentLeads        []LeadWithDocs
	RecentConversions  []domain.Order
}
