package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Inputs
// =====================================================================

type CreateInvoicePlanInput struct {
	QuotationID uuid.UUID
	Notes       string
	CreatedBy   *uuid.UUID
}

type InvoicePlanItemInput struct {
	SeqNo         int     `json:"seq_no"`
	Label         string  `json:"label"`
	Amount        float64 `json:"amount"`
	DueOffsetDays int     `json:"due_offset_days"`
}

type ReplaceInvoicePlanItemsInput struct {
	PlanID uuid.UUID
	Items  []InvoicePlanItemInput
}

type ActivateInvoicePlanInput struct {
	PlanID uuid.UUID
}

type IssueTerminItemInput struct {
	ItemID   uuid.UUID
	IssuedBy *uuid.UUID
}

type CancelInvoicePlanInput struct {
	PlanID uuid.UUID
	Reason string
}

// =====================================================================
// UseCase additions
// =====================================================================

type InvoicePlanUseCase interface {
	GetInvoicePlanByQuotation(ctx context.Context, quotationID uuid.UUID) (*domain.InvoicePlan, []domain.InvoicePlanItem, error)
	GetInvoicePlan(ctx context.Context, id uuid.UUID) (*domain.InvoicePlan, []domain.InvoicePlanItem, error)
	CreateInvoicePlan(ctx context.Context, in CreateInvoicePlanInput) (*domain.InvoicePlan, error)
	ReplaceInvoicePlanItems(ctx context.Context, in ReplaceInvoicePlanItemsInput) ([]domain.InvoicePlanItem, error)
	ActivateInvoicePlan(ctx context.Context, in ActivateInvoicePlanInput) (*domain.InvoicePlan, error)
	IssueTerminItem(ctx context.Context, in IssueTerminItemInput) (*domain.Invoice, error)
	CancelInvoicePlan(ctx context.Context, in CancelInvoicePlanInput) (*domain.InvoicePlan, error)
}

// =====================================================================
// Repository
// =====================================================================

type InvoicePlanRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*domain.InvoicePlan, error)
	FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.InvoicePlan, error)
	Create(ctx context.Context, p *domain.InvoicePlan) error
	Update(ctx context.Context, p *domain.InvoicePlan) error

	ListItems(ctx context.Context, planID uuid.UUID) ([]domain.InvoicePlanItem, error)
	ReplaceItems(ctx context.Context, planID uuid.UUID, items []domain.InvoicePlanItem) error
	FindItemByID(ctx context.Context, id uuid.UUID) (*domain.InvoicePlanItem, error)
	UpdateItem(ctx context.Context, it *domain.InvoicePlanItem) error
}
