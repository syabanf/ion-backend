package domain

import (
	"time"

	"github.com/google/uuid"
)

// SubsidiaryRole enumerates the posture a subsidiary takes within a
// holding. The role drives downstream defaulting — e.g. a `reseller`
// subsidiary is the legal entity that signs customer contracts, a
// `supplier` is the procurement-side entity that issues POs, and
// `holding_ops` represents shared services (HR, finance, IT) that
// don't directly transact with customers or vendors.
//
// Wave 92 introduces the type without yet enforcing role-based
// business rules — those land alongside the FK rollout to existing
// enterprise tables.
type SubsidiaryRole string

const (
	SubsidiaryRoleReseller   SubsidiaryRole = "reseller"
	SubsidiaryRoleSupplier   SubsidiaryRole = "supplier"
	SubsidiaryRoleHoldingOps SubsidiaryRole = "holding_ops"
)

// Subsidiary is a legal/operational arm of a `HoldingCompany`. Each
// subsidiary carries its own NPWP. PPN / PKP posture lives on
// `tax.company_tax_profiles` (one row per effective window) so invoice
// generation can resolve the correct tax stance for a given invoice
// date — a fact that a static boolean on this row cannot encode.
//
// To answer "is this subsidiary PKP right now," call
// `tax.usecase.Service.GetActiveProfile(ctx, subsidiaryID, time.Now())`.
//
// NPWP is optional at create time so a freshly-onboarded subsidiary
// can be set up before the tax number is issued.
type Subsidiary struct {
	ID               uuid.UUID
	HoldingCompanyID uuid.UUID
	Name             string
	NPWP             *string
	Role             SubsidiaryRole
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// FakturProfile is the subset of the tax bounded context's
// CompanyTaxProfile that this domain needs in order to decide whether
// to issue a Faktur Pajak. Duplicated locally (rather than imported
// from internal/tax/domain) so the enterprise context stays
// loose-coupled — see internal/enterprise/port/tax.go for the
// resolver port and internal/enterprise/adapter/tax/ for the seam.
//
// The adapter maps tax.CompanyTaxProfile -> this struct in one place;
// every other call site is purely intra-enterprise.
type FakturProfile interface {
	GetIsPKP() bool
}

// RequiresFaktur reports whether this subsidiary's tax stance requires
// issuing a Faktur Pajak. Non-PKP subsidiaries do not collect PPN so
// they cannot issue a faktur — TC-FN-T3..T7 (Faktur waiver) per the
// Wave 91 audit. Caller (invoice generation) is expected to log a
// `faktur.waived` audit row with reason="non_pkp_subsidiary" on the
// false branch.
//
// nil-safe — when no profile is supplied (e.g., the resolver returned
// NotFound because no profile covers the timestamp), we conservatively
// return false so the path WAIVES rather than panics. The reconciliation
// cron will surface unbacked invoices for operator review.
func (Subsidiary) RequiresFaktur(profile FakturProfile) bool {
	if profile == nil {
		return false
	}
	return profile.GetIsPKP()
}
