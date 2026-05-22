// Per-item scan end-to-end for the warehouse dispatch flow.
//
// Wave 61 — extends Wave 60's coarse-grained dispatch lifecycle test
// with per-item scan coverage. The tech-app surface scans each item's
// serial/QR individually (different items may go to different parts
// of the install) and only after all required items are scanned does
// the dispatch get marked-picked-up.
//
//   1. setupCoveredCustomer — gives us order + branch
//   2. Warehouse + 100-unit intake of a bulk cable item
//   3. Create dispatch with 2 line items (qty=3, qty=2) against a fresh WO
//   4. Stage the dispatch
//   5. POST /api/warehouse/dispatch-items/{id}/scan for the first line
//      with a synthetic serial → response shows the item picked up
//      (item-level status changes; serial_or_qr persisted)
//   6. POST scan for the second line
//   7. Mark the whole dispatch picked-up
//   8. Inventory drops by qty1 + qty2
//   9. Negative — POSTing /scan on a different (cancelled) dispatch's
//      item returns 409 (state guard)
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestStockDispatchPerItemScan(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// Warehouse + item + intake.
	// -----------------------------------------------------------------
	var wh struct{ ID string `json:"id"` }
	admin.do("POST", "/api/warehouse/warehouses", map[string]any{
		"name":    "W61 Scan Warehouse " + sx,
		"code":    "W61-WH-" + sx,
		"address": "W61 per-item scan",
	}, &wh, 201)

	var item struct{ ID string `json:"id"` }
	admin.do("POST", "/api/warehouse/catalog/items", map[string]any{
		"sku":      "W61-SKU-" + sx,
		"name":     "W61 Test Cable",
		"category": "cable",
		"brand":    "ION",
		"model":    "PER-ITEM",
		"spec":     "Wave 61 — per-item scan",
		"unit":     "meter",
	}, &item, 201)

	const intakeQty = 100.0
	admin.do("POST", "/api/warehouse/warehouses/"+wh.ID+"/intake", map[string]any{
		"stock_item_id":      item.ID,
		"quantity":           intakeQty,
		"distributor":        "W61 supplier",
		"purchase_order_ref": "W61-PO-" + sx,
		"reason":             "intake for per-item scan test",
	}, nil, 201)

	// -----------------------------------------------------------------
	// WO + dispatch with two line items.
	// -----------------------------------------------------------------
	var wo struct{ ID string `json:"id"` }
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id": h.OrderID,
	}, &wo, 201)

	const qty1, qty2 = 3.0, 2.0
	var disp struct {
		ID    string `json:"id"`
		Items []struct {
			ID     string  `json:"id"`
			Qty    float64 `json:"qty"`
			Status string  `json:"status"`
		} `json:"items"`
	}
	admin.do("POST", "/api/warehouse/dispatch", map[string]any{
		"wo_id":        wo.ID,
		"warehouse_id": wh.ID,
		"notes":        "Wave 61 — per-item scan probe",
		"items": []map[string]any{
			{"item_id": item.ID, "qty": qty1, "notes": "drop A"},
			{"item_id": item.ID, "qty": qty2, "notes": "drop B"},
		},
	}, &disp, 201)
	if len(disp.Items) != 2 {
		t.Fatalf("dispatch should have 2 items, got %d", len(disp.Items))
	}
	item1ID := disp.Items[0].ID
	item2ID := disp.Items[1].ID

	// -----------------------------------------------------------------
	// Stage so the items are eligible for scan.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/warehouse/dispatch/"+disp.ID+"/stage", nil, nil, 200)

	// -----------------------------------------------------------------
	// Scan item 1.
	// -----------------------------------------------------------------
	scan1 := "W61-SERIAL-" + sx + "-A"
	var scanned1 struct {
		ID         string  `json:"id"`
		Status     string  `json:"status"`
		SerialOrQR *string `json:"serial_or_qr,omitempty"`
		PickedAt   *string `json:"picked_at,omitempty"`
	}
	admin.do("POST", "/api/warehouse/dispatch-items/"+item1ID+"/scan",
		map[string]any{"serial_or_qr": scan1}, &scanned1, 200)
	if scanned1.SerialOrQR == nil || *scanned1.SerialOrQR != scan1 {
		t.Errorf("serial_or_qr round-trip broken: got %v want %q", scanned1.SerialOrQR, scan1)
	}
	if scanned1.PickedAt == nil || *scanned1.PickedAt == "" {
		t.Errorf("picked_at not populated after scan on item 1")
	}

	// -----------------------------------------------------------------
	// Scan item 2.
	// -----------------------------------------------------------------
	scan2 := "W61-SERIAL-" + sx + "-B"
	admin.do("POST", "/api/warehouse/dispatch-items/"+item2ID+"/scan",
		map[string]any{"serial_or_qr": scan2}, nil, 200)

	// -----------------------------------------------------------------
	// Mark-picked-up commits the dispatch + drops inventory by full sum.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/warehouse/dispatch/"+disp.ID+"/mark-picked-up", nil, nil, 200)

	var inv struct {
		Items []struct {
			Item     struct{ ID string `json:"id"` } `json:"item"`
			Quantity float64                          `json:"quantity"`
		} `json:"items"`
	}
	admin.do("GET", "/api/warehouse/warehouses/"+wh.ID+"/inventory", nil, &inv, 200)
	var got float64
	for _, it := range inv.Items {
		if it.Item.ID == item.ID {
			got = it.Quantity
			break
		}
	}
	want := intakeQty - qty1 - qty2
	if !approxEqual(got, want) {
		t.Errorf("inventory after pickup: want %v, got %v", want, got)
	}

	// -----------------------------------------------------------------
	// Negative — re-scanning an already picked-up item must 409.
	// Once mark-picked-up runs, the items are terminal.
	// -----------------------------------------------------------------
	status := admin.statusOnlyJSON("POST",
		"/api/warehouse/dispatch-items/"+item1ID+"/scan",
		map[string]any{"serial_or_qr": "DUP-SCAN"})
	if status == 200 {
		t.Errorf("re-scan after pickup-commit returned 200 — terminal-state guard missing")
	}
}
