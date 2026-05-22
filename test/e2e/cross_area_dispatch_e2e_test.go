// Cross-area dispatch borrow end-to-end.
//
// Wave 51 — proves the field cross-area request flow (PRD §6.5).
// When a WO's home branch has no available technician but a neighbouring
// branch does, the team leader can request a cross-area borrow: the
// WO stays attached to the home branch but is offered to the target
// branch's team leader for pickup.
//
//   1. setupCoveredCustomer creates a customer + order in branch A
//   2. Admin creates a second branch B (the borrow target)
//   3. Admin creates an install WO for the customer in branch A
//   4. Admin POSTs /work-orders/{id}/cross-area with target_branch_id = B
//   5. GET /cross-area/pending lists the WO with target_branch_name = B
//   6. The WO row now has is_cross_area=true + cross_area_target_branch_id=B
//
//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestCrossAreaDispatch(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Build the borrow-target branch (different area under the same
	//    regional). The customer's branch is already wired by the
	//    helper as area-level branch h.BranchID.
	// -----------------------------------------------------------------
	// First find the regional parent of h.BranchID so the target sits
	// under the same regional (the cross-area flow is between sibling
	// areas, not across regions).
	var srcBranch struct {
		ID       string  `json:"id"`
		ParentID *string `json:"parent_id"`
	}
	admin.do("GET", "/api/identity/branches/"+h.BranchID, nil, &srcBranch, 200)
	if srcBranch.ParentID == nil {
		t.Fatal("source branch has no parent — setupCoveredCustomer invariant broken")
	}

	var targetBranch struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":      "W51 Borrow Target " + sx,
		"code":      "W51-BORROW-" + sx,
		"level":     "area",
		"parent_id": *srcBranch.ParentID,
	}, &targetBranch, 201)

	// -----------------------------------------------------------------
	// 2. Create an install WO in the source branch.
	// -----------------------------------------------------------------
	scheduled := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	var wo struct {
		ID       string `json:"id"`
		WoNumber string `json:"wo_number"`
		Status   string `json:"status"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id":       h.OrderID,
		"customer_id":    h.CustomerID,
		"kind":           "install",
		"scheduled_date": scheduled,
		"branch_id":      h.BranchID,
		"notes":          "W51 cross-area borrow probe",
	}, &wo, 201)
	if wo.ID == "" {
		t.Fatal("WO create returned empty id")
	}

	// -----------------------------------------------------------------
	// 3. Request a cross-area borrow.
	// -----------------------------------------------------------------
	const borrowReason = "Source branch overbooked; closest available tech is in the neighbouring area."
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/cross-area", map[string]any{
		"target_branch_id": targetBranch.ID,
		"reason":           borrowReason,
	}, nil, 200)

	// -----------------------------------------------------------------
	// 4. The pending queue lists the WO.
	// -----------------------------------------------------------------
	var pending struct {
		Items []struct {
			WoID           string  `json:"wo_id"`
			WoNumber       string  `json:"wo_number"`
			TargetBranchID string  `json:"target_branch_id"`
			TargetBranch   string  `json:"target_branch_name"`
			Reason         *string `json:"reason"`
		} `json:"items"`
	}
	admin.do("GET", "/api/field/cross-area/pending", nil, &pending, 200)
	var got *struct {
		WoID           string  `json:"wo_id"`
		WoNumber       string  `json:"wo_number"`
		TargetBranchID string  `json:"target_branch_id"`
		TargetBranch   string  `json:"target_branch_name"`
		Reason         *string `json:"reason"`
	}
	for i := range pending.Items {
		if pending.Items[i].WoID == wo.ID {
			got = &pending.Items[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("WO %s missing from /cross-area/pending (queue broken)", wo.ID)
	}
	if got.TargetBranchID != targetBranch.ID {
		t.Errorf("target_branch_id: want %q, got %q", targetBranch.ID, got.TargetBranchID)
	}
	if got.TargetBranch == "" {
		t.Errorf("target_branch_name not joined into queue row")
	}
	if got.Reason == nil || *got.Reason != borrowReason {
		t.Errorf("reason not persisted: got %v, want %q", got.Reason, borrowReason)
	}

	// -----------------------------------------------------------------
	// 5. Fetching the WO directly shows the cross-area flag + target.
	// -----------------------------------------------------------------
	var woAfter struct {
		ID                      string  `json:"id"`
		IsCrossArea             bool    `json:"is_cross_area"`
		CrossAreaTargetBranchID *string `json:"cross_area_target_branch_id,omitempty"`
		CrossAreaReason         *string `json:"cross_area_reason,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+wo.ID, nil, &woAfter, 200)
	if !woAfter.IsCrossArea {
		t.Errorf("is_cross_area flag not set on WO after request")
	}
	if woAfter.CrossAreaTargetBranchID == nil || *woAfter.CrossAreaTargetBranchID != targetBranch.ID {
		t.Errorf("cross_area_target_branch_id: want %q, got %v", targetBranch.ID, woAfter.CrossAreaTargetBranchID)
	}
}
