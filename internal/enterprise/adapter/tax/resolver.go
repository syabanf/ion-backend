// Package tax is the enterprise-context adapter that bridges to the
// `internal/tax` bounded context.
//
// Wave 101 introduces the tax_snapshot chain. The enterprise context
// needs to resolve a subsidiary's active tax_profile at BOQ-approval
// time. Instead of every enterprise call site reaching into
// internal/tax/domain (which would defeat the bounded-context
// boundary), this package contains the single approved cross-context
// reference: a thin wrapper that calls tax.usecase.Service and maps
// the result into the enterprise-side DTO (port.TaxProfileSnapshot).
//
// To swap tax-svc for an out-of-process implementation later, replace
// the activeProfileFetcher dependency with an HTTP/gRPC client — the
// enterprise usecase only sees the port interface.
package tax

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/port"
	taxdomain "github.com/ion-core/backend/internal/tax/domain"
)

// activeProfileFetcher is the slice of tax.usecase.Service the
// resolver actually needs. Declared as an interface here (rather than
// taking *taxusecase.Service directly) so test code can fake it
// without spinning up the full tax usecase.
type activeProfileFetcher interface {
	GetActiveProfile(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*taxdomain.CompanyTaxProfile, error)
}

// Resolver implements port.TaxResolver by delegating to the tax
// usecase and mapping the domain aggregate into the enterprise-side
// snapshot DTO.
type Resolver struct {
	tax activeProfileFetcher
}

// NewResolver constructs the adapter. The argument is anything that
// satisfies the narrow GetActiveProfile contract — typically the
// tax.usecase.Service itself.
func NewResolver(tax activeProfileFetcher) *Resolver {
	return &Resolver{tax: tax}
}

// Compile-time check that the adapter satisfies the enterprise port.
var _ port.TaxResolver = (*Resolver)(nil)

// ActiveProfile fetches the active tax_profile for `subsidiaryID` at
// `at` (UTC) and maps it into port.TaxProfileSnapshot.
//
// Error semantics:
//   - Nil resolver / nil tax dep — returns nil, nil (caller treats
//     this as "no profile configured" and falls through to the
//     Non-PKP / waiver path).
//   - tax.GetActiveProfile errors propagate untouched; the usecase
//     converts NotFound into the same waiver path.
func (r *Resolver) ActiveProfile(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*port.TaxProfileSnapshot, error) {
	if r == nil || r.tax == nil {
		return nil, nil
	}
	if subsidiaryID == uuid.Nil {
		return nil, nil
	}
	p, err := r.tax.GetActiveProfile(ctx, subsidiaryID, at)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return &port.TaxProfileSnapshot{
		ProfileID:     p.ID,
		SubsidiaryID:  p.SubsidiaryID,
		IsPKP:         p.IsPKP,
		PPNRate:       p.PPNRate,
		PPh23Rate:     p.PPh23Rate,
		PPhFinalRate:  p.PPhFinalRate,
		EffectiveFrom: p.EffectiveFrom,
	}, nil
}
