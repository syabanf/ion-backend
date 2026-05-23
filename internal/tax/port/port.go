// Package port defines the driving (UseCase) and driven (Repository +
// gateway) contracts for the tax bounded context.
//
// Same hexagonal pattern as identity / enterprise / warehouse: HTTP
// handlers depend on UseCase; UseCase depends on repository +
// gateway interfaces; postgres adapters + the DJP gateway adapter
// implement the interfaces. This isolates the domain from both
// transport and storage so the bounded context can be extracted into
// its own service (cmd/tax-svc) without touching the domain.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/tax/domain"
)

// =====================================================================
// Repository inputs / filters
// =====================================================================

// CompanyTaxProfileFilter is the read-list filter for profile lookups.
// All fields are optional; empty filter returns the full list.
type CompanyTaxProfileFilter struct {
	SubsidiaryID *uuid.UUID
	Limit        int
	Offset       int
}

// CreateCompanyTaxProfileInput is the HTTP-friendly create payload.
// Dates are ISO strings (YYYY-MM-DD) so the HTTP layer can pass them
// through without time-parsing concerns; the usecase parses + validates.
type CreateCompanyTaxProfileInput struct {
	SubsidiaryID  uuid.UUID
	Name          string
	NPWP          string
	IsPKP         bool
	PPNRate       float64
	PPh23Rate     float64
	PPhFinalRate  float64
	EffectiveFrom string // YYYY-MM-DD
	EffectiveTo   string // YYYY-MM-DD or empty
}

// UpdateCompanyTaxProfileInput is the partial-update payload. nil
// fields are left untouched.
type UpdateCompanyTaxProfileInput struct {
	ID            uuid.UUID
	Name          *string
	NPWP          *string
	IsPKP         *bool
	PPNRate       *float64
	PPh23Rate     *float64
	PPhFinalRate  *float64
	EffectiveFrom *string
	EffectiveTo   *string
}

// IssueFakturInput is the HTTP-friendly create payload for a Draft
// faktur. The usecase calls domain.NewDraftFaktur and persists.
type IssueFakturInput struct {
	InvoiceID    uuid.UUID
	SubsidiaryID uuid.UUID
	JenisFaktur  string // optional — defaults to "01"
	NPWPLawan    string
	DPP          float64
	PPN          float64
}

// =====================================================================
// Driven ports — repositories + gateways
// =====================================================================

// CompanyTaxProfileRepository persists CompanyTaxProfile aggregates.
type CompanyTaxProfileRepository interface {
	Create(ctx context.Context, p *domain.CompanyTaxProfile) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.CompanyTaxProfile, error)
	// FindActiveBySubsidiary returns the highest-effective_from row
	// whose window includes `at`. Returns NotFound when no row exists
	// for the subsidiary at that timestamp.
	FindActiveBySubsidiary(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*domain.CompanyTaxProfile, error)
	Update(ctx context.Context, p *domain.CompanyTaxProfile) error
	List(ctx context.Context, f CompanyTaxProfileFilter) ([]domain.CompanyTaxProfile, int, error)
}

// FakturPajakRepository persists FakturPajak aggregates.
type FakturPajakRepository interface {
	Create(ctx context.Context, f *domain.FakturPajak) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FakturPajak, error)
	// UpdateStatus persists a status change (+ optional nomor_seri /
	// djp payload). The usecase loads, mutates via TransitionTo, then
	// calls this to flush.
	UpdateStatus(ctx context.Context, f *domain.FakturPajak) error
	FindByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.FakturPajak, error)
}

// DJPGateway is the abstract integration with the Indonesian DJP
// e-Faktur API. The real adapter will sign + post to the DJP HTTPS
// endpoint; the stub returns NotImplemented-equivalent errors so the
// usecase wiring is exercised end-to-end without a live integration.
type DJPGateway interface {
	// IssueFaktur sends a Draft faktur to DJP for issuance. On
	// success the DJP returns the canonical nomor_seri and the raw
	// response payload; the usecase persists both via
	// FakturPajak.MarkSubmitted.
	IssueFaktur(ctx context.Context, f *domain.FakturPajak) (nomorSeri string, payload []byte, err error)
	// CheckStatus polls DJP for the current status of a previously-
	// issued faktur. Returns the DJP status string (matched to
	// domain.FakturStatus by the usecase) plus the raw payload.
	CheckStatus(ctx context.Context, nomorSeri string) (status string, payload []byte, err error)
}

// =====================================================================
// Driving port — UseCase
// =====================================================================

// UseCase is what the HTTP layer depends on.
type UseCase interface {
	// Profile reads + writes.
	GetActiveProfile(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*domain.CompanyTaxProfile, error)
	GetProfile(ctx context.Context, id uuid.UUID) (*domain.CompanyTaxProfile, error)
	ListProfiles(ctx context.Context, f CompanyTaxProfileFilter) ([]domain.CompanyTaxProfile, int, error)
	CreateProfile(ctx context.Context, in CreateCompanyTaxProfileInput) (*domain.CompanyTaxProfile, error)

	// Faktur lifecycle.
	IssueFakturForInvoice(ctx context.Context, in IssueFakturInput) (*domain.FakturPajak, error)
	SubmitFaktur(ctx context.Context, fakturID uuid.UUID) (*domain.FakturPajak, error)
	GetFaktur(ctx context.Context, id uuid.UUID) (*domain.FakturPajak, error)
}
