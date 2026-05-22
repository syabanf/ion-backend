package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
)

// =====================================================================
// WO dispatch — driving inputs
// =====================================================================

type CreateWODispatchItemInput struct {
	ItemID uuid.UUID
	Qty    float64
	Notes  string
}

type CreateWODispatchInput struct {
	WOID         uuid.UUID
	WarehouseID  uuid.UUID
	DispatchedBy uuid.UUID
	Notes        string
	Items        []CreateWODispatchItemInput
}

// WODispatchListFilter scopes a list query. All fields optional; an
// empty filter returns everything sorted by planned_at DESC.
type WODispatchListFilter struct {
	WOID        *uuid.UUID
	WarehouseID *uuid.UUID
	Status      string
	Limit       int
	Offset      int
}

// =====================================================================
// WO dispatch — usecase contract
// =====================================================================

// WODispatchUseCase is a side-port a Service implements. Kept separate
// from the main UseCase interface so wiring is opt-in (same approach
// suppliers + r2 took — a new round of features doesn't force a rebuild
// of every existing test double).
type WODispatchUseCase interface {
	CreateDispatch(ctx context.Context, in CreateWODispatchInput) (*domain.WODispatch, error)
	StageDispatch(ctx context.Context, id, by uuid.UUID) (*domain.WODispatch, error)
	CancelDispatch(ctx context.Context, id, by uuid.UUID, reason string) (*domain.WODispatch, error)
	MarkPickedUp(ctx context.Context, id, by uuid.UUID) (*domain.WODispatch, error)
	PickUpItemByScan(ctx context.Context, itemID uuid.UUID, serialOrQR string, pickedBy uuid.UUID) (*domain.WODispatchItem, error)
	ReturnItem(ctx context.Context, itemID uuid.UUID, qty float64, notes string, returnedBy uuid.UUID) (*domain.WODispatchItem, error)
	ListDispatches(ctx context.Context, f WODispatchListFilter) ([]domain.WODispatch, int, error)
	GetDispatch(ctx context.Context, id uuid.UUID) (*domain.WODispatch, error)
}

// =====================================================================
// WO dispatch — repository
// =====================================================================

type WODispatchRepository interface {
	Create(ctx context.Context, d *domain.WODispatch) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.WODispatch, error)
	List(ctx context.Context, f WODispatchListFilter) ([]domain.WODispatch, int, error)
	UpdateStatus(ctx context.Context, d *domain.WODispatch) error
	FindItemByID(ctx context.Context, itemID uuid.UUID) (*domain.WODispatchItem, error)
	UpdateItem(ctx context.Context, it *domain.WODispatchItem) error
}
