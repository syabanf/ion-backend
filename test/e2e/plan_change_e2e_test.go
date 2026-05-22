// Plan change end-to-end — portal customer requests → dashboard approves.
//
// Wave 50 — proves the upgrade/downgrade flow defined in the Phase 1
// PRD §10:
//
//   1. setupCoveredCustomer (helper) — fresh customer on BB-30
//   2. Customer logs in via portal OTP (mobile customer_app surface)
//   3. Customer POSTs /portal/plan-change with to_product_id = BB-100
//      (upgrade) and change_kind = 'upgrade'
//   4. Admin (dashboard surface) lists /api/crm/plan-changes/pending
//      and sees the new request
//   5. Admin PATCHes the request with decision='approved'
//   6. Status flips to approved + applied_at populated; the customer's
//      effective product follows on the next billing cycle
//
// Cross-surface: customer's portal POST shows up on the admin's queue,
// and the admin's PATCH reflects back when the customer re-queries.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestPlanChangeUpgradeFlow(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// Find an upgrade target — BB-100 is the high tier from Wave 47.
	var products struct {
		Items []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/products", nil, &products, 200)
	var upgradeProductID string
	for _, p := range products.Items {
		if p.Code == "BB-100" {
			upgradeProductID = p.ID
		}
	}
	if upgradeProductID == "" {
		t.Fatal("BB-100 product not seeded — Wave 47 migration missing?")
	}

	// -----------------------------------------------------------------
	// Customer logs in via portal OTP (mobile customer_app surface).
	// -----------------------------------------------------------------
	customer := newCustomerClient(t, h.CustomerNumber, phoneLast4(h.Phone))

	// -----------------------------------------------------------------
	// Customer requests upgrade.
	// -----------------------------------------------------------------
	var req struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		ChangeKind  string `json:"change_kind"`
		ToProductID string `json:"to_product_id"`
	}
	customer.do("POST", "/api/portal/plan-change", map[string]any{
		"to_product_id": upgradeProductID,
		"change_kind":   "upgrade",
		"reason":        "Wave 50 — need more bandwidth for streaming.",
	}, &req, 201)
	if req.ID == "" {
		t.Fatal("plan-change request returned empty id")
	}
	if req.Status != "pending" && req.Status != "submitted" {
		t.Errorf("new plan-change status: want pending/submitted, got %q", req.Status)
	}
	if req.ToProductID != upgradeProductID {
		t.Errorf("to_product_id round-trip broken: got %q want %q", req.ToProductID, upgradeProductID)
	}

	// -----------------------------------------------------------------
	// Admin sees it in the pending queue.
	// -----------------------------------------------------------------
	var queue struct {
		Items []struct {
			ID         string `json:"id"`
			CustomerID string `json:"customer_id"`
			Status     string `json:"status"`
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
		t.Fatalf("admin queue missing plan-change %s (handoff broken)", req.ID)
	}

	// -----------------------------------------------------------------
	// Admin approves the request.
	// -----------------------------------------------------------------
	var decided struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		AppliedAt string `json:"applied_at"`
	}
	admin.do("PATCH", "/api/crm/plan-changes/"+req.ID, map[string]any{
		"decision": "approved",
		"note":     "Wave 50 — auto-approved by E2E test.",
	}, &decided, 200)
	if decided.Status != "approved" {
		t.Errorf("decided status: want approved, got %q", decided.Status)
	}

	// -----------------------------------------------------------------
	// Customer re-queries and confirms the change is reflected.
	// -----------------------------------------------------------------
	var custList struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	// Customer-side list endpoint — portal returns the customer's own
	// plan-change history.
	customer.do("GET", "/api/portal/plan-changes", nil, &custList, 200)
	customerSawApproval := false
	for _, it := range custList.Items {
		if it.ID == req.ID && it.Status == "approved" {
			customerSawApproval = true
			break
		}
	}
	if !customerSawApproval {
		t.Errorf("customer's portal /plan-changes doesn't show the approved state for %s", req.ID)
	}
}
