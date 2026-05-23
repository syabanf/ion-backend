// Wave 118 — Permission bundles tests (TC-RBAC-* regression edge).

package http

import (
	"testing"
)

func TestCommonPermissionBundles_ReturnsAllSix(t *testing.T) {
	bundles := CommonPermissionBundles()
	if len(bundles) != 6 {
		t.Fatalf("want 6 bundles, got %d", len(bundles))
	}
	wantCodes := map[string]bool{
		"sales_starter":    false,
		"sales_advanced":   false,
		"ops_starter":      false,
		"ops_advanced":     false,
		"finance_starter":  false,
		"finance_advanced": false,
	}
	for _, b := range bundles {
		if _, ok := wantCodes[b.Code]; !ok {
			t.Fatalf("unexpected bundle code: %s", b.Code)
		}
		wantCodes[b.Code] = true
		if b.DisplayName == "" {
			t.Fatalf("bundle %s missing display_name", b.Code)
		}
		if b.Persona == "" {
			t.Fatalf("bundle %s missing persona", b.Code)
		}
		if len(b.Permissions) == 0 {
			t.Fatalf("bundle %s has no permissions", b.Code)
		}
	}
	for code, seen := range wantCodes {
		if !seen {
			t.Fatalf("bundle %s was not in the result", code)
		}
	}
}

func TestCommonPermissionBundles_BundlesHaveExpectedPermissions(t *testing.T) {
	byCode := map[string]PermissionBundle{}
	for _, b := range CommonPermissionBundles() {
		byCode[b.Code] = b
	}

	// sales_starter must include lead create + read.
	sb, ok := byCode["sales_starter"]
	if !ok {
		t.Fatal("sales_starter bundle missing")
	}
	if !containsString(sb.Permissions, "crm.lead.create") {
		t.Fatal("sales_starter must include crm.lead.create")
	}
	if !containsString(sb.Permissions, "crm.lead.read") {
		t.Fatal("sales_starter must include crm.lead.read")
	}

	// finance_advanced must include hris.employee.read (the Wave 118 link).
	fa, ok := byCode["finance_advanced"]
	if !ok {
		t.Fatal("finance_advanced bundle missing")
	}
	if !containsString(fa.Permissions, "hris.employee.read") {
		t.Fatal("finance_advanced must include hris.employee.read")
	}
	if !containsString(fa.Permissions, "billing.commission.approve") {
		t.Fatal("finance_advanced must include billing.commission.approve")
	}

	// ops_advanced must include wo.assign + bast.verify.
	oa, ok := byCode["ops_advanced"]
	if !ok {
		t.Fatal("ops_advanced bundle missing")
	}
	if !containsString(oa.Permissions, "field.wo.assign") {
		t.Fatal("ops_advanced must include field.wo.assign")
	}
	if !containsString(oa.Permissions, "field.bast.verify") {
		t.Fatal("ops_advanced must include field.bast.verify")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
