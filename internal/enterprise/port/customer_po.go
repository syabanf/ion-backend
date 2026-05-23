package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Customer PO inputs — Wave 95
// =====================================================================

type UploadCustomerPOInput struct {
	OpportunityID                uuid.UUID
	BOQVersionID                 uuid.UUID
	CustomerID                   *uuid.UUID
	CommercialOwnerSubsidiaryID  uuid.UUID
	PONumber                     string
	POValue                      *float64
	FileURL                      string
	FileHash                     string
	UploadedBy                   *uuid.UUID
	Notes                        string
}

type CustomerPOListFilter struct {
	OpportunityID               *uuid.UUID
	BOQVersionID                *uuid.UUID
	CommercialOwnerSubsidiaryID *uuid.UUID
	Status                      string
	Limit                       int
	Offset                      int
}

// CustomerPORepository is the driven port for `enterprise.customer_pos`.
type CustomerPORepository interface {
	Create(ctx context.Context, po *domain.CustomerPO) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.CustomerPO, error)
	List(ctx context.Context, f CustomerPOListFilter) ([]domain.CustomerPO, int, error)
	UpdateStatus(ctx context.Context, po *domain.CustomerPO) error
}

// =====================================================================
// Intercompany PO inputs — Wave 95
// =====================================================================

type IntercompanyPOListFilter struct {
	CustomerPOID                *uuid.UUID
	CommercialOwnerSubsidiaryID *uuid.UUID
	ExecutingSubsidiaryID       *uuid.UUID
	BOQVersionID                *uuid.UUID
	Status                      string
	Limit                       int
	Offset                      int
}

// IntercompanyPORepository persists IC-PO headers + lines. `Create`
// writes the header + every line in a single transaction; the unique
// constraint on (ic_po_number) surfaces as a `.duplicate` conflict.
type IntercompanyPORepository interface {
	Create(ctx context.Context, header *domain.IntercompanyPO, lines []domain.IntercompanyPOLine) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.IntercompanyPO, error)
	List(ctx context.Context, f IntercompanyPOListFilter) ([]domain.IntercompanyPO, int, error)
	UpdateStatus(ctx context.Context, po *domain.IntercompanyPO) error
	FindLines(ctx context.Context, icPoID uuid.UUID) ([]domain.IntercompanyPOLine, error)
}

// IntercompanyPairRepository persists the auto-accept policy table.
type IntercompanyPairRepository interface {
	FindByPair(ctx context.Context, commercialOwner, executing uuid.UUID) (*domain.IntercompanyPair, error)
	List(ctx context.Context) ([]domain.IntercompanyPair, error)
	Upsert(ctx context.Context, pair *domain.IntercompanyPair) error
}
