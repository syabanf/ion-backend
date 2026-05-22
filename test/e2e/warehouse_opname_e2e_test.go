// Warehouse stock opname end-to-end.
//
// Wave 51 — proves the opname (stock-take) lifecycle from the Phase 1
// warehouse module:
//
//   1. Admin creates a warehouse + a stock item
//   2. Admin records an intake of 100 units (so expected_qty > 0)
//   3. Admin starts an opname session for the warehouse
//   4. Admin upserts a count of 95 units for the item (5-unit shrinkage)
//   5. Admin commits the session
//   6. After commit:
//        - session status flips to 'committed' + committed_at populated
//        - count row carries variance = -5
//        - inventory now reflects the counted_qty (the commit reconciles)
//   7. A second opname session against the same warehouse can be started
//      cleanly (no residual lock from the committed session)
//
// This is the canonical happy-path for the warehouse_staff role's most
// load-bearing operation. Cancellation + cable_remnant_decision branches
// are not covered here — they belong in dedicated specs as scope grows.
//
//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestWarehouseOpnameLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Create a warehouse — opname is scoped per warehouse.
	// -----------------------------------------------------------------
	var wh struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	admin.do("POST", "/api/warehouse/warehouses", map[string]any{
		"name":    "W51 Warehouse " + sx,
		"code":    "W51-WH-" + sx,
		"address": "W51 opname test",
		"notes":   "Wave 51 opname e2e",
	}, &wh, 201)
	if wh.ID == "" {
		t.Fatal("warehouse create returned empty id")
	}

	// -----------------------------------------------------------------
	// 2. Create a stock item — bulk (non-serialized) so intake takes a
	//    quantity rather than serial entries.
	// -----------------------------------------------------------------
	var item struct {
		ID         string `json:"id"`
		SKU        string `json:"sku"`
		Serialized bool   `json:"serialized"`
	}
	admin.do("POST", "/api/warehouse/catalog/items", map[string]any{
		"sku":      "W51-SKU-" + sx,
		"name":     "W51 Test Cable",
		"category": "cable",
		"brand":    "ION",
		"model":    "TEST",
		"spec":     "Wave 51 opname test material",
		"unit":     "meter",
	}, &item, 201)
	if item.ID == "" {
		t.Fatal("stock item create returned empty id")
	}

	// -----------------------------------------------------------------
	// 3. Intake — seed the warehouse with 100 units.
	// -----------------------------------------------------------------
	const intakeQty = 100.0
	admin.do("POST", "/api/warehouse/warehouses/"+wh.ID+"/intake", map[string]any{
		"stock_item_id":      item.ID,
		"quantity":           intakeQty,
		"distributor":        "W51 test distributor",
		"purchase_order_ref": "W51-PO-" + sx,
		"reason":             "initial stocking for opname test",
	}, nil, 201)

	// -----------------------------------------------------------------
	// 4. Start an opname session.
	// -----------------------------------------------------------------
	var session struct {
		ID            string `json:"id"`
		SessionNumber string `json:"session_number"`
		Status        string `json:"status"`
	}
	admin.do("POST", "/api/warehouse/opname/sessions", map[string]any{
		"warehouse_id": wh.ID,
		"notes":        "Wave 51 opname e2e test",
	}, &session, 201)
	if session.ID == "" {
		t.Fatal("opname start returned empty id")
	}
	if !strings.HasPrefix(session.Status, "open") && !strings.HasPrefix(session.Status, "in_progress") {
		t.Errorf("opname session status: want open/in_progress, got %q", session.Status)
	}

	// -----------------------------------------------------------------
	// 5. Upsert a count — 95 of expected 100 (5-unit shrinkage).
	// -----------------------------------------------------------------
	const counted = 95.0
	var count struct {
		ID          string  `json:"id"`
		ExpectedQty float64 `json:"expected_qty"`
		CountedQty  float64 `json:"counted_qty"`
		Variance    float64 `json:"variance"`
	}
	admin.do("PUT", "/api/warehouse/opname/sessions/"+session.ID+"/counts",
		map[string]any{
			"stock_item_id": item.ID,
			"counted_qty":   counted,
			"notes":         "5 units missing — under investigation",
		}, &count, 200)
	if !approxEqual(count.ExpectedQty, intakeQty) {
		t.Errorf("expected_qty: want %v, got %v", intakeQty, count.ExpectedQty)
	}
	if !approxEqual(count.CountedQty, counted) {
		t.Errorf("counted_qty: want %v, got %v", counted, count.CountedQty)
	}
	if !approxEqual(count.Variance, counted-intakeQty) {
		t.Errorf("variance: want %v, got %v", counted-intakeQty, count.Variance)
	}

	// -----------------------------------------------------------------
	// 6. Commit the session.
	// -----------------------------------------------------------------
	var committed struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		CommittedAt string `json:"committed_at"`
	}
	admin.do("POST", "/api/warehouse/opname/sessions/"+session.ID+"/commit",
		nil, &committed, 200)
	if committed.Status != "committed" {
		t.Errorf("post-commit status: want committed, got %q", committed.Status)
	}
	if committed.CommittedAt == "" {
		t.Errorf("committed_at not populated after commit")
	}

	// -----------------------------------------------------------------
	// 7. Verify inventory was reconciled — quantity drops to 95.
	// -----------------------------------------------------------------
	var inv struct {
		Items []struct {
			Item     struct{ ID string `json:"id"` } `json:"item"`
			Quantity float64                          `json:"quantity"`
		} `json:"items"`
	}
	admin.do("GET", "/api/warehouse/warehouses/"+wh.ID+"/inventory", nil, &inv, 200)
	var got *float64
	for i := range inv.Items {
		if inv.Items[i].Item.ID == item.ID {
			got = &inv.Items[i].Quantity
			break
		}
	}
	if got == nil {
		t.Fatalf("item %s missing from warehouse inventory after opname commit", item.ID)
	}
	if !approxEqual(*got, counted) {
		t.Errorf("inventory after commit: want %v, got %v (opname didn't reconcile)", counted, *got)
	}

	// -----------------------------------------------------------------
	// 8. A second session can be started cleanly.
	// -----------------------------------------------------------------
	var second struct{ ID string `json:"id"` }
	admin.do("POST", "/api/warehouse/opname/sessions", map[string]any{
		"warehouse_id": wh.ID,
		"notes":        "follow-up session",
	}, &second, 201)
	if second.ID == "" || second.ID == session.ID {
		t.Errorf("second opname session id invalid (%q vs first %q)", second.ID, session.ID)
	}
	// Tidy up — cancel the second so we don't leave it dangling.
	admin.do("POST", "/api/warehouse/opname/sessions/"+second.ID+"/cancel", nil, nil, 200)
}
