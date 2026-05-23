// Package domain holds the tax bounded context's entities and value
// objects.
//
// Rules (same as enterprise / crm / warehouse domains):
//   - No imports of pkg/database, pkg/httpserver, or any other
//     framework — keeps the bounded context easy to extract into its
//     own service later.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map
//     them to the right HTTP status without inspecting strings.
//
// Wave 93 scope: PKP/Non-PKP tax profiles + Faktur Pajak scaffold for
// DJP e-Faktur. The DJP integration itself is a stub — the wave wires
// up the persistence + lifecycle so the integration can land in a
// later wave by swapping the adapter only.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// CompanyTaxProfile is the per-subsidiary, time-bounded set of rates
// + PKP status used to compute invoice tax lines.
//
// Profiles are versioned by replacement, not mutation: when rates
// change (e.g., 2025 PPN bump), insert a new row with the new
// effective_from. The "active" profile at any timestamp is the one
// with the highest effective_from <= t and (effective_to IS NULL OR
// effective_to >= t).
//
// Rate fields use float64 because the codebase doesn't pull in
// shopspring/decimal — Postgres holds NUMERIC(5,4) which round-trips
// safely through float64 at these magnitudes. If the codebase later
// adopts a decimal package, swap the types here and at the adapter
// boundary in one place.
type CompanyTaxProfile struct {
	ID            uuid.UUID
	SubsidiaryID  uuid.UUID
	Name          string
	NPWP          string
	IsPKP         bool
	PPNRate       float64
	PPh23Rate     float64
	PPhFinalRate  float64
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// maxReasonablePPNRate caps PPN at 30%. Indonesia's actual rate is
// 11% (2024) heading to 12% (2025) — anything over 30% is almost
// certainly a unit error (e.g., 11 instead of 0.11). We reject at
// the domain layer so downstream invoice math can trust the value.
const maxReasonablePPNRate = 0.30

// NewCompanyTaxProfile constructs a new profile and validates basic
// invariants. The caller picks `effectiveFrom`; `effectiveTo` is
// optional (nil for open-ended).
func NewCompanyTaxProfile(
	subsidiaryID uuid.UUID,
	name, npwp string,
	isPKP bool,
	ppnRate, pph23Rate, pphFinalRate float64,
	effectiveFrom time.Time,
	effectiveTo *time.Time,
) (*CompanyTaxProfile, error) {
	if subsidiaryID == uuid.Nil {
		return nil, errors.Validation(
			"tax_profile.subsidiary_required",
			"subsidiary_id is required",
		)
	}
	p := &CompanyTaxProfile{
		ID:            uuid.New(),
		SubsidiaryID:  subsidiaryID,
		Name:          strings.TrimSpace(name),
		NPWP:          strings.TrimSpace(npwp),
		IsPKP:         isPKP,
		PPNRate:       ppnRate,
		PPh23Rate:     pph23Rate,
		PPhFinalRate:  pphFinalRate,
		EffectiveFrom: effectiveFrom.UTC(),
		EffectiveTo:   effectiveTo,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate enforces rate sanity rules + window ordering.
//
// Rules:
//   - All three rate fields must be >= 0.
//   - PPN rate must be <= maxReasonablePPNRate (30%) — see constant
//     comment for why.
//   - effective_to (if set) must be strictly after effective_from.
func (p *CompanyTaxProfile) Validate() error {
	if p.PPNRate < 0 {
		return errors.Validation(
			"tax_profile.ppn_negative",
			"ppn_rate must be >= 0",
		)
	}
	if p.PPNRate > maxReasonablePPNRate {
		return errors.Validation(
			"tax_profile.ppn_too_high",
			"ppn_rate must be <= 0.30 (probable unit error — express as 0.11 not 11)",
		)
	}
	if p.PPh23Rate < 0 {
		return errors.Validation(
			"tax_profile.pph23_negative",
			"pph23_rate must be >= 0",
		)
	}
	if p.PPhFinalRate < 0 {
		return errors.Validation(
			"tax_profile.pph_final_negative",
			"pph_final_rate must be >= 0",
		)
	}
	if p.EffectiveTo != nil && !p.EffectiveTo.After(p.EffectiveFrom) {
		return errors.Validation(
			"tax_profile.window_invalid",
			"effective_to must be strictly after effective_from",
		)
	}
	return nil
}

// EffectivePPNRate returns the PPN rate to apply at time `at`. If
// `at` falls outside the profile's window the rate is reported as 0
// — the usecase layer is responsible for picking the right profile
// for the timestamp; this method is a defensive last line.
//
// Non-PKP companies do not collect PPN, so the effective rate is
// always 0 regardless of the configured rate.
func (p *CompanyTaxProfile) EffectivePPNRate(at time.Time) float64 {
	if !p.IsPKP {
		return 0
	}
	at = at.UTC()
	if at.Before(p.EffectiveFrom) {
		return 0
	}
	if p.EffectiveTo != nil && at.After(*p.EffectiveTo) {
		return 0
	}
	return p.PPNRate
}
