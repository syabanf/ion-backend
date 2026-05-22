package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Quotation inputs
// =====================================================================

type GenerateQuotationInput struct {
	BOQVersionID uuid.UUID
	// Notes — operator-provided context that lands on the PDF's Notes
	// section + on the row.
	Notes      string
	// Validity window override; if zero, the usecase falls back to the
	// domain default (30 days).
	ValidityDays int
	IssuedBy   *uuid.UUID
}

type QuotationListFilter struct {
	BOQVersionID  *uuid.UUID
	OpportunityID *uuid.UUID
	Status        string
	Limit         int
	Offset        int
}

type AcceptQuotationInput struct {
	ID         uuid.UUID
	IfRevision *int
}

type RejectQuotationInput struct {
	ID         uuid.UUID
	Reason     string
	IfRevision *int
}

type CancelQuotationInput struct {
	ID         uuid.UUID
	Reason     string
	IfRevision *int
}

// QuotationUseCase is the surface exposed to the HTTP layer for the
// Phase-4a quotation flows. Implemented by the existing Service
// alongside the BOQ + opportunity surfaces.
type QuotationUseCase interface {
	GenerateQuotation(ctx context.Context, in GenerateQuotationInput) (*domain.Quotation, error)
	ListQuotations(ctx context.Context, f QuotationListFilter) ([]domain.Quotation, int, error)
	GetQuotation(ctx context.Context, id uuid.UUID) (*domain.Quotation, error)
	// PDFOnly returns ONLY the bytes — the HTTP handler streams them.
	// Keeping this separate from GetQuotation avoids paying the BYTEA
	// transfer on the JSON path where the FE only needs metadata.
	GetQuotationPDF(ctx context.Context, id uuid.UUID) ([]byte, string, error)
	AcceptQuotation(ctx context.Context, in AcceptQuotationInput) (*domain.Quotation, error)
	RejectQuotation(ctx context.Context, in RejectQuotationInput) (*domain.Quotation, error)
	CancelQuotation(ctx context.Context, in CancelQuotationInput) (*domain.Quotation, error)
}

// =====================================================================
// Repository
// =====================================================================

type QuotationRepository interface {
	List(ctx context.Context, f QuotationListFilter) ([]domain.Quotation, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Quotation, error)
	// FindPDFBytes is the lean-path read — returns only the BYTEA +
	// hash so the HTTP layer streams without rehydrating the rest of
	// the row.
	FindPDFBytes(ctx context.Context, id uuid.UUID) ([]byte, string, error)
	// FindHighestVersion returns the highest version_no row for a
	// quotation_number — used to compute v(N+1) on re-quote.
	FindHighestVersion(ctx context.Context, quotationNumber string) (*domain.Quotation, error)
	// FindLatestForBOQ returns the most recent (highest version_no)
	// quotation tied to a given BOQ version. Used by GenerateQuotation
	// to decide whether to issue v1 or v(N+1).
	FindLatestForBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Quotation, error)
	Create(ctx context.Context, q *domain.Quotation) error
	// Update writes the row's mutable columns (status + timestamps).
	// PDF bytes + hash are immutable post-create — re-renders create
	// a new version, not an update in place.
	Update(ctx context.Context, q *domain.Quotation, ifRevision *int) error
}
