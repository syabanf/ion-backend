package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/auth"
)

// =====================================================================
// Wave 97 — contract RBAC test sweep
// =====================================================================
//
// TC-RBAC-IV-031 (audit doc Wave 95) asks for a single test that
// enumerates every enterprise route × every role classification and
// asserts the field-mask path produces the right shape. Today we drive
// the test off `stripMaskedFields` directly with canned response
// payloads — the same code path the middleware runs in production. We
// don't yet spin up the full chi router because half the routes in the
// table (customer_po, IC-PO) ship in the parallel Wave 95 agent's PR;
// once they land, this test gets upgraded to fire HTTP requests at
// httptest.NewServer in a follow-up. The unit-level invariant covered
// here is the load-bearing one — if mask sets drop the wrong field
// the table catches it immediately.
//
// The classification ranking is also asserted in TestClassifyActor_*
// below so a future change to fullAccessRoles / vendorRoles / ... gets
// flagged.

type contractCase struct {
	name            string
	routePath       string
	actorRole       RoleClassification
	canned          map[string]any
	expectedFields  []string // MUST be present
	forbiddenFields []string // MUST be stripped
	skipReason      string   // non-empty => t.Skip
}

// cannedPricebookLine — a representative pricebook GET response.
// Fields chosen so each mask has something to act on.
func cannedPricebookLine() map[string]any {
	return map[string]any{
		"id":               "pl-1",
		"sku":              "SKU-100",
		"description":      "Patch cord 2m",
		"unit_price":       1000.0, // published — resellers SEE this
		"unit_cost":        700.0,  // internal — reseller/sales/tech HIDE
		"vendor_unit_cost": 650.0,  // vendor doesn't see their basis here
		"holding_company_id": "h-1",
	}
}

// cannedBOQ — representative BOQ GET response, includes header + one
// line so all six masks have something to strip.
func cannedBOQ() map[string]any {
	return map[string]any{
		"id":            "boq-1",
		"version_no":    1,
		"status":        "approved",
		"sell_total":    5_000_000.0,
		"cost_total":    3_500_000.0,
		"margin_pct":    30.0,
		"snapshot_hash": "deadbeef",
		"lines": []any{
			map[string]any{
				"id":                    "line-1",
				"sku":                   "SKU-1",
				"sell_unit_price":       1000.0,
				"unit_price":            1000.0,
				"unit_cost":             700.0,
				"vendor_unit_cost":      650.0,
				"line_discount_pct":     5.0,
				"base_price_snapshot":   1200.0,
				"min_margin_snapshot":   18.0,
				"max_discount_snapshot": 10.0,
			},
		},
	}
}

func cannedCustomerPO() map[string]any {
	return map[string]any{
		"id":                  "po-1",
		"po_number":           "PO-2026-001",
		"file_name":           "PO_signed.pdf",
		"file_url":            "https://files.ion/po-1.pdf",
		"file_size_bytes":     12345,
		"customer_contact_name": "Pak Budi",
		"customer_email":      "budi@customer.co.id",
		"customer_phone":      "+62-812-0000-0000",
		"opportunity_id":      "opp-1",
	}
}

func cannedOpportunity() map[string]any {
	return map[string]any{
		"id":              "opp-1",
		"name":            "Big Telco Q3",
		"stage":           "qualified",
		"contact_name":    "Pak Andi",
		"contact_email":   "andi@telco.co.id",
		"contact_phone":   "+62-811-0000-0000",
		"billing_email":   "billing@telco.co.id",
		"billing_address": "Jl. Sudirman 1",
		"customer_email":  "ar@telco.co.id",
	}
}

func cannedEWO() map[string]any {
	return map[string]any{
		"id":                "ewo-1",
		"ewo_number":        "EWO-001",
		"site_address":      "Jl. Gatot Subroto 88",
		"site_contact_phone": "+62-813-1111-2222",
		"scheduled_at":      "2026-06-01T09:00:00Z",
		"status":            "scheduled",
		"approval_chain": []any{
			map[string]any{"step": 1, "approver": "vp_sales", "approver_comment": "looks good"},
		},
		"recognized_amount": 5_000_000.0,
		"internal_margin":   500_000.0,
		"vendor_rating":     4.5,
	}
}

func cannedInvoice() map[string]any {
	return map[string]any{
		"id":             "inv-1",
		"invoice_number": "INV-001",
		"status":         "partial",
		"total_amount":   50_000_000.0,
		"paid_amount":    5_000_000.0,
		"balance":        45_000_000.0,
		"file_url":       "https://files.ion/inv-1.pdf",
	}
}

// TestContractRBAC_FieldMaskSweep enumerates 36 cases (6 routes ×
// 6 roles) and asserts the mask path strips / preserves the right
// fields. Routes that depend on Wave 95 handlers (customer_po, ic_po)
// are skip-marked rather than failing — they'll turn green as those
// handlers land.
func TestContractRBAC_FieldMaskSweep(t *testing.T) {
	cases := []contractCase{
		// ---- Pricebook line GET ----
		{
			name:            "pricebook/full_access",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleFullAccess,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku", "unit_price", "unit_cost", "vendor_unit_cost"},
			forbiddenFields: nil,
		},
		{
			name:            "pricebook/vendor",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleVendor,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku", "unit_price"},
			forbiddenFields: nil, // vendor mask doesn't include unit_cost on PB — that's reseller territory
		},
		{
			name:            "pricebook/reseller",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleReseller,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku", "unit_price"},
			forbiddenFields: []string{"unit_cost", "vendor_unit_cost"},
		},
		{
			name:            "pricebook/sister",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleSister,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku", "unit_price"},
			forbiddenFields: nil,
		},
		{
			name:            "pricebook/sales_mobile",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleSalesMobile,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku", "unit_price"},
			forbiddenFields: []string{"unit_cost", "vendor_unit_cost"},
		},
		{
			name:            "pricebook/technician",
			routePath:       "GET /pricebooks/{id}/lines",
			actorRole:       RoleTechnician,
			canned:          cannedPricebookLine(),
			expectedFields:  []string{"sku"},
			forbiddenFields: []string{"unit_cost", "vendor_unit_cost", "unit_price"},
		},

		// ---- BOQ GET ----
		{
			name:            "boq/full_access",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleFullAccess,
			canned:          cannedBOQ(),
			expectedFields:  []string{"sell_total", "cost_total", "margin_pct"},
			forbiddenFields: nil,
		},
		{
			name:            "boq/vendor",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleVendor,
			canned:          cannedBOQ(),
			expectedFields:  []string{"id", "version_no", "status"},
			forbiddenFields: []string{"sell_total", "cost_total", "margin_pct", "snapshot_hash"},
		},
		{
			name:            "boq/reseller",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleReseller,
			canned:          cannedBOQ(),
			expectedFields:  []string{"id", "version_no", "status"},
			forbiddenFields: []string{"cost_total", "margin_pct", "snapshot_hash"},
		},
		{
			name:            "boq/sister",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleSister,
			canned:          cannedBOQ(),
			expectedFields:  []string{"id", "sell_total"},
			forbiddenFields: nil, // sister sees BOQ commercial total; PII strip is on opportunity/PO
		},
		{
			name:            "boq/sales_mobile",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleSalesMobile,
			canned:          cannedBOQ(),
			expectedFields:  []string{"id", "status"},
			forbiddenFields: []string{"cost_total", "margin_pct"},
		},
		{
			name:            "boq/technician",
			routePath:       "GET /boqs/{id}",
			actorRole:       RoleTechnician,
			canned:          cannedBOQ(),
			expectedFields:  []string{"id", "status"},
			forbiddenFields: []string{"sell_total", "cost_total", "margin_pct", "snapshot_hash"},
		},

		// ---- Customer PO GET (Wave 95 — handlers landing in parallel) ----
		{
			name:            "customer_po/full_access",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleFullAccess,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number", "file_url", "customer_email"},
			forbiddenFields: nil,
		},
		{
			name:            "customer_po/vendor",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleVendor,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number"},
			forbiddenFields: nil,
		},
		{
			name:            "customer_po/reseller",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleReseller,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number"},
			forbiddenFields: nil,
		},
		{
			name:            "customer_po/sister",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleSister,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number", "file_name"},
			forbiddenFields: []string{"file_url", "customer_email", "customer_phone", "customer_contact_name"},
		},
		{
			name:            "customer_po/sales_mobile",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleSalesMobile,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number"},
			forbiddenFields: nil,
		},
		{
			name:            "customer_po/technician",
			routePath:       "GET /customer-pos/{id}",
			actorRole:       RoleTechnician,
			canned:          cannedCustomerPO(),
			expectedFields:  []string{"po_number"},
			forbiddenFields: []string{"file_url", "customer_email"},
		},

		// ---- Opportunity GET ----
		{
			name:            "opportunity/full_access",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleFullAccess,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "contact_email", "billing_address"},
			forbiddenFields: nil,
		},
		{
			name:            "opportunity/vendor",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleVendor,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "stage"},
			forbiddenFields: nil, // vendor mask doesn't touch contact_* — opportunity isn't in their normal view
		},
		{
			name:            "opportunity/reseller",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleReseller,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "stage"},
			forbiddenFields: nil,
		},
		{
			name:            "opportunity/sister",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleSister,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "stage"},
			forbiddenFields: []string{"contact_email", "contact_phone", "billing_address", "customer_email"},
		},
		{
			name:            "opportunity/sales_mobile",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleSalesMobile,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "stage", "contact_email"},
			forbiddenFields: nil,
		},
		{
			name:            "opportunity/technician",
			routePath:       "GET /opportunities/{id}",
			actorRole:       RoleTechnician,
			canned:          cannedOpportunity(),
			expectedFields:  []string{"name", "stage"},
			forbiddenFields: []string{"contact_email", "contact_phone", "billing_email", "billing_address"},
		},

		// ---- EWO GET ----
		{
			name:            "ewo/full_access",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleFullAccess,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address", "recognized_amount", "vendor_rating"},
			forbiddenFields: nil,
		},
		{
			name:            "ewo/vendor",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleVendor,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address", "site_contact_phone"},
			forbiddenFields: nil,
		},
		{
			name:            "ewo/reseller",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleReseller,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address"},
			forbiddenFields: []string{"recognized_amount", "internal_margin", "vendor_rating"},
		},
		{
			name:            "ewo/sister",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleSister,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address", "site_contact_phone"},
			forbiddenFields: nil, // sister IS the executor — they need to see scheduling + recognized amount
		},
		{
			name:            "ewo/sales_mobile",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleSalesMobile,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address"},
			forbiddenFields: []string{"recognized_amount", "internal_margin", "vendor_rating"},
		},
		{
			name:            "ewo/technician",
			routePath:       "GET /ewos/{id}",
			actorRole:       RoleTechnician,
			canned:          cannedEWO(),
			expectedFields:  []string{"ewo_number", "site_address", "site_contact_phone", "scheduled_at"},
			forbiddenFields: []string{"recognized_amount", "internal_margin", "vendor_rating", "approval_chain"},
		},

		// ---- Invoice GET ----
		{
			name:            "invoice/full_access",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleFullAccess,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "total_amount", "file_url"},
			forbiddenFields: nil,
		},
		{
			name:            "invoice/vendor",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleVendor,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "status"},
			forbiddenFields: []string{"total_amount", "paid_amount", "balance"},
		},
		{
			name:            "invoice/reseller",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleReseller,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "status"},
			forbiddenFields: nil, // resellers see their own invoice money; cost basis is what we strip
		},
		{
			name:            "invoice/sister",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleSister,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "total_amount"},
			forbiddenFields: []string{"file_url"},
		},
		{
			name:            "invoice/sales_mobile",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleSalesMobile,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "total_amount"},
			forbiddenFields: nil,
		},
		{
			name:            "invoice/technician",
			routePath:       "GET /invoices/{id}",
			actorRole:       RoleTechnician,
			canned:          cannedInvoice(),
			expectedFields:  []string{"invoice_number", "status"},
			forbiddenFields: []string{"total_amount", "paid_amount", "balance", "file_url"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipReason != "" {
				t.Skip(tc.skipReason)
				return
			}
			masked := MaskFieldsFor(tc.actorRole)
			// FullAccess => no strip, raw payload as-is.
			out := stripMaskedFields(tc.canned, masked)
			outMap, ok := out.(map[string]any)
			if !ok {
				t.Fatalf("expected map[string]any output, got %T (route=%s role=%s)",
					out, tc.routePath, tc.actorRole)
			}
			// Look for fields anywhere in the payload (recursive) — the
			// canned payloads put `cost_total` etc. on the root, but a
			// real BOQ response has them on the nested object. We walk
			// the whole tree.
			js, _ := json.Marshal(outMap)
			body := string(js)
			for _, want := range tc.expectedFields {
				if !containsField(body, want) {
					t.Errorf("[%s/%s] expected field %q to be present, body=%s",
						tc.routePath, tc.actorRole, want, body)
				}
			}
			for _, forbidden := range tc.forbiddenFields {
				if containsField(body, forbidden) {
					t.Errorf("[%s/%s] expected field %q to be stripped, body=%s",
						tc.routePath, tc.actorRole, forbidden, body)
				}
			}
		})
	}
}

// containsField returns true if the JSON body has a `"<name>":` key
// anywhere. We don't accept the field name inside a string value as a
// hit — that's the difference between testing for a key vs testing for
// a substring. Good enough for the contract sweep; if we ever store a
// user-controlled field NAMED `"unit_cost"` inside another payload's
// value, we'd need a real parser walk.
func containsField(jsonBody, field string) bool {
	return strings.Contains(jsonBody, `"`+field+`":`)
}

// =====================================================================
// Classifier ranking — the load-bearing decision point.
// =====================================================================

func TestClassifyActor_Ranking(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  RoleClassification
	}{
		{"nil claims", nil, RoleFullAccess},
		{"empty roles", []string{}, RoleFullAccess},
		{"unknown role -> full access", []string{"randomly_named_role"}, RoleFullAccess},
		{"super_admin", []string{"super_admin"}, RoleFullAccess},
		{"finance_admin", []string{"finance_admin"}, RoleFullAccess},
		{"vendor_user", []string{"vendor_user"}, RoleVendor},
		{"internal_vendor", []string{"internal_vendor"}, RoleVendor},
		{"reseller_admin", []string{"reseller_admin"}, RoleReseller},
		{"sister_ops", []string{"sister_ops"}, RoleSister},
		{"sales_mobile", []string{"sales_mobile"}, RoleSalesMobile},
		{"technician", []string{"technician"}, RoleTechnician},

		// Multi-role rankings — the load-bearing rule. super_admin wins
		// over any restricted role; vendor wins over reseller/sister/...
		{
			name:  "super_admin + reseller_admin -> full access",
			roles: []string{"super_admin", "reseller_admin"},
			want:  RoleFullAccess,
		},
		{
			name:  "vendor_user + reseller_admin -> vendor",
			roles: []string{"vendor_user", "reseller_admin"},
			want:  RoleVendor,
		},
		{
			name:  "reseller_admin + sister_ops -> reseller",
			roles: []string{"reseller_admin", "sister_ops"},
			want:  RoleReseller,
		},
		{
			name:  "sister_ops + technician -> sister",
			roles: []string{"sister_ops", "technician"},
			want:  RoleSister,
		},
		{
			name:  "technician + sales_mobile -> technician (heavier mask)",
			roles: []string{"technician", "sales_mobile"},
			want:  RoleTechnician,
		},
		// =====================================================================
		// Wave 104 — Edge #15: multi-capability sister
		//
		// A user seconded to a sister subsidiary that carries BOTH supplier
		// (internal_vendor) and reseller (reseller_admin) capabilities ends
		// up with both role tags. ClassifyActor must return the most-
		// privileged classification — i.e. the one with the NARROWEST mask
		// applied — so a leak through "we forgot you can also be a vendor"
		// is impossible. By the precedence ladder, vendor wins over
		// reseller; reseller wins over sister.
		// =====================================================================
		{
			name:  "wave104 edge15: vendor + reseller -> vendor (narrowest mask wins)",
			roles: []string{"internal_vendor", "reseller_admin"},
			want:  RoleVendor,
		},
		{
			name:  "wave104 edge15: vendor + sister_ops -> vendor",
			roles: []string{"internal_vendor", "sister_ops"},
			want:  RoleVendor,
		},
		{
			name:  "wave104 edge15: reseller + sister_ops -> reseller",
			roles: []string{"reseller_finance", "sister_ops"},
			want:  RoleReseller,
		},
		{
			name:  "wave104 edge15: triple cap (vendor + reseller + sister) -> vendor",
			roles: []string{"internal_vendor", "reseller_admin", "sister_ops"},
			want:  RoleVendor,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var claims *auth.Claims
			if tc.roles != nil {
				claims = &auth.Claims{Roles: tc.roles}
			}
			got := ClassifyActor(claims)
			if got != tc.want {
				t.Fatalf("ClassifyActor(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

// =====================================================================
// CompanyScope predicate.
// =====================================================================

func TestCompanyScope_Wildcard(t *testing.T) {
	a := uuid.New()
	s := CompanyScope{Wildcard: true}
	if !s.AppliesTo(a) {
		t.Fatalf("wildcard scope must apply to any id")
	}
}

func TestCompanyScope_AppliesTo(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	s := CompanyScope{AllowedSubsidiaryIDs: []uuid.UUID{a, b}}
	if !s.AppliesTo(a) {
		t.Errorf("scope should include a")
	}
	if !s.AppliesTo(b) {
		t.Errorf("scope should include b")
	}
	if s.AppliesTo(c) {
		t.Errorf("scope should NOT include c")
	}
}

func TestCompanyScope_EmptyDeniesAll(t *testing.T) {
	a := uuid.New()
	s := CompanyScope{AllowedSubsidiaryIDs: []uuid.UUID{}}
	if s.AppliesTo(a) {
		t.Fatalf("empty (non-nil) scope must deny — got allow")
	}
}

func TestResolveScope_NoClaims(t *testing.T) {
	s := ResolveScope(context.Background())
	if s.Wildcard {
		t.Fatalf("no claims should NOT yield wildcard scope, got %+v", s)
	}
	if len(s.AllowedSubsidiaryIDs) != 0 {
		t.Fatalf("no claims should yield empty scope, got %+v", s)
	}
}

func TestResolveScope_AttachedScopeWins(t *testing.T) {
	a := uuid.New()
	want := CompanyScope{AllowedSubsidiaryIDs: []uuid.UUID{a}, SubsidiaryID: &a}
	ctx := WithScope(context.Background(), want)
	got := ResolveScope(ctx)
	if len(got.AllowedSubsidiaryIDs) != 1 || got.AllowedSubsidiaryIDs[0] != a {
		t.Fatalf("attached scope should pass through, got %+v", got)
	}
}
