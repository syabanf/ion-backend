// Package port — tax resolver port.
//
// Wave 101 introduces the tax_snapshot chain (BOQ → Quotation →
// Invoice → Faktur Pajak). To compute the chain the enterprise
// usecase needs to know the active tax_profile at the moment of
// BOQ approval. Instead of cross-importing internal/tax/domain (which
// would couple two bounded contexts together at compile time), the
// enterprise context declares its own narrow interface here and an
// adapter at internal/enterprise/adapter/tax/ maps the tax domain
// type into this small DTO.
//
// That adapter is THE ONLY APPROVED CROSS-CONTEXT REFERENCE in the
// Wave 101 plan — everything else stays intra-enterprise.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TaxProfileSnapshot is the enterprise-side mirror of the
// tax.CompanyTaxProfile fields that BOQ approval needs. Kept
// deliberately small + non-pointer so the resolver can return a value
// without leaking the tax aggregate's invariants.
type TaxProfileSnapshot struct {
	ProfileID     uuid.UUID
	SubsidiaryID  uuid.UUID
	IsPKP         bool
	PPNRate       float64 // fractional, 0.00–0.30
	PPh23Rate     float64
	PPhFinalRate  float64
	EffectiveFrom time.Time
}

// GetIsPKP satisfies domain.FakturProfile so the resolver result can
// be passed directly into Subsidiary.RequiresFaktur.
func (s TaxProfileSnapshot) GetIsPKP() bool { return s.IsPKP }

// TaxResolver looks up the active tax profile for a subsidiary at a
// given point in time. Implementations live in
// internal/enterprise/adapter/tax/ — there's exactly one (a thin
// wrapper around tax.usecase.Service.GetActiveProfile) and the
// constructor returns it as the interface so callers don't take a
// hard dependency on the concrete type.
//
// Returns derrors.NotFound when no profile covers the timestamp. The
// usecase translates that into the Non-PKP fallback path (waive
// faktur, audit-log the reason).
type TaxResolver interface {
	ActiveProfile(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*TaxProfileSnapshot, error)
}
