package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Inputs
// =====================================================================

type SetNegotiationConfigInput struct {
	BOQVersionID             uuid.UUID
	Enabled                  bool
	Type                     string // 'standard' | 'custom'
	Mode                     string // 'sequential' | 'parallel'
	PricingAdjustmentAllowed bool
	MarginFloorPct           float64
	DiscountCeilingPct       float64
	Participants             []domain.NegotiationParticipant
}

type ActivateNegotiationInput struct {
	BOQVersionID uuid.UUID
	ActorUserID  uuid.UUID
}

type LinePriceChangeInput struct {
	LineID            uuid.UUID
	NewSellUnitPrice  float64
	NewDiscountPct    float64
}

type SubmitNegotiationRoundInput struct {
	NegotiationID uuid.UUID
	ActorUserID   uuid.UUID
	Changes       []LinePriceChangeInput
}

type NegotiationRoundActionInput struct {
	ApprovalID  uuid.UUID
	ActorUserID uuid.UUID
	ReasonCode  string // reject-only
	Comment     string // reject-only
}

type AbortNegotiationInput struct {
	NegotiationID uuid.UUID
	Reason        string
}

// =====================================================================
// UseCase
// =====================================================================

type NegotiationUseCase interface {
	// Config — mutable while BOQ is draft; locked on approval.
	SetNegotiationConfig(ctx context.Context, in SetNegotiationConfigInput) (*domain.NegotiationConfig, error)
	GetNegotiationConfig(ctx context.Context, boqVersionID uuid.UUID) (*domain.NegotiationConfig, error)

	// Negotiation lifecycle.
	GetNegotiation(ctx context.Context, id uuid.UUID) (*domain.Negotiation, []domain.NegotiationRound, error)
	GetNegotiationByBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Negotiation, error)
	ActivateNegotiation(ctx context.Context, in ActivateNegotiationInput) (*domain.Negotiation, error)
	AbortNegotiation(ctx context.Context, in AbortNegotiationInput) (*domain.Negotiation, error)

	// Round flow — VP submits, chain approves, completion fires re-quote.
	SubmitRound(ctx context.Context, in SubmitNegotiationRoundInput) (*domain.NegotiationRound, []domain.NegotiationRoundApproval, error)
	GetRound(ctx context.Context, id uuid.UUID) (*domain.NegotiationRound, []domain.NegotiationRoundApproval, error)
	ApproveRoundStep(ctx context.Context, in NegotiationRoundActionInput) (*domain.NegotiationRoundApproval, *domain.NegotiationRound, error)
	RejectRoundStep(ctx context.Context, in NegotiationRoundActionInput) (*domain.NegotiationRoundApproval, *domain.NegotiationRound, error)

	// Hooks called from the BOQ approval/revision paths.
	LockConfigOnApproval(ctx context.Context, boqVersionID uuid.UUID) error
	SupersedeOnBOQRevision(ctx context.Context, boqVersionID uuid.UUID) error

	// Inbox: pending round approvals for the given user. Mirrors the
	// ListApprovalInstances(pending_for_me=true) surface used by the
	// BOQ chain.
	ListPendingRoundApprovalsForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.NegotiationRoundApproval, error)
}

// =====================================================================
// Repositories
// =====================================================================

type NegotiationConfigRepository interface {
	GetConfig(ctx context.Context, boqVersionID uuid.UUID) (*domain.NegotiationConfig, error)
	SetConfig(ctx context.Context, cfg *domain.NegotiationConfig) error
	LockConfig(ctx context.Context, boqVersionID uuid.UUID) error
	ListParticipants(ctx context.Context, boqVersionID uuid.UUID) ([]domain.NegotiationParticipant, error)
	ReplaceParticipants(ctx context.Context, boqVersionID uuid.UUID, members []domain.NegotiationParticipant) error
}

type NegotiationRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Negotiation, error)
	FindByBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Negotiation, error)
	Create(ctx context.Context, n *domain.Negotiation) error
	Update(ctx context.Context, n *domain.Negotiation) error
}

type NegotiationRoundRepository interface {
	List(ctx context.Context, negotiationID uuid.UUID) ([]domain.NegotiationRound, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.NegotiationRound, error)
	HighestRoundNo(ctx context.Context, negotiationID uuid.UUID) (int, error)
	Create(ctx context.Context, r *domain.NegotiationRound) error
	Update(ctx context.Context, r *domain.NegotiationRound) error
}

type NegotiationRoundApprovalRepository interface {
	ListByRound(ctx context.Context, roundID uuid.UUID) ([]domain.NegotiationRoundApproval, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.NegotiationRoundApproval, error)
	CreateBatch(ctx context.Context, approvals []domain.NegotiationRoundApproval) error
	Update(ctx context.Context, a *domain.NegotiationRoundApproval) error
	// ListPendingForUser returns negotiation round approvals where the
	// approver matches and the status is pending. Used by the unified
	// approvals inbox so a single user query covers both BOQ steps and
	// negotiation round steps.
	ListPendingForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.NegotiationRoundApproval, error)
}
