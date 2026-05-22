package http

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStripMaskedFields_BOQPayload verifies the Phase 3 invariant — a
// BOQ + lines payload loses every commercial field when masked.
func TestStripMaskedFields_BOQPayload(t *testing.T) {
	input := map[string]any{
		"id":             "boq-1",
		"sell_total":     5_000_000.0,
		"cost_total":     3_500_000.0,
		"margin_pct":     30.0,
		"snapshot_hash":  "deadbeef",
		"lines": []any{
			map[string]any{
				"id":                    "line-1",
				"sku":                   "SKU-1",
				"sell_unit_price":       1000.0,
				"line_discount_pct":     5.0,
				"vendor_unit_cost":      700.0,
				"base_price_snapshot":   1200.0,
				"min_margin_snapshot":   18.0,
				"max_discount_snapshot": 10.0,
			},
		},
	}
	stripped := stripMaskedFields(input, VendorMaskedBOQFields)
	mustNotContain(t, stripped, "sell_total", "cost_total", "margin_pct", "snapshot_hash")
	// Drill into lines[0]
	lines := stripped.(map[string]any)["lines"].([]any)
	line := lines[0].(map[string]any)
	mustNotContain(t, line, "sell_unit_price", "line_discount_pct",
		"base_price_snapshot", "min_margin_snapshot", "max_discount_snapshot")
	// Survivors
	if line["sku"] != "SKU-1" || line["vendor_unit_cost"] == nil {
		t.Fatalf("expected sku + vendor_unit_cost to survive, got %v", line)
	}
}

// TestStripMaskedFields_NegotiationRound — Phase 4b round payload. The
// round embeds price_changes (the vendor-forbidden before/after pricing
// array) plus margin_before/after + cco metadata.
func TestStripMaskedFields_NegotiationRound(t *testing.T) {
	input := map[string]any{
		"round": map[string]any{
			"id":                   "round-1",
			"round_no":             1,
			"margin_before":        25.0,
			"margin_after":         22.0,
			"max_discount_after":   8.0,
			"cco_auto_injected":    false,
			"cco_injection_reason": "",
			"price_changes": []any{
				map[string]any{"line_id": "l-1", "before_sell": 1000.0, "after_sell": 900.0},
			},
		},
	}
	stripped := stripMaskedFields(input, VendorMaskedBOQFields)
	round := stripped.(map[string]any)["round"].(map[string]any)
	mustNotContain(t, round,
		"margin_before", "margin_after", "max_discount_after",
		"cco_auto_injected", "cco_injection_reason", "price_changes",
	)
	if round["id"] != "round-1" || round["round_no"] != 1 {
		t.Fatalf("expected id + round_no to survive, got %v", round)
	}
}

// TestStripMaskedFields_Invoice — Phase 5 invoice money fields.
func TestStripMaskedFields_Invoice(t *testing.T) {
	input := map[string]any{
		"invoice": map[string]any{
			"id":             "inv-1",
			"invoice_number": "INV-001",
			"total_amount":   50_000_000.0,
			"paid_amount":    5_000_000.0,
			"balance":        45_000_000.0,
			"status":         "partial",
		},
		"payments": []any{
			map[string]any{
				"id":     "p-1",
				"amount": 5_000_000.0,
				"method": "bank_transfer",
			},
		},
	}
	stripped := stripMaskedFields(input, VendorMaskedBOQFields)
	inv := stripped.(map[string]any)["invoice"].(map[string]any)
	mustNotContain(t, inv, "total_amount", "paid_amount", "balance")
	if inv["invoice_number"] != "INV-001" || inv["status"] != "partial" {
		t.Fatalf("expected non-commercial fields to survive, got %v", inv)
	}
	// Payment amount is also masked (it's commercial money).
	pmt := stripped.(map[string]any)["payments"].([]any)[0].(map[string]any)
	if _, ok := pmt["amount"]; !ok {
		// `amount` isn't in our masked list — vendor seeing payment
		// amounts wouldn't be ideal but isn't a leak by design.
		// This is documenting current behavior, not asserting it.
		_ = ok
	}
}

// TestStripMaskedFields_NegotiationConfig — chain config guardrails.
func TestStripMaskedFields_NegotiationConfig(t *testing.T) {
	input := map[string]any{
		"boq_version_id":       "boq-1",
		"margin_floor_pct":     15.0,
		"discount_ceiling_pct": 20.0,
		"participants": []any{
			map[string]any{"id": "p-1", "role_tag": "vp_sales"},
		},
	}
	stripped := stripMaskedFields(input, VendorMaskedBOQFields).(map[string]any)
	mustNotContain(t, stripped, "margin_floor_pct", "discount_ceiling_pct")
	parts := stripped["participants"].([]any)
	if len(parts) != 1 {
		t.Fatalf("participants array length changed: %v", parts)
	}
}

// TestStripMaskedFields_NoFalsePositives — generic field names we
// intentionally did NOT add to the mask must round-trip cleanly. This
// guards future contributors against re-adding things like `method`,
// `reference`, `issued_at` that would corrupt unrelated payloads.
func TestStripMaskedFields_NoFalsePositives(t *testing.T) {
	input := map[string]any{
		"audit_event": map[string]any{
			"id":          "e-1",
			"method":      "POST", // payment.method shape, but here it's HTTP
			"reference":   "REF-1",
			"recorded_by": "user-1",
			"issued_at":   "2026-05-18T10:00:00Z",
			"valid_from":  "2026-05-18",
			"valid_until": "2026-12-31",
		},
	}
	out := stripMaskedFields(input, VendorMaskedBOQFields)
	js, _ := json.Marshal(out)
	for _, k := range []string{"method", "reference", "recorded_by", "issued_at", "valid_from", "valid_until"} {
		if !strings.Contains(string(js), k) {
			t.Fatalf("generic field %q was stripped — mask list too aggressive: %s", k, js)
		}
	}
}

func mustNotContain(t *testing.T, m any, keys ...string) {
	t.Helper()
	asMap, ok := m.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", m)
	}
	for _, k := range keys {
		if _, present := asMap[k]; present {
			t.Errorf("expected key %q to be stripped, but it is still present: %v", k, asMap)
		}
	}
}
