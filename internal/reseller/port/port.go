// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the reseller bounded context.
//
// Same hexagonal layout as identity / crm / warehouse / enterprise:
// HTTP handlers depend on a UseCase interface; the UseCase depends on
// repository interfaces; postgres adapters implement the repository
// interfaces. The domain stays oblivious to both transport and
// storage so the bounded context can be extracted into its own
// service (cmd/reseller-svc) without touching domain rules.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
)

// =====================================================================
// Reseller onboarding inputs
// =====================================================================

type OnboardResellerInput struct {
	Name               string
	NPWP               string
	ContactEmail       string
	ContactPhone       string
	ParentSubsidiaryID *uuid.UUID
}

type ResellerListFilter struct {
	Status             string
	ParentSubsidiaryID *uuid.UUID
	Limit              int
	Offset             int
}

// =====================================================================
// Wholesale catalog + order inputs
// =====================================================================

type CreateWholesaleSKUInput struct {
	SupplierSubsidiaryID uuid.UUID
	Name                 string
	SKUCode              string
	UnitPrice            float64
	Unit                 string
}

type UpdateWholesaleSKUInput struct {
	ID        uuid.UUID
	Name      *string
	UnitPrice *float64
	Unit      *string
	IsActive  *bool
}

type WholesaleSKUListFilter struct {
	SupplierSubsidiaryID *uuid.UUID
	OnlyActive           bool
	Limit                int
	Offset               int
}

// CreateWholesaleOrderInput is the platform-tenant-scoped create. The
// usecase pulls the reseller_account_id from the request context
// (set by the tenant middleware) — callers MUST NOT take a tenant id
// from the request body. SupplierSubsidiaryID is derived from the
// looked-up SKUs; the usecase rejects mixed-supplier orders.
type CreateWholesaleOrderInput struct {
	ResellerAccountID uuid.UUID
	Lines             []WholesaleOrderLineInput
}

type WholesaleOrderLineInput struct {
	SKUID uuid.UUID
	Qty   int
}

type WholesaleOrderListFilter struct {
	// ResellerAccountID is REQUIRED on the platform surface (tenant
	// isolation). The admin surface passes a zero-value uuid to
	// disable the tenant filter — the usecase doesn't auto-apply it
	// because the call site knows which surface it's on.
	ResellerAccountID uuid.UUID
	Status            string
	Limit             int
	Offset            int
}

// =====================================================================
// UseCase — what the HTTP layer depends on
// =====================================================================

type OnboardingUseCase interface {
	OnboardReseller(ctx context.Context, in OnboardResellerInput) (*domain.ResellerAccount, error)
	ApproveKYC(ctx context.Context, id, approver uuid.UUID) (*domain.ResellerAccount, error)
	Suspend(ctx context.Context, id uuid.UUID, reason string) (*domain.ResellerAccount, error)
	Terminate(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error)
	ListAccounts(ctx context.Context, f ResellerListFilter) ([]domain.ResellerAccount, int, error)
	GetAccount(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error)
}

type WholesaleUseCase interface {
	// Catalog (admin)
	CreateSKU(ctx context.Context, in CreateWholesaleSKUInput) (*domain.WholesaleSKU, error)
	UpdateSKU(ctx context.Context, in UpdateWholesaleSKUInput) (*domain.WholesaleSKU, error)
	ListSKUs(ctx context.Context, f WholesaleSKUListFilter) ([]domain.WholesaleSKU, int, error)

	// Orders (mixed admin + platform)
	CreateOrder(ctx context.Context, in CreateWholesaleOrderInput) (*domain.WholesaleOrder, error)
	SubmitOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	ApproveOrder(ctx context.Context, id, by uuid.UUID) (*domain.WholesaleOrder, error)
	RejectOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	FulfillOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	CancelOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	GetOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	ListOrders(ctx context.Context, f WholesaleOrderListFilter) ([]domain.WholesaleOrder, int, error)
}

// PlatformUseCase covers the reseller-platform auth surface. Issuing
// a session takes the reseller id + a shared secret; verifying a
// session is the resolver below.
type PlatformUseCase interface {
	IssueSession(ctx context.Context, resellerID uuid.UUID, secret string, ttl time.Duration) (*domain.PlatformSession, error)
	PlatformResolver
}

// PlatformResolver is the contract the HTTP tenant middleware depends
// on. Kept as a separate interface so an alternate implementation
// (e.g. a JWT verifier) can be plugged in without dragging the full
// PlatformUseCase in.
type PlatformResolver interface {
	ResolveTenant(ctx context.Context, sessionToken string) (resellerAccountID uuid.UUID, err error)
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type ResellerAccountRepository interface {
	Create(ctx context.Context, a *domain.ResellerAccount) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error)
	List(ctx context.Context, f ResellerListFilter) ([]domain.ResellerAccount, int, error)
	// UpdateStatus persists status + the per-status timestamps + the
	// suspend reason. Used by ApproveKYC / Suspend / Terminate so the
	// adapter doesn't grow one UPDATE per transition.
	UpdateStatus(ctx context.Context, a *domain.ResellerAccount) error
}

type WholesaleSKURepository interface {
	Create(ctx context.Context, s *domain.WholesaleSKU) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.WholesaleSKU, error)
	FindByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.WholesaleSKU, error)
	List(ctx context.Context, f WholesaleSKUListFilter) ([]domain.WholesaleSKU, int, error)
	Update(ctx context.Context, s *domain.WholesaleSKU) error
}

type WholesaleOrderRepository interface {
	// Create persists the header + every line in a single transaction
	// so a partially-saved order can never exist.
	Create(ctx context.Context, o *domain.WholesaleOrder) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error)
	List(ctx context.Context, f WholesaleOrderListFilter) ([]domain.WholesaleOrder, int, error)
	// UpdateStatus persists status + the per-status timestamps and
	// approver id. Lines are immutable after submission so we don't
	// expose a line-mutating method on the repo.
	UpdateStatus(ctx context.Context, o *domain.WholesaleOrder) error
}

type PlatformSessionRepository interface {
	Create(ctx context.Context, s *domain.PlatformSession) error
	FindByToken(ctx context.Context, token string) (*domain.PlatformSession, error)
	MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error
}
