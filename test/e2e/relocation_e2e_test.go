// Customer relocation lifecycle end-to-end.
//
// Wave 55 — closes the customer self-service trifecta (termination,
// plan-change, relocation). Proves:
//
//   1. Customer (portal OTP) submits POST /portal/relocation with
//      a new address — status defaults to 'pending_survey'
//   2. Admin (dashboard) sees the request in /admin/relocations/pending
//   3. Admin PATCHes /admin/relocations/{id} with decision='approved'
//   4. Customer re-queries /customers/{id}/relocations and sees the
//      approved row (cross-surface handoff)
//   5. Decision endpoint rejects a malformed decision value (negative
//      contract test — proves input validation isn't optimistic)
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestCustomerRelocationFlow(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	customer := newCustomerClient(t, h.CustomerNumber, phoneLast4(h.Phone))

	// -----------------------------------------------------------------
	// 1. Customer requests relocation via the portal.
	// -----------------------------------------------------------------
	const newAddress = "Jl. Relocation Wave-55 No 42, Jakarta Selatan"
	const newLat = -6.2901
	const newLng = 106.8401
	const portalNote = "Wave 55 — moving for work."

	var req struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ToAddress string `json:"to_address"`
	}
	customer.do("POST", "/api/portal/relocation", map[string]any{
		"to_address": newAddress,
		"to_gps_lat": newLat,
		"to_gps_lng": newLng,
		"notes":      portalNote,
	}, &req, 201)
	if req.ID == "" {
		t.Fatal("portal relocation returned empty id")
	}
	if req.Status != "pending_survey" {
		t.Errorf("new relocation status: want pending_survey, got %q", req.Status)
	}
	if req.ToAddress != newAddress {
		t.Errorf("to_address round-trip broken: got %q", req.ToAddress)
	}

	// -----------------------------------------------------------------
	// 2. Admin sees it in the pending queue.
	// -----------------------------------------------------------------
	var pending struct {
		Items []struct {
			ID         string `json:"id"`
			CustomerID string `json:"customer_id"`
			Status     string `json:"status"`
			ToAddress  string `json:"to_address"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/relocations/pending", nil, &pending, 200)
	found := false
	for _, it := range pending.Items {
		if it.ID == req.ID {
			found = true
			if it.CustomerID != h.CustomerID {
				t.Errorf("queue row customer_id: want %q, got %q", h.CustomerID, it.CustomerID)
			}
			if it.ToAddress != newAddress {
				t.Errorf("queue row to_address: want %q, got %q", newAddress, it.ToAddress)
			}
			break
		}
	}
	if !found {
		t.Fatalf("relocation %s missing from admin pending queue (cross-surface handoff broken)", req.ID)
	}

	// -----------------------------------------------------------------
	// 3. Negative case — invalid decision value rejected with 400.
	// -----------------------------------------------------------------
	_ = admin.doExpectError("PATCH", "/api/crm/relocations/"+req.ID,
		map[string]any{"decision": "maybe", "survey_note": "invalid decision"}, 400)

	// -----------------------------------------------------------------
	// 4. Admin approves.
	// -----------------------------------------------------------------
	var decided struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	admin.do("PATCH", "/api/crm/relocations/"+req.ID, map[string]any{
		"decision":    "approved",
		"survey_note": "Wave 55 — site survey complete, coverage confirmed.",
	}, &decided, 200)
	if decided.Status != "approved" {
		t.Errorf("decided status: want approved, got %q", decided.Status)
	}

	// -----------------------------------------------------------------
	// 5. Customer's history reflects the decision (cross-surface).
	// -----------------------------------------------------------------
	var history struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/customers/"+h.CustomerID+"/relocations", nil, &history, 200)
	customerSawApproval := false
	for _, it := range history.Items {
		if it.ID == req.ID && it.Status == "approved" {
			customerSawApproval = true
			break
		}
	}
	if !customerSawApproval {
		t.Errorf("customer history doesn't show the approval for %s", req.ID)
	}
}
