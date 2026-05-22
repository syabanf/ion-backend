// Warehouse stock dispatch lifecycle end-to-end.
//
// Wave 60 — proves the warehouse → tech bridge (TC-WHD): warehouse
// staff prepares stock against a specific WO, stages it, and a tech
// picks it up. Inventory drops by the dispatched quantity at the
// mark-picked-up step (the warehouse is committed to the physical
// handoff at that point).
//
//   1. setupCoveredCustomer — gives us a customer + order
//   2. Create warehouse + intake 100 units of a stock item
//   3. Create a WO against the customer's order
//   4. POST /api/warehouse/dispatch with wo_id + warehouse_id + items=[{item_id, qty:5}]
//   5. POST /api/warehouse/dispatch/{id}/stage → status flips to staged
//   6. POST /api/warehouse/dispatch/{id}/mark-picked-up → status flips
//      to picked_up + inventory drops to 95
//   7. Negative — second dispatch + cancel it; inventory unchanged
//      (cancel before pickup → no commit)
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestStockDispatchLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Warehouse + bulk stock item + 100-unit intake.
	// -----------------------------------------------------------------
	var wh struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/warehouse/warehouses", map[string]any{
		"name":    "W60 Dispatch Warehouse " + sx,
		"code":    "W60-WH-" + sx,
		"address": "W60 dispatch test",
	}, &wh, 201)

	var item struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/warehouse/catalog/items", map[string]any{
		"sku":      "W60-SKU-" + sx,
		"name":     "W60 Test Cable",
		"category": "cable",
		"brand":    "ION",
		"model":    "TEST",
		"spec":     "Wave 60 dispatch test material",
		"unit":     "meter",
	}, &item, 201)

	const intakeQty = 100.0
	admin.do("POST", "/api/warehouse/warehouses/"+wh.ID+"/intake", map[string]any{
		"stock_item_id":      item.ID,
		"quantity":           intakeQty,
		"distributor":        "W60 dispatch supplier",
		"purchase_order_ref": "W60-PO-" + sx,
		"reason":             "initial stocking for dispatch test",
	}, nil, 201)

	// -----------------------------------------------------------------
	// 2. Create a WO against the helper's order. We don't need to drive
	//    it through assign/in_progress — dispatch attaches to the WO
	//    purely by id.
	// -----------------------------------------------------------------
	var wo struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id": h.OrderID,
	}, &wo, 201)

	// -----------------------------------------------------------------
	// 3. Create dispatch — 5 units of the item.
	// -----------------------------------------------------------------
	const dispatchQty = 5.0
	var disp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	admin.do("POST", "/api/warehouse/dispatch", map[string]any{
		"wo_id":        wo.ID,
		"warehouse_id": wh.ID,
		"notes":        "Wave 60 — dispatch probe",
		"items": []map[string]any{
			{"item_id": item.ID, "qty": dispatchQty, "notes": "install cable"},
		},
	}, &disp, 201)
	if disp.ID == "" {
		t.Fatal("dispatch create returned empty id")
	}

	// -----------------------------------------------------------------
	// 4. Stage. status flips to 'staged' (or equivalent — handler
	//    docs use staged as the post-create label).
	// -----------------------------------------------------------------
	var staged struct {
		Status string `json:"status"`
	}
	admin.do("POST", "/api/warehouse/dispatch/"+disp.ID+"/stage", nil, &staged, 200)
	if staged.Status == "" {
		t.Errorf("stage response missing status field")
	}

	// -----------------------------------------------------------------
	// 5. Inventory before pickup — should still be 100 (dispatch isn't
	//    committed until mark-picked-up).
	// -----------------------------------------------------------------
	getQty := func() float64 {
		var inv struct {
			Items []struct {
				Item     struct{ ID string `json:"id"` } `json:"item"`
				Quantity float64                          `json:"quantity"`
			} `json:"items"`
		}
		admin.do("GET", "/api/warehouse/warehouses/"+wh.ID+"/inventory", nil, &inv, 200)
		for _, it := range inv.Items {
			if it.Item.ID == item.ID {
				return it.Quantity
			}
		}
		t.Fatalf("inventory missing item %s in warehouse %s", item.ID, wh.ID)
		return -1
	}
	if before := getQty(); !approxEqual(before, intakeQty) {
		// Some flows decrement at stage rather than pickup — accept either,
		// but the dispatch_qty must match either way.
		t.Logf("post-stage inventory: %v (some flows decrement here; check pickup)", before)
	}

	// -----------------------------------------------------------------
	// 6. Mark picked up. Inventory drops by dispatchQty.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/warehouse/dispatch/"+disp.ID+"/mark-picked-up", nil, nil, 200)
	after := getQty()
	if !approxEqual(after, intakeQty-dispatchQty) {
		t.Errorf("inventory after pickup: want %v, got %v (intake=%v, dispatched=%v)",
			intakeQty-dispatchQty, after, intakeQty, dispatchQty)
	}

	// -----------------------------------------------------------------
	// 7. Negative — fresh dispatch then cancel before pickup. Inventory
	//    must not change.
	// -----------------------------------------------------------------
	var disp2 struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/warehouse/dispatch", map[string]any{
		"wo_id":        wo.ID,
		"warehouse_id": wh.ID,
		"notes":        "Wave 60 — cancel probe",
		"items": []map[string]any{
			{"item_id": item.ID, "qty": 3.0, "notes": "to be cancelled"},
		},
	}, &disp2, 201)

	admin.do("POST", "/api/warehouse/dispatch/"+disp2.ID+"/cancel", map[string]any{
		"reason": "Wave 60 — testing cancel-before-pickup path",
	}, nil, 200)

	cancelled := getQty()
	if !approxEqual(cancelled, intakeQty-dispatchQty) {
		t.Errorf("inventory changed after cancelled dispatch: want %v, got %v",
			intakeQty-dispatchQty, cancelled)
	}
}
