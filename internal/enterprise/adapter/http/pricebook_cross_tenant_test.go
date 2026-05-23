package http

import (
	"testing"
)

// =====================================================================
// Wave 108 — Edge #13: cross-customer pricebook leak
//
// A sales rep whose `claims.allowed_companies` only includes tenant A,
// trying to read a published pricebook owned by tenant B, should get
// HTTP 404 (consistent with the reseller-cross-tenant pattern in
// internal/reseller/adapter/http/cross_tenant_test.go where the
// presence of the row stays invisible). Today the pricebook handler
// has no `holding_company_id`-scope predicate at all (see
// internal/enterprise/usecase/service.go::GetPricebook — straight pass-
// through to repo.FindByID with no tenant filter), so the load-bearing
// invariant doesn't exist yet.
//
// Wave 95 (the RBAC company-scope wave) is the implementing wave per
// the Wave 91 audit roadmap. Until that lands the test stays t.Skip-ed
// to document the contract:
//
//	GET /pricebooks/{id-belongs-to-tenant-B}
//	    requested by tenant-A user
//	→ HTTP 404
//	→ body { code: "pricebook.not_found" }
//	  (NOT 403; 403 would leak the existence of the row, same threat
//	   model as the reseller subscriber-not-found path)
//
// When the company-scope predicate lands the implementer should drop
// the t.Skip and exercise:
//  1. Create a published pricebook with `holding_company_id = tenant-B`.
//  2. Issue GET /pricebooks/{id} with a JWT whose
//     `allowed_companies = [tenant-A]`.
//  3. Assert status 404 + code "pricebook.not_found".
//  4. Issue GET with `allowed_companies = [tenant-A, tenant-B]` →
//     expect 200 + the row.
// =====================================================================

func TestPricebookCrossTenant_NonOwnerSees404(t *testing.T) {
	t.Skip("pricebook company-scope predicate not yet implemented; tracked in wave-108 compliance report §3e")
}

// TestPricebookCrossTenant_PinPricebookFromAnotherTenant_Rejected —
// the symmetric write-side test: pinning a pricebook the sales rep
// can't read should also be rejected with 404 (same invisibility
// invariant). Skipped for the same reason.
func TestPricebookCrossTenant_PinPricebookFromAnotherTenant_Rejected(t *testing.T) {
	t.Skip("pricebook company-scope predicate not yet implemented; tracked in wave-108 compliance report §3e")
}
