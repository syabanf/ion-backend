// Plan upgrade end-to-end — proves the 'applied' decision branch.
//
// Wave 57 — the Wave-50 plan_change_e2e tested request → approved.
// 'approved' is an intermediate decision: the plan change is greenlit
// but not yet in effect. The terminal happy path is 'applied' — the
// admin commits the change, applied_at gets stamped, and the customer
// sees the new plan on their next billing cycle.
//
// This test proves:
//   1. Active customer (BB-30) requests upgrade to BB-100 via portal
//   2. Admin reads /api/crm/plan-changes/pending, sees the request
//   3. Admin PATCHes with decision='applied'
//   4. Request flips to status='applied' + applied_at stamped
//   5. Customer's /portal/plan-changes shows the applied row
//   6. Re-PATCHing with another decision returns 409 (already decided)
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestPlanUpgradeApplied(t *testing.T) {
	admin := newClient(t)
	admin.login()

	w := setupActiveCustomer(t, admin)
	customer := newCustomerClient(t, "", "") // placeholder — fetched below

	// We need customer_number + phone to log in via portal. Pull from
	// the customer record (setupActiveCustomer doesn't expose them).
	var custDetails struct {
		CustomerNumber string `json:"customer_number"`
		Phone          string `json:"phone"`
		ProductID      string `json:"product_id"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &custDetails, 200)
	if custDetails.CustomerNumber == "" || custDetails.Phone == "" {
		t.Fatalf("customer %s missing customer_number/phone: %+v", w.CustomerID, custDetails)
	}
	customer = newCustomerClient(t, custDetails.CustomerNumber, phoneLast4(custDetails.Phone))

	// Find BB-100 (upgrade target).
	var products struct {
		Items []struct {
			ID, Code string
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/products", nil, &products, 200)
	var upgradeID string
	for _, p := range products.Items {
		if p.Code == "BB-100" {
			upgradeID = p.ID
		}
	}
	if upgradeID == "" {
		t.Fatal("BB-100 not seeded — Wave 47 broken?")
	}

	// -----------------------------------------------------------------
	// 1. Customer (portal) requests upgrade.
	// -----------------------------------------------------------------
	var req struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		ToProductID string `json:"to_product_id"`
	}
	customer.do("POST", "/api/portal/plan-change", map[string]any{
		"to_product_id": upgradeID,
		"change_kind":   "upgrade",
		"reason":        "Wave 57 — upgrade applied test.",
	}, &req, 201)
	if req.ID == "" {
		t.Fatal("plan-change request returned empty id")
	}

	// -----------------------------------------------------------------
	// 2. Admin sees it in the pending queue.
	// -----------------------------------------------------------------
	var queue struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/plan-changes/pending", nil, &queue, 200)
	found := false
	for _, it := range queue.Items {
		if it.ID == req.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("plan-change %s missing from pending queue", req.ID)
	}

	// -----------------------------------------------------------------
	// 3. Admin applies the change (skips 'approved' intermediate).
	// -----------------------------------------------------------------
	admin.do("PATCH", "/api/crm/plan-changes/"+req.ID, map[string]any{
		"decision": "applied",
		"note":     "Wave 57 — upgrade applied.",
	}, nil, 200)

	// -----------------------------------------------------------------
	// 4. Re-decision returns 409 (terminal state guard).
	// -----------------------------------------------------------------
	got := admin.statusOnlyJSON("PATCH", "/api/crm/plan-changes/"+req.ID,
		map[string]any{"decision": "rejected", "note": "should be blocked"})
	if got != 409 {
		t.Errorf("re-decide on applied plan-change: want 409, got %d", got)
	}

	// -----------------------------------------------------------------
	// 5. Customer's portal history shows the applied row with applied_at.
	// -----------------------------------------------------------------
	var custList struct {
		Items []struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			AppliedAt *string `json:"applied_at"`
		} `json:"items"`
	}
	customer.do("GET", "/api/portal/plan-changes", nil, &custList, 200)
	var seen *struct {
		ID        string  `json:"id"`
		Status    string  `json:"status"`
		AppliedAt *string `json:"applied_at"`
	}
	for i := range custList.Items {
		if custList.Items[i].ID == req.ID {
			seen = &custList.Items[i]
			break
		}
	}
	if seen == nil {
		t.Fatalf("customer's portal plan-changes doesn't list %s", req.ID)
	}
	if seen.Status != "applied" {
		t.Errorf("customer view status: want applied, got %q", seen.Status)
	}
	if seen.AppliedAt == nil || *seen.AppliedAt == "" {
		t.Errorf("applied_at not stamped on customer view of %s", req.ID)
	}
}
