// Work-order reschedule end-to-end.
//
// Wave 50 — proves the reschedule contract (PRD §6):
//
//   1. setupCoveredCustomer creates a customer with an order
//   2. Admin creates an install WO for the order
//   3. Admin reschedules the WO with a reason + new_date
//   4. WO's scheduled_date / status reflects the change
//   5. GET /work-orders/{id}/reschedules lists the history with the
//      reason captured
//
//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestWOReschedule(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// Create an install WO for the customer's order. The team-leader
	// assignment + tech pairing flows are exercised in broadband_e2e_test;
	// here we just need a WO that exists so we can reschedule it.
	originalDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	var wo struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		ScheduledDate string `json:"scheduled_date"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id":       h.OrderID,
		"customer_id":    h.CustomerID,
		"kind":           "install",
		"scheduled_date": originalDate,
		"branch_id":      h.BranchID,
		"notes":          "Wave 50 reschedule probe",
	}, &wo, 201)
	if wo.ID == "" {
		t.Fatal("WO create returned empty id")
	}

	// Reschedule 2 days later.
	newDate := time.Now().Add(72 * time.Hour).Format(time.RFC3339)
	const reschedReason = "customer_requested"
	const reschedNote = "Wave 50 — customer pushed install to weekend."

	var afterResched struct {
		ID            string `json:"id"`
		ScheduledDate string `json:"scheduled_date"`
		RescheduledAt string `json:"rescheduled_at"`
	}
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/reschedule", map[string]any{
		"new_date": newDate,
		"reason":   reschedReason,
		"notes":    reschedNote,
	}, &afterResched, 200)
	if afterResched.ScheduledDate == wo.ScheduledDate {
		t.Errorf("scheduled_date didn't change: still %q", afterResched.ScheduledDate)
	}
	if afterResched.RescheduledAt == "" {
		t.Errorf("rescheduled_at not populated after reschedule")
	}

	// Verify the history endpoint records what happened.
	var history struct {
		Items []struct {
			Reason  string `json:"reason"`
			NewDate string `json:"new_date"`
			Notes   string `json:"notes"`
		} `json:"items"`
	}
	admin.do("GET", "/api/field/work-orders/"+wo.ID+"/reschedules", nil, &history, 200)
	if len(history.Items) == 0 {
		t.Fatal("reschedule history empty after a reschedule")
	}
	last := history.Items[len(history.Items)-1]
	if last.Reason != reschedReason {
		t.Errorf("history reason: want %q, got %q", reschedReason, last.Reason)
	}
	if last.Notes != reschedNote {
		t.Errorf("history notes: want %q, got %q", reschedNote, last.Notes)
	}
}
