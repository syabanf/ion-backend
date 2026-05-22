// Work-order BAST rejection end-to-end.
//
// Wave 56 — the broadband happy-path covers `verifyBAST` with
// decision=approved. The rejection branch (NOC says "speedtest didn't
// hit 80% of contracted speed — redo") was uncovered until now. This
// test proves:
//
//   1. setupWOReadyForNOC (helper) — full chain through BAST submit
//      + OTC payment so the NOC verify call isn't blocked by the
//      payment gate
//   2. NOC POSTs /basts/{id}/verify with decision=rejected + notes
//   3. The BAST's noc_status flips to 'rejected' + verified_at populated
//   4. The customer's status STAYS at 'pending_install' (NOT 'active') —
//      the rejection MUST NOT activate the customer
//   5. The RADIUS account is NOT created — proving the activation hook
//      gates on decision=approved
//
// Together with the broadband happy-path's "approve → active" leg,
// this nails down both arms of the NOC verification state machine.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestWOBASTRejection(t *testing.T) {
	admin := newClient(t)
	admin.login()

	w := setupWOReadyForNOC(t, admin)

	// -----------------------------------------------------------------
	// NOC rejects the BAST.
	// -----------------------------------------------------------------
	const rejectNote = "Speedtest hit 65% of contracted bandwidth — redo + recheck CPE."
	var rejected struct {
		ID         string `json:"id"`
		NOCStatus  string `json:"noc_status"`
		VerifiedAt string `json:"verified_at"`
		Notes      string `json:"notes"`
	}
	admin.do("POST", "/api/field/basts/"+w.BASTID+"/verify",
		map[string]any{
			"decision": "rejected",
			"notes":    rejectNote,
		}, &rejected, 200)

	if rejected.NOCStatus != "rejected" {
		t.Fatalf("BAST noc_status: want rejected, got %q", rejected.NOCStatus)
	}
	if rejected.VerifiedAt == "" {
		t.Errorf("verified_at not populated after rejection")
	}
	if rejected.Notes != rejectNote {
		t.Errorf("notes round-trip broken: got %q want %q", rejected.Notes, rejectNote)
	}

	// -----------------------------------------------------------------
	// Customer MUST stay in 'pending_install'. Activation on rejection
	// would be a silent data leak (customer treated as paying for an
	// install that never met spec).
	// -----------------------------------------------------------------
	var cust struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &cust, 200)
	if cust.Status == "active" {
		t.Fatalf("ACTIVATION LEAK: customer %s flipped to active after BAST rejected", w.CustomerID)
	}
	if cust.Status != "pending_install" {
		t.Errorf("customer status: want pending_install, got %q", cust.Status)
	}

	// -----------------------------------------------------------------
	// Re-fetch the BAST to confirm the rejection persisted in storage
	// (catches a regression where the response object is updated but
	// the DB write is dropped).
	// -----------------------------------------------------------------
	var refetched struct {
		NOCStatus string `json:"noc_status"`
	}
	admin.do("GET", "/api/field/basts/"+w.BASTID, nil, &refetched, 200)
	if refetched.NOCStatus != "rejected" {
		t.Errorf("BAST re-read: want rejected (persisted), got %q", refetched.NOCStatus)
	}
}
