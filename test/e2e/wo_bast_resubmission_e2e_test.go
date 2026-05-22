// BAST resubmission after rejection end-to-end.
//
// Wave 58 — proves that after NOC rejects a BAST, the tech can submit
// a fresh one (and NOC can approve THAT to activate the customer).
// The usecase comment in field/usecase/service.go is explicit:
//
//   "Refuse double-submit: an active (non-rejected) BAST already exists."
//
// i.e. a rejected BAST does NOT block resubmission. This test pins
// that contract. Without it, a regression that tightens the gate to
// "any BAST exists" would leave techs stuck after a single rejection.
//
//   1. setupWOReadyForNOC (helper) — BAST already submitted + paid
//   2. NOC rejects first BAST
//   3. Tech POSTs /work-orders/{id}/bast a second time → 200 with
//      a new BAST ID, noc_status='pending'
//   4. New BAST ID differs from the rejected one (proves it's not
//      an update-in-place)
//   5. NOC approves the new BAST → customer.status → 'active'
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestBASTResubmissionAfterRejection(t *testing.T) {
	admin := newClient(t)
	admin.login()

	w := setupWOReadyForNOC(t, admin)

	// -----------------------------------------------------------------
	// 1. Reject the first BAST.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/field/basts/"+w.BASTID+"/verify", map[string]any{
		"decision": "rejected",
		"notes":    "Wave 58 — speedtest fell short of spec; redo.",
	}, nil, 200)

	// -----------------------------------------------------------------
	// 2. Resubmit BAST on the same WO. Must succeed (rejected BASTs
	//    don't block new submissions).
	// -----------------------------------------------------------------
	var resub struct {
		ID        string `json:"id"`
		NOCStatus string `json:"noc_status"`
	}
	admin.do("POST", "/api/field/work-orders/"+w.WOID+"/bast", map[string]any{
		"sign_off_mode": "on_site",
	}, &resub, 200)
	if resub.ID == "" {
		t.Fatal("resubmission returned empty BAST id")
	}
	if resub.ID == w.BASTID {
		t.Fatalf("resubmission reused old BAST id %s — should mint a new row", w.BASTID)
	}
	if resub.NOCStatus != "pending" {
		t.Errorf("new BAST noc_status: want pending, got %q", resub.NOCStatus)
	}

	// -----------------------------------------------------------------
	// 3. Resubmitting AGAIN (without the in-flight one being decided)
	//    must 409. The "active BAST already exists" guard kicks in.
	// -----------------------------------------------------------------
	code := admin.doExpectError("POST", "/api/field/work-orders/"+w.WOID+"/bast",
		map[string]any{"sign_off_mode": "on_site"}, 409)
	if code != "bast.already_submitted" {
		t.Errorf("double-submit error code: want bast.already_submitted, got %q", code)
	}

	// -----------------------------------------------------------------
	// 4. NOC approves the new BAST → customer flips to active.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/field/basts/"+resub.ID+"/verify", map[string]any{
		"decision": "approved",
		"notes":    "Wave 58 — speedtest passed on redo.",
	}, nil, 200)

	var cust struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &cust, 200)
	if cust.Status != "active" {
		t.Errorf("customer status after approved resub-BAST: want active, got %q", cust.Status)
	}
}
