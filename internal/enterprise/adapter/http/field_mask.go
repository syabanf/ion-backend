package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Field-masking middleware — vendor isolation per CPQ §8 + NFR-011
// =====================================================================
//
// The middleware sits between RequireAuth and every BOQ-touching
// handler. It buffers the response, parses it as JSON, strips any
// commercial-secret fields when the actor is a vendor role, and
// writes the redacted body back to the client.
//
// Why middleware vs. per-handler masking:
//   - NFR-011 mandates server-side masking, not just UI hiding
//   - Contract tests sweep every endpoint asserting forbidden fields
//     are absent for vendor tokens (TC-RBAC-IV-008 / TC-NFR-011)
//   - Centralizing the mask makes future field additions safe — adding
//     a column doesn't require remembering to also redact it
//
// Masked fields for vendor role:
//   - boq.sell_total, boq.cost_total, boq.margin_pct
//   - boq.snapshot_hash (could leak commercial state via diff)
//   - boq_line.sell_unit_price, boq_line.line_discount_pct
//   - boq_line.base_price_snapshot (vendor doesn't need our reference price)
//   - boq_line.min_margin_snapshot, boq_line.max_discount_snapshot
//
// Vendor-VISIBLE fields:
//   - sku, name, unit, quantity, sla_template_id, status
//   - vendor_unit_cost (their own input)
//   - provider_company_id, provider_user_id, notes

// VendorMaskedBOQFields are the keys redacted from any object in a
// vendor-scoped response. Centralized for the contract test sweep
// (TC-RBAC-IV-008 / TC-NFR-011).
//
// Coverage spans every enterprise surface that quotes back commercial
// state — BOQ, Quotation, Negotiation, Invoice. The middleware does a
// recursive strip so the same list works whether the field appears at
// the root, inside `boq`, inside `round`, inside `invoice`, etc.
//
// We deliberately keep this list NARROW — every entry must be a name
// that uniquely identifies commercial state. Generic field names like
// `method` / `reference` / `issued_at` / `valid_from` would cause
// false-positive strips on unrelated objects (audit rows, user records,
// permission payloads) so we avoid them. Vendor RBAC permissions
// don't grant the negotiation / invoice / ewo endpoints in the first
// place; this middleware is defense-in-depth for misconfigured chains.
var VendorMaskedBOQFields = []string{
	// BOQ header (Phase 3)
	"sell_total",
	"cost_total",
	"margin_pct",
	"snapshot_hash",
	// BOQ line (Phase 3)
	"sell_unit_price",
	"line_discount_pct",
	"base_price_snapshot",
	"min_margin_snapshot",
	"max_discount_snapshot",

	// Negotiation (Phase 4b) — commercial signals carried on each round.
	"margin_floor_pct",
	"discount_ceiling_pct",
	"margin_before",
	"margin_after",
	"max_discount_after",
	"price_changes",
	"cco_auto_injected",
	"cco_injection_reason",

	// Invoice + payment (Phase 5) — money values. We strip the heavy
	// fields rather than stripping whole invoice objects so EWO
	// references that embed invoice_id still round-trip cleanly.
	"total_amount",
	"paid_amount",
	"balance",
}

// IsVendorActor returns true when the JWT claims indicate the actor
// is an internal-vendor user. We currently key off the `vendor_user`
// role tag — Phase 4 may move this to a dedicated `is_vendor` flag
// on the claims for cheaper lookup.
func IsVendorActor(ctx context.Context) bool {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == "vendor_user" || r == "internal_vendor" {
			return true
		}
	}
	return false
}

// BOQFieldMaskMiddleware wraps every BOQ-touching handler. It only
// performs the mask + parse work when the actor is a vendor — for
// sales/finance/admin it's effectively a passthrough.
func BOQFieldMaskMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cheap fast path — non-vendor actors don't pay the buffering cost.
		if !IsVendorActor(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}

		// Vendor actor — buffer the response so we can rewrite it.
		rw := &captureResponseWriter{
			ResponseWriter: w,
			buf:            &bytes.Buffer{},
			status:         http.StatusOK,
		}
		next.ServeHTTP(rw, r)

		// Only rewrite JSON 2xx responses. Errors + non-JSON pass through
		// as-is (no commercial fields to leak).
		if rw.status < 200 || rw.status >= 300 || rw.buf.Len() == 0 {
			rw.flush(w)
			return
		}
		ct := rw.Header().Get("Content-Type")
		if !isJSON(ct) {
			rw.flush(w)
			return
		}

		// Parse + strip masked fields, then write back.
		var raw any
		if err := json.Unmarshal(rw.buf.Bytes(), &raw); err != nil {
			// If unmarshal fails it's not our JSON — pass through.
			rw.flush(w)
			return
		}
		stripped := stripMaskedFields(raw, VendorMaskedBOQFields)
		buf, err := json.Marshal(stripped)
		if err != nil {
			rw.flush(w)
			return
		}
		// Reset Content-Length because we may have shrunk the body.
		rw.Header().Del("Content-Length")
		rw.ResponseWriter.WriteHeader(rw.status)
		_, _ = rw.ResponseWriter.Write(buf)
	})
}

// captureResponseWriter intercepts the body so we can rewrite it. We
// don't write to the inner ResponseWriter until we've decided whether
// to rewrite.
type captureResponseWriter struct {
	http.ResponseWriter
	buf         *bytes.Buffer
	status      int
	wroteHeader bool
}

func (c *captureResponseWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
}

func (c *captureResponseWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.status = http.StatusOK
		c.wroteHeader = true
	}
	return c.buf.Write(b)
}

func (c *captureResponseWriter) flush(w http.ResponseWriter) {
	if c.status != 0 {
		w.WriteHeader(c.status)
	}
	if c.buf.Len() > 0 {
		_, _ = w.Write(c.buf.Bytes())
	}
}

func isJSON(ct string) bool {
	// Lightweight check — handles `application/json` and
	// `application/json; charset=utf-8` without importing mime.
	for i, ch := range ct {
		if ch == ';' || ch == ' ' {
			return ct[:i] == "application/json"
		}
	}
	return ct == "application/json"
}

// stripMaskedFields walks a JSON tree and removes any object key
// matching the maskedKeys set. Recursive: works on nested objects +
// arrays. The set is small (≤10 keys) so linear scan is fine vs
// building a map per request.
func stripMaskedFields(v any, masked []string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if containsString(masked, k) {
				continue
			}
			out[k] = stripMaskedFields(val, masked)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripMaskedFields(val, masked)
		}
		return out
	default:
		return v
	}
}

func containsString(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
