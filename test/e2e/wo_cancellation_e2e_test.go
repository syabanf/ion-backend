// WO cancellation end-to-end.
//
// Wave 58 — proves POST /work-orders/{id}/status with status='cancelled'
// is allowed from multiple lifecycle states, and that cancellation is a
// terminal transition (you can't un-cancel).
//
// Two scenarios:
//
//   A. Cancel from 'unassigned' (the common case — admin spots a
//      duplicate install order, cancels before any tech is involved)
//   B. Cancel from 'in_progress' (the rarer case — tech can't complete,
//      admin pulls the WO so it doesn't sit there blocking the
//      customer's order)
//
// Plus negative: trying to flip a cancelled WO back to 'in_progress'
// must 409 (terminal state guard).
//
//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestWOCancellation(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// Scenario A — cancel from unassigned (most common path).
	// -----------------------------------------------------------------
	t.Run("A_cancel_unassigned", func(t *testing.T) {
		var wo struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		admin.do("POST", "/api/field/work-orders", map[string]any{
			"order_id": h.OrderID,
		}, &wo, 201)
		if wo.Status != "unassigned" && wo.Status != "created" {
			t.Errorf("fresh WO status: want unassigned/created, got %q", wo.Status)
		}

		var cancelled struct {
			Status      string `json:"status"`
			CancelledAt string `json:"cancelled_at"`
		}
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
			map[string]any{
				"status": "cancelled",
				"notes":  "Wave 58 — duplicate install order " + sx,
			}, &cancelled, 200)
		if cancelled.Status != "cancelled" {
			t.Errorf("status after cancel: want cancelled, got %q", cancelled.Status)
		}

		// Terminal guard — can't un-cancel.
		code := admin.doExpectError("POST", "/api/field/work-orders/"+wo.ID+"/status",
			map[string]any{"status": "in_progress"}, 409)
		if code == "" {
			t.Errorf("expected error code on un-cancel attempt; got empty")
		}
	})

	// -----------------------------------------------------------------
	// Scenario B — cancel from in_progress. Needs a team + assignment
	// chain. We reuse setupWOReadyForNOC's WO but call cancel BEFORE
	// it submits BAST, by building a separate WO + assignment chain.
	// -----------------------------------------------------------------
	t.Run("B_cancel_in_progress", func(t *testing.T) {
		// Build a fresh team + techs since this is a different WO.
		mkUser := func(prefix, role, grade string) string {
			body := map[string]any{
				"employee_id": prefix + "-" + sx,
				"full_name":   "W58 " + prefix + " " + sx,
				"email":       "w58-" + prefix + "-" + sx + "@ion.local",
				"phone":       "+62811W58" + prefix,
				"password":    "TempPass!2026",
				"branch_id":   h.BranchID,
				"roles":       []string{role},
			}
			if grade != "" {
				body["technician_grade"] = grade
			}
			var u struct {
				ID string `json:"id"`
			}
			admin.do("POST", "/api/identity/users", body, &u, 201)
			return u.ID
		}
		tlID := mkUser("CXTL", "team_leader", "")
		srID := mkUser("CXSR", "technician", "senior")
		jrID := mkUser("CXJR", "technician", "junior")

		var team struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/field/teams", map[string]any{
			"name":           "W58 Cancel Team " + sx,
			"code":           "W58-CX-" + sx,
			"branch_id":      h.BranchID,
			"team_leader_id": tlID,
		}, &team, 201)
		admin.do("POST", "/api/field/teams/"+team.ID+"/members",
			map[string]any{"user_id": srID, "grade": "senior"}, nil, 201)
		admin.do("POST", "/api/field/teams/"+team.ID+"/members",
			map[string]any{"user_id": jrID, "grade": "junior"}, nil, 201)

		// Fresh WO — note that the order already has a WO from scenario A
		// (cancelled), so the service should allow another. If it
		// 409s on "order already has a WO" then we know the cancel
		// path needs to clear that constraint too.
		scheduled := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
		var wo struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/field/work-orders", map[string]any{
			"order_id":       h.OrderID,
			"customer_id":    h.CustomerID,
			"kind":           "install",
			"scheduled_date": scheduled,
			"branch_id":      h.BranchID,
		}, &wo, 201)

		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/route",
			map[string]any{"team_id": team.ID}, nil, 200)
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/assign", map[string]any{
			"lead_id":        srID,
			"lead_grade":     "senior",
			"observer_id":    jrID,
			"observer_grade": "junior",
		}, nil, 200)
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		// Cancel from in_progress.
		var cancelled struct {
			Status string `json:"status"`
		}
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
			map[string]any{
				"status": "cancelled",
				"notes":  "Wave 58 — tech reports site abandoned by customer",
			}, &cancelled, 200)
		if cancelled.Status != "cancelled" {
			t.Errorf("in_progress → cancelled didn't take: got %q", cancelled.Status)
		}
	})
}
