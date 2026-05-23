package http

import (
	"github.com/ion-core/backend/pkg/auth"
)

// =====================================================================
// Wave 97 — multi-role field-mask configuration
// =====================================================================
//
// `field_mask.go` ships the vendor-only mask + the generic
// `stripMaskedFields` walker. This file extends that infrastructure to
// every role tier in the audit (Wave 95 in the audit doc):
//
//   - Reseller  — sister subsidiary acting as a B2B2C distributor; sees
//     published unit_price + their own margin but not the holding's
//     internal cost basis or vendor benchmarks.
//   - Sister    — peer sister subsidiary executing operational work for
//     someone else's commercial customer; sees the BOQ lines + schedule
//     but not customer PII or PO documents.
//   - SalesMobile — mobile sales app; minimal commercial-cost view, kept
//     even tighter than reseller because the device may be shared.
//   - Technician — on-site field tech; only WO/EWO schedule, site
//     address, one contact phone, and the checklist. NO pricing, NO
//     commercial PII, NO approval chains.
//
// Mask sets are `map[string]struct{}` keyed for O(1) lookup. We keep
// each mask deliberately NARROW for the same reason the vendor mask is
// narrow — a generic key like `notes` or `description` would corrupt
// unrelated payloads. The classifier picks the strongest mask the
// actor matches; full-access actors (super_admin, finance_admin with no
// reseller affiliation, ...) bypass the middleware entirely.

// ResellerMaskedFields — fields stripped from any response served to a
// reseller subsidiary actor when the data is outside their reseller
// scope. Resellers see published unit_price + their own commission line
// but not the holding company's internal cost basis or vendor performance
// benchmarks.
//
// TC-RBAC-IV-009..014 (reseller scope).
var ResellerMaskedFields = []string{
	// Pricebook / BOQ — strip our internal cost basis. The published
	// `unit_price` (what the reseller can quote to the end-customer)
	// stays visible; the unit_cost / vendor_unit_cost is hidden so the
	// reseller can't reverse-engineer the holding's margin.
	"unit_cost",
	"vendor_unit_cost",
	"cost_total",
	"margin_pct",
	"snapshot_hash",
	// BOQ line internal columns — same reasoning as vendor mask.
	"base_price_snapshot",
	"min_margin_snapshot",
	"max_discount_snapshot",
	"line_discount_pct",

	// Intercompany PO + internal-transaction ledger — these are
	// holding-internal transfers between sister subsidiaries. Resellers
	// MUST NOT see the recognized_amount field; they see only the
	// `commission_amount` that's their own due.
	"recognized_amount",
	"internal_margin",
	"internal_margin_pct",

	// Vendor benchmark data — vendor performance ratings live on the
	// internal scorecard, not visible to a B2B2C distributor.
	"vendor_rating",
	"vendor_score",
	"vendor_sla_breach_count",
	"vendor_performance",

	// Negotiation internal commercial signals (same as vendor mask).
	"margin_floor_pct",
	"discount_ceiling_pct",
	"margin_before",
	"margin_after",
	"max_discount_after",
	"price_changes",
	"cco_auto_injected",
	"cco_injection_reason",
}

// SisterMaskedFields — fields stripped from responses served to a sister
// subsidiary actor when that sister is NOT the commercial owner of the
// record. Sisters see the operational data (BOQ lines they execute,
// schedule, site address) but not customer-facing PII or the customer
// PO document.
//
// The classifier still applies this even for commercial-owner sisters
// because the middleware can't tell at the response-rewrite layer which
// rows are "theirs" vs. peer — the company-scope WHERE predicate in
// the repo layer handles that. The mask here is the defence-in-depth
// for cross-sister responses (e.g. shared dashboards, list endpoints
// that fan out by holding).
//
// TC-RBAC-IV-015..018 (sister scope).
var SisterMaskedFields = []string{
	// Customer-facing PII — sisters performing peer ops should never
	// see the end-customer's contact details. These fields appear on
	// opportunity, customer_po, and quotation payloads.
	"customer_email",
	"customer_phone",
	"customer_contact_name",
	"contact_name",
	"contact_email",
	"contact_phone",
	"billing_address",
	"billing_email",

	// Customer PO document — file_url leaks the uploaded PDF directly.
	// We strip the URL but keep `file_name` + `po_number` so the sister
	// can see that a PO exists. (`file_size_bytes` is informational.)
	"file_url",

	// Negotiation internal signals — same reasoning as vendor mask.
	"margin_floor_pct",
	"discount_ceiling_pct",
	"margin_before",
	"margin_after",
	"max_discount_after",
	"price_changes",

	// Approval chain comments — internal commentary may name customers
	// or expose negotiation tactics. We strip approver comments but
	// keep `step_status` and `decided_at` so the sister can see the
	// approval timeline.
	"approver_comment",
	"reject_reason",
	"approval_notes",
}

// SalesMobileMaskedFields — minimal mask for the mobile sales app
// (sales_app on Flutter). The mobile device may be shared, so we mask
// the same internal-cost fields as reseller plus internal approval
// commentary + vendor benchmark data. Sales mobile DOES see published
// unit_price and their own commission.
//
// TC-RBAC-IV-019..022 (sales mobile scope).
var SalesMobileMaskedFields = []string{
	// Internal cost basis — same as reseller; mobile devices shouldn't
	// cache holding-internal margin.
	"unit_cost",
	"vendor_unit_cost",
	"cost_total",
	"margin_pct",
	"base_price_snapshot",
	"min_margin_snapshot",
	"max_discount_snapshot",
	"line_discount_pct",

	// Internal approval commentary — same reasoning as sister mask.
	"approver_comment",
	"reject_reason",
	"approval_notes",

	// Vendor benchmark data — only finance + ops sees the vendor
	// scorecard; a sales rep would only weaponize it against vendors.
	"vendor_rating",
	"vendor_score",
	"vendor_sla_breach_count",
	"vendor_performance",

	// Negotiation internal signals.
	"margin_floor_pct",
	"discount_ceiling_pct",
	"margin_before",
	"margin_after",
	"max_discount_after",
	"price_changes",
	"cco_auto_injected",
	"cco_injection_reason",

	// Intercompany ledger — sales mobile is read-only on this anyway,
	// but if a fan-out list returns it we strip the holding-internal
	// amount.
	"recognized_amount",
	"internal_margin",
	"internal_margin_pct",
}

// TechnicianMaskedFields — heaviest mask. Field technicians using the
// tech_app on-site need only: WO/EWO schedule, site address, ONE
// contact phone, and the checklist. NO pricing, NO commercial PII
// beyond the site contact, NO approval chains. This is also the mask
// applied when the actor's only role is `technician`.
//
// TC-RBAC-IV-023..028 (technician scope).
var TechnicianMaskedFields = []string{
	// Everything in the vendor + reseller + sister masks is fair game
	// for the technician strip — they need almost nothing commercial.
	"sell_total",
	"cost_total",
	"unit_cost",
	"vendor_unit_cost",
	"unit_price",
	"sell_unit_price",
	"margin_pct",
	"snapshot_hash",
	"base_price_snapshot",
	"min_margin_snapshot",
	"max_discount_snapshot",
	"line_discount_pct",
	"total_amount",
	"paid_amount",
	"balance",
	"recognized_amount",
	"internal_margin",
	"internal_margin_pct",

	// Approval chain — no need on-site.
	"approver_comment",
	"reject_reason",
	"approval_notes",
	"approval_chain",
	"approval_instances",
	"approval_steps",

	// Customer-facing PII beyond the site contact. Technicians may see
	// `site_contact_phone` (we don't strip that) but not the
	// commercial billing contact.
	"customer_email",
	"customer_phone",
	"customer_contact_name",
	"contact_name",
	"contact_email",
	"contact_phone",
	"billing_address",
	"billing_email",

	// Negotiation internals — never relevant to a tech.
	"margin_floor_pct",
	"discount_ceiling_pct",
	"margin_before",
	"margin_after",
	"max_discount_after",
	"price_changes",
	"cco_auto_injected",
	"cco_injection_reason",
	"vendor_rating",
	"vendor_score",
	"vendor_sla_breach_count",
	"vendor_performance",

	// PO file URLs.
	"file_url",
}

// =====================================================================
// Actor classification
// =====================================================================

// RoleClassification is the bucket the JWT claims fall into once the
// classifier has weighed each role. The contract test sweep enumerates
// every classification × every route and asserts the right mask runs.
type RoleClassification int

const (
	// RoleFullAccess — no mask applied. The actor has a privileged
	// role (super_admin, finance_admin, holding_director, ...) that
	// outranks any masked role they may also carry.
	RoleFullAccess RoleClassification = iota
	// RoleVendor — internal-vendor user; the original Wave 31 mask.
	RoleVendor
	// RoleReseller — actor's sister subsidiary is a reseller / B2B2C
	// distributor.
	RoleReseller
	// RoleSister — actor's sister subsidiary is performing ops on
	// another sister's customer.
	RoleSister
	// RoleSalesMobile — actor logged in via the mobile sales app.
	RoleSalesMobile
	// RoleTechnician — field technician.
	RoleTechnician
)

// String returns the snake_case classification name. Used by the
// contract test for readable failure messages.
func (rc RoleClassification) String() string {
	switch rc {
	case RoleFullAccess:
		return "full_access"
	case RoleVendor:
		return "vendor"
	case RoleReseller:
		return "reseller"
	case RoleSister:
		return "sister"
	case RoleSalesMobile:
		return "sales_mobile"
	case RoleTechnician:
		return "technician"
	default:
		return "unknown"
	}
}

// fullAccessRoles — actors with any of these roles bypass field
// masking entirely. They're the holding-level audit / finance / IT
// admin roles that need the full picture by definition. Lower-tier
// roles in the same claims set don't downgrade them — a user who is
// BOTH `super_admin` AND `reseller_admin` (e.g. seconded executives)
// is treated as full-access. This matches the audit's "lowest privilege
// wins for masking — except when an explicitly-trusted role is
// present" rule.
var fullAccessRoles = map[string]struct{}{
	"super_admin":       {},
	"holding_admin":     {},
	"holding_director":  {},
	"finance_admin":     {},
	"compliance_admin":  {},
	"audit_admin":       {},
	"it_admin":          {},
}

// vendorRoles — see existing IsVendorActor logic; centralised here so
// the classifier and the legacy entry point agree.
var vendorRoles = map[string]struct{}{
	"vendor_user":     {},
	"internal_vendor": {},
}

var resellerRoles = map[string]struct{}{
	"reseller_admin":    {},
	"reseller_sales":    {},
	"reseller_finance":  {},
	"reseller_operator": {},
}

var sisterRoles = map[string]struct{}{
	"sister_ops":        {},
	"sister_pm":         {},
	"sister_technician": {},
}

var salesMobileRoles = map[string]struct{}{
	"sales_mobile":   {},
	"sales_field":    {},
	"sales_consumer": {},
}

var technicianRoles = map[string]struct{}{
	"technician":        {},
	"technician_senior": {},
	"technician_junior": {},
	"field_tech":        {},
}

// ClassifyActor inspects the claim's role list and returns the
// classification with the strongest mask the actor still qualifies for.
//
// Ranking (highest precedence wins):
//  1. If the actor carries ANY full-access role -> RoleFullAccess.
//  2. Else, if the actor carries a vendor role -> RoleVendor.
//     Vendor wins over reseller/sister because the vendor mask is
//     narrowest and the role is the most operationally restricted —
//     an internal_vendor seconded to a sister is still acting as a
//     vendor at the request boundary.
//  3. Else, if any reseller role -> RoleReseller.
//  4. Else, if any sister role -> RoleSister.
//  5. Else, if technician role -> RoleTechnician.
//  6. Else, if sales-mobile role -> RoleSalesMobile.
//  7. Default -> RoleFullAccess. We deliberately default to FullAccess
//     for unknown role tags so the introduction of a new role doesn't
//     silently apply the heaviest mask to a previously-working endpoint.
//     The audit + contract test sweep is the safety net for
//     unintentional "we forgot to add the mask" regressions.
//
// Technician comes BEFORE sales_mobile because a technician who also
// happens to carry sales_mobile (e.g. dual-role sub-contractor) needs
// the heavier technician strip.
//
// Nil claims return RoleFullAccess so the middleware passes through
// when invoked outside the auth chain (test helpers, health checks).
func ClassifyActor(claims *auth.Claims) RoleClassification {
	if claims == nil {
		return RoleFullAccess
	}
	for _, r := range claims.Roles {
		if _, ok := fullAccessRoles[r]; ok {
			return RoleFullAccess
		}
	}
	for _, r := range claims.Roles {
		if _, ok := vendorRoles[r]; ok {
			return RoleVendor
		}
	}
	for _, r := range claims.Roles {
		if _, ok := resellerRoles[r]; ok {
			return RoleReseller
		}
	}
	for _, r := range claims.Roles {
		if _, ok := sisterRoles[r]; ok {
			return RoleSister
		}
	}
	for _, r := range claims.Roles {
		if _, ok := technicianRoles[r]; ok {
			return RoleTechnician
		}
	}
	for _, r := range claims.Roles {
		if _, ok := salesMobileRoles[r]; ok {
			return RoleSalesMobile
		}
	}
	return RoleFullAccess
}

// MaskFieldsFor returns the field list that should be stripped from any
// response served to the given classification. Used by the contract
// test sweep and by the role-aware middleware below.
//
// FullAccess returns nil — the middleware short-circuits on that.
func MaskFieldsFor(rc RoleClassification) []string {
	switch rc {
	case RoleVendor:
		return VendorMaskedBOQFields
	case RoleReseller:
		return ResellerMaskedFields
	case RoleSister:
		return SisterMaskedFields
	case RoleSalesMobile:
		return SalesMobileMaskedFields
	case RoleTechnician:
		return TechnicianMaskedFields
	default:
		return nil
	}
}
