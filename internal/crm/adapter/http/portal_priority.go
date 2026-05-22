// Public + customer-facing endpoints that close the
// "Suggested order of priority" gaps from docs/gap-analysis.md:
//
//   • Self-order funnel (public coverage check + public lead create)
//   • Payment integration (manual-transfer VA + Xendit-shaped intent)
//   • Faktur Pajak surface
//   • Push notifications (device-token registration)
//
// All handlers live on the same PortalHandler struct so they share
// the existing pool + verifier wiring. Public handlers are mounted
// outside RequireAuth in portal_auth.go; the customer-authenticated
// ones are mounted inside.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/notifyx"
)

// =============================================================================
// Public — coverage check (no auth)
//
// Mirrors network.coverageCheck's intent, but we re-implement the
// nearest-node lookup in pure SQL here so the portal-svc doesn't need
// to cross-call network-svc for an unauthenticated request. The math
// is the haversine formula approximated by an equirectangular
// projection — good enough for "are we close enough" decisions inside
// a single city.
// =============================================================================

type publicCoverageCheckReq struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type publicCoverageCheckResp struct {
	Verdict         string  `json:"verdict"` // covered | covered_with_excess | no_coverage
	NearestNodeID   string  `json:"nearest_node_id,omitempty"`
	NearestNodeName string  `json:"nearest_node_name,omitempty"`
	CableDistanceM  float64 `json:"cable_distance_m"`
	ExcessCharge    float64 `json:"excess_charge,omitempty"`
}

const (
	// Defaults if the platform_config row isn't populated.
	defaultMaxCableM     = 150.0
	defaultRouteFactor   = 1.25
	defaultExcessPerM    = 50000.0 // IDR per excess meter
	defaultCoverageRadius = 250.0  // hard cap before "no coverage"
)

func (h *PortalHandler) publicCoverageCheck(w http.ResponseWriter, r *http.Request) {
	var req publicCoverageCheckReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writePortalErr(w, http.StatusBadRequest, "coverage.bad_body", err.Error())
		return
	}
	if req.Lat == 0 || req.Lng == 0 {
		writePortalErr(w, http.StatusBadRequest, "coverage.missing_gps", "lat and lng are required")
		return
	}

	// Find the nearest active ODP/ODC using a bounding-box prefilter
	// + Haversine on the candidate set. Limiting to 200 nodes inside
	// a ~5 km box keeps the query trivial even with no PostGIS index.
	const dLatDeg = 0.045 // ~5 km
	dLngDeg := 0.045 / math.Cos(req.Lat*math.Pi/180)

	rows, err := h.pool.Query(r.Context(), `
		SELECT id, name, gps_lat, gps_lng, COALESCE(coverage_radius_m, 0)
		FROM network.nodes
		WHERE active = TRUE
		  AND status = 'active'
		  AND gps_lat IS NOT NULL AND gps_lng IS NOT NULL
		  AND gps_lat BETWEEN $1::float8 - $3::float8 AND $1::float8 + $3::float8
		  AND gps_lng BETWEEN $2::float8 - $4::float8 AND $2::float8 + $4::float8
		LIMIT 200
	`, req.Lat, req.Lng, dLatDeg, dLngDeg)
	if err != nil {
		writePortalErr(w, http.StatusInternalServerError, "coverage.query_failed", err.Error())
		return
	}
	defer rows.Close()

	type cand struct {
		id     uuid.UUID
		name   string
		dist   float64
		radius float64
	}
	var best *cand
	for rows.Next() {
		var c cand
		var lat, lng float64
		var radius int
		if err := rows.Scan(&c.id, &c.name, &lat, &lng, &radius); err != nil {
			continue
		}
		c.dist = haversineM(req.Lat, req.Lng, lat, lng)
		c.radius = float64(radius)
		if best == nil || c.dist < best.dist {
			x := c
			best = &x
		}
	}

	resp := publicCoverageCheckResp{Verdict: "no_coverage"}
	if best == nil {
		writePortalJSON(w, http.StatusOK, resp)
		return
	}
	cableM := best.dist * defaultRouteFactor
	resp.NearestNodeID = best.id.String()
	resp.NearestNodeName = best.name
	resp.CableDistanceM = math.Round(cableM)
	switch {
	case cableM <= defaultMaxCableM:
		resp.Verdict = "covered"
	case cableM <= defaultCoverageRadius:
		resp.Verdict = "covered_with_excess"
		resp.ExcessCharge = math.Round((cableM - defaultMaxCableM) * defaultExcessPerM)
	default:
		resp.Verdict = "no_coverage"
	}
	writePortalJSON(w, http.StatusOK, resp)
}

// haversineM returns the great-circle distance between two (lat,lng)
// pairs in meters. 6,371,000 m is Earth's mean radius.
func haversineM(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180.0 }
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * R * math.Asin(math.Sqrt(a))
}

// =============================================================================
// Public — list products for the self-order funnel (no auth)
// =============================================================================

func (h *PortalHandler) publicListProducts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, code, name, speed_mbps,
		       COALESCE(monthly_price, 0), COALESCE(otc_price, 0)
		FROM crm.products
		WHERE active = TRUE
		ORDER BY monthly_price ASC
	`)
	if err != nil {
		writePortalErr(w, http.StatusInternalServerError, "products.query_failed", err.Error())
		return
	}
	defer rows.Close()
	type p struct {
		ID           string  `json:"id"`
		Code         string  `json:"code"`
		Name         string  `json:"name"`
		SpeedMbps    int     `json:"speed_mbps"`
		MonthlyPrice float64 `json:"monthly_price"`
		OTCPrice     float64 `json:"otc_price"`
	}
	out := []p{}
	for rows.Next() {
		var x p
		if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.SpeedMbps, &x.MonthlyPrice, &x.OTCPrice); err == nil {
			out = append(out, x)
		}
	}
	writePortalJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Public — self-order lead create (no auth)
//
// Drops a row in crm.leads with source='self_order'. A sales rep picks
// it up from the leads board exactly like any other inbound lead.
// =============================================================================

type publicSelfOrderReq struct {
	FullName       string  `json:"full_name"`
	Phone          string  `json:"phone"`
	Email          string  `json:"email,omitempty"`
	Address        string  `json:"address"`
	GPSLat         float64 `json:"gps_lat,omitempty"`
	GPSLng         float64 `json:"gps_lng,omitempty"`
	ProductID      string  `json:"product_id,omitempty"`
	NearestNodeID  string  `json:"nearest_node_id,omitempty"`
	CableDistanceM float64 `json:"cable_distance_m,omitempty"`
	AcceptExcess   bool    `json:"accept_excess_cable,omitempty"`
	Notes          string  `json:"notes,omitempty"`
}

func (h *PortalHandler) publicSelfOrder(w http.ResponseWriter, r *http.Request) {
	var in publicSelfOrderReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writePortalErr(w, http.StatusBadRequest, "self_order.bad_body", err.Error())
		return
	}
	in.FullName = strings.TrimSpace(in.FullName)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Address = strings.TrimSpace(in.Address)
	if in.FullName == "" || in.Phone == "" || in.Address == "" {
		writePortalErr(w, http.StatusBadRequest, "self_order.missing_fields",
			"full_name, phone, and address are required")
		return
	}

	// Light-weight rate limit: refuse >5 self-orders from the same
	// phone in 24h. Cheap because crm.leads has a phone index.
	var recent int
	_ = h.pool.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM crm.leads
		WHERE phone = $1 AND created_at > NOW() - INTERVAL '24 hours'
	`, in.Phone).Scan(&recent)
	if recent >= 5 {
		writePortalErr(w, http.StatusTooManyRequests, "self_order.rate_limited",
			"too many self-order attempts for this phone in 24h")
		return
	}

	id := uuid.New()
	leadNo := fmt.Sprintf("LD-%s-%s", time.Now().Format("20060102"), id.String()[:8])

	var productID *uuid.UUID
	if in.ProductID != "" {
		if p, err := uuid.Parse(in.ProductID); err == nil {
			productID = &p
		}
	}
	var nodeID *uuid.UUID
	if in.NearestNodeID != "" {
		if n, err := uuid.Parse(in.NearestNodeID); err == nil {
			nodeID = &n
		}
	}
	var gpsLat, gpsLng *float64
	if in.GPSLat != 0 {
		gpsLat = &in.GPSLat
	}
	if in.GPSLng != 0 {
		gpsLng = &in.GPSLng
	}

	// Auto-route: pick a sales rep round-robin within the nearest
	// node's branch (or any sales rep if we don't have a node). The
	// lookup + the routing pointer update happen in one tx so two
	// concurrent self-orders don't both pick the same rep.
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writePortalErr(w, http.StatusInternalServerError, "self_order.begin_tx", err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var branchID *uuid.UUID
	var salesID *uuid.UUID
	if nodeID != nil {
		var b uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT branch_id FROM network.nodes WHERE id = $1`,
			*nodeID).Scan(&b); err == nil {
			branchID = &b
		}
	}
	salesID = pickRoundRobinSales(ctx, tx, branchID)

	if _, err := tx.Exec(ctx, `
		INSERT INTO crm.leads (
			id, lead_number, full_name, phone, email, address,
			gps_lat, gps_lng, product_id, status, source,
			accept_excess_cable, nearest_node_id, cable_distance_m,
			notes, branch_id, sales_id
		) VALUES (
			$1, $2, $3, $4, NULLIF($5,''), $6,
			$7, $8, $9, 'new', 'self_order',
			$10, $11, NULLIF($12,0)::numeric(10,2),
			NULLIF($13,''), $14, $15
		)
	`, id, leadNo, in.FullName, in.Phone, in.Email, in.Address,
		gpsLat, gpsLng, productID,
		in.AcceptExcess, nodeID, in.CableDistanceM,
		in.Notes, branchID, salesID); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "self_order.insert_failed", err.Error())
		return
	}

	// Initial timeline event so the rep sees provenance immediately.
	_, _ = tx.Exec(ctx, `
		INSERT INTO crm.lead_events (lead_id, kind, summary, data)
		VALUES ($1, 'created', $2, $3::jsonb)
	`, id, "Self-order from public funnel",
		fmt.Sprintf(`{"source":"self_order","phone":"%s"}`, in.Phone))

	// Coverage-check provenance — if the funnel ran the public coverage
	// check before submit, the lead carries node + distance + excess
	// flag. We re-derive the verdict here (same thresholds as
	// publicCoverageCheck) and write it as a `coverage_checked` event
	// so the timeline shows WHY this lead was accepted.
	if in.NearestNodeID != "" || in.CableDistanceM > 0 {
		// The switch is exhaustive (covers zero / inside-window /
		// outside-window) so verdict is always assigned. No initial.
		var verdict string
		switch {
		case in.CableDistanceM == 0:
			verdict = "no_coverage"
		case in.CableDistanceM <= defaultMaxCableM:
			verdict = "covered"
		default:
			// Outside the free window — either accepted excess or
			// surfaced excess; both land as covered_with_excess in
			// the timeline (the accept_excess flag is the JSON
			// payload's source of truth).
			verdict = "covered_with_excess"
		}
		summary := fmt.Sprintf(
			"Coverage check: %s (≈%.0f m to %s)",
			verdict, in.CableDistanceM, in.NearestNodeID,
		)
		dataJSON := fmt.Sprintf(
			`{"verdict":"%s","cable_distance_m":%.0f,"nearest_node_id":"%s","accept_excess":%t}`,
			verdict, in.CableDistanceM, in.NearestNodeID, in.AcceptExcess,
		)
		_, _ = tx.Exec(ctx, `
			INSERT INTO crm.lead_events (lead_id, kind, summary, data)
			VALUES ($1, 'coverage_checked', $2, $3::jsonb)
		`, id, summary, dataJSON)
	}

	if err := tx.Commit(ctx); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "self_order.commit", err.Error())
		return
	}

	// In-app notification to the assigned rep (persisted inbox row) +
	// push fan-out via notifyx (no-op until FCM provider lands).
	if salesID != nil {
		_, _ = h.pool.Exec(r.Context(), `
			INSERT INTO enterprise.notifications
				(user_id, kind, title, body, deep_link, data)
			VALUES ($1, 'self_order_assigned',
			        $2,
			        'Tap to open the lead.',
			        $3,
			        $4::jsonb)
			ON CONFLICT DO NOTHING
		`, *salesID,
			"New self-order lead — "+leadNo,
			"/crm/leads/"+id.String(),
			fmt.Sprintf(`{"lead_id":"%s","lead_number":"%s"}`, id.String(), leadNo))

		// notifyx.Dispatcher fans the push out to every device token
		// registered for this user. Stub provider logs only; FCM
		// adapter swaps in via main.go (`WithProvider`).
		if h.notifier != nil {
			h.notifier.Send(r.Context(),
				notifyx.Target{UserID: *salesID},
				notifyx.Message{
					Title:    "New self-order lead — " + leadNo,
					Body:     "Tap to open the lead.",
					DeepLink: "/crm/leads/" + id.String(),
					Topic:    "self_order_assigned",
					Data: map[string]string{
						"lead_id":     id.String(),
						"lead_number": leadNo,
					},
				})
		}
	}

	writePortalJSON(w, http.StatusCreated, map[string]any{
		"id":          id.String(),
		"lead_number": leadNo,
		"status":      "new",
		"assigned":    salesID != nil,
		"next_steps":  "A sales representative will contact you within 1 business day to confirm your order.",
	})
}

// pickRoundRobinSales picks the next sales_rep for a branch, updates
// the routing pointer, and returns the chosen id (nil if no rep can
// be found). Caller passes the transaction so the pointer-update is
// atomic with the lead insert.
//
// Strategy: prefer reps in the same branch; if the branch has none,
// fall back to any sales_rep. The "next" rep is the one whose id
// sorts immediately after the routing pointer.
func pickRoundRobinSales(ctx context.Context, tx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, branchID *uuid.UUID) *uuid.UUID {
	var last *uuid.UUID
	if branchID != nil {
		var p uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT last_sales_id FROM crm.sales_routing_config WHERE branch_id=$1`,
			*branchID).Scan(&p); err == nil {
			last = &p
		}
	}

	// Pick a rep — branch-bound first, fallback to any.
	var next uuid.UUID
	var err error
	if branchID != nil {
		err = tx.QueryRow(ctx, `
			SELECT u.id
			FROM identity.users u
			JOIN identity.user_roles ur ON ur.user_id = u.id
			JOIN identity.roles r ON r.id = ur.role_id
			WHERE r.name = 'sales_rep'
			  AND u.active = TRUE
			  AND u.branch_id = $1
			  AND ($2::uuid IS NULL OR u.id > $2::uuid)
			ORDER BY u.id ASC
			LIMIT 1
		`, *branchID, last).Scan(&next)
		// Wrap-around: nobody after `last`, start from the top.
		if err != nil {
			err = tx.QueryRow(ctx, `
				SELECT u.id
				FROM identity.users u
				JOIN identity.user_roles ur ON ur.user_id = u.id
				JOIN identity.roles r ON r.id = ur.role_id
				WHERE r.name = 'sales_rep' AND u.active = TRUE AND u.branch_id = $1
				ORDER BY u.id ASC LIMIT 1
			`, *branchID).Scan(&next)
		}
	}
	// Branch-less fallback.
	if err != nil || next == uuid.Nil {
		_ = tx.QueryRow(ctx, `
			SELECT u.id
			FROM identity.users u
			JOIN identity.user_roles ur ON ur.user_id = u.id
			JOIN identity.roles r ON r.id = ur.role_id
			WHERE r.name = 'sales_rep' AND u.active = TRUE
			ORDER BY u.id ASC LIMIT 1
		`).Scan(&next)
	}
	if next == uuid.Nil {
		return nil
	}

	// Update the routing pointer for this branch.
	if branchID != nil {
		_, _ = tx.Exec(ctx, `
			INSERT INTO crm.sales_routing_config (branch_id, last_sales_id)
			VALUES ($1, $2)
			ON CONFLICT (branch_id) DO UPDATE
				SET last_sales_id = EXCLUDED.last_sales_id,
				    updated_at = NOW()
		`, *branchID, next)
	}
	return &next
}

// =============================================================================
// Payment intent — customer-initiated checkout
//
// We're "Xendit-shaped" — the JSON payload looks like a Xendit invoice
// so the customer app + admin UI can wire to the real provider later
// with no schema changes. Today we mint a fake VA number and a
// checkout_url that points at a stub page; in prod the gateway adapter
// fills both fields from the Xendit API response.
// =============================================================================

type payInvoiceReq struct {
	Method string `json:"method"` // xendit_va | manual_transfer | xendit_qris
	Bank   string `json:"bank,omitempty"`
}

func (h *PortalHandler) payInvoice(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	invoiceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writePortalErr(w, http.StatusBadRequest, "pay.bad_id", err.Error())
		return
	}
	var in payInvoiceReq
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Method == "" {
		in.Method = "xendit_va"
	}
	if in.Bank == "" {
		in.Bank = "BCA"
	}

	// Refuse pay for someone else's invoice.
	var custID uuid.UUID
	var amount float64
	var status string
	if err := h.pool.QueryRow(r.Context(), `
		SELECT customer_id, total, status
		FROM billing.invoices WHERE id = $1
	`, invoiceID).Scan(&custID, &amount, &status); err != nil {
		if err == pgx.ErrNoRows {
			writePortalErr(w, http.StatusNotFound, "pay.invoice_not_found", "invoice not found")
			return
		}
		writePortalErr(w, http.StatusInternalServerError, "pay.lookup_failed", err.Error())
		return
	}
	if custID != claims.UserID {
		writePortalErr(w, http.StatusForbidden, "pay.not_yours", "invoice does not belong to this customer")
		return
	}
	if status == "paid" {
		writePortalErr(w, http.StatusConflict, "pay.already_paid", "invoice already paid")
		return
	}

	// Mint a deterministic-looking VA number. Real provider replaces
	// this; for now it's the invoice id's hex truncated to 16 digits.
	va := mockVANumber(invoiceID)
	checkoutURL := fmt.Sprintf("https://pay-stub.ion.local/checkout/%s", invoiceID.String())
	expiresAt := time.Now().Add(24 * time.Hour)

	intentID := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO billing.payment_intents (
			id, invoice_id, customer_id, method, amount,
			gateway_ref, checkout_url, va_number, va_bank_code,
			expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, intentID, invoiceID, custID, in.Method, amount,
		fmt.Sprintf("stub_%s", intentID.String()[:8]),
		checkoutURL, va, strings.ToUpper(in.Bank),
		expiresAt); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "pay.intent_create_failed", err.Error())
		return
	}

	// Cache the latest checkout url on the invoice for convenience.
	_, _ = h.pool.Exec(r.Context(), `
		UPDATE billing.invoices
		SET checkout_url = $1, va_number = $2, va_bank_code = $3,
		    payment_due_at = $4, updated_at = NOW()
		WHERE id = $5
	`, checkoutURL, va, strings.ToUpper(in.Bank), expiresAt, invoiceID)

	writePortalJSON(w, http.StatusCreated, map[string]any{
		"intent_id":   intentID.String(),
		"method":      in.Method,
		"amount":      amount,
		"va_number":   va,
		"bank":        strings.ToUpper(in.Bank),
		"checkout_url": checkoutURL,
		"expires_at":  expiresAt.Format(time.RFC3339),
		"status":      "pending",
	})
}

func mockVANumber(id uuid.UUID) string {
	// 16-char numeric-ish string. Real VA format depends on bank
	// (BCA 10-digit, BNI 8-digit company + 8-digit suffix, etc.) —
	// good enough for the stub.
	b := id[:]
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		out[i] = '0' + (b[i%len(b)] % 10)
	}
	return string(out)
}

// =============================================================================
// Faktur Pajak surface
//
// /portal/invoices/{id}/faktur-pajak returns the FP number + a stub
// download link. The actual DJP e-Faktur PDF generation happens in
// billing-svc (out of scope for this turn — we just expose the data).
// =============================================================================

func (h *PortalHandler) invoiceFakturPajak(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writePortalErr(w, http.StatusBadRequest, "fp.bad_id", err.Error())
		return
	}
	var (
		number   *string
		issuedAt *time.Time
		custID   uuid.UUID
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT customer_id, faktur_pajak_number, faktur_pajak_issued_at
		FROM billing.invoices WHERE id = $1
	`, id).Scan(&custID, &number, &issuedAt); err != nil {
		if err == pgx.ErrNoRows {
			writePortalErr(w, http.StatusNotFound, "fp.invoice_not_found", "invoice not found")
			return
		}
		writePortalErr(w, http.StatusInternalServerError, "fp.lookup_failed", err.Error())
		return
	}
	if custID != claims.UserID {
		writePortalErr(w, http.StatusForbidden, "fp.not_yours", "invoice does not belong to this customer")
		return
	}
	if number == nil {
		writePortalJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"message":   "Faktur Pajak not yet issued for this invoice",
		})
		return
	}
	writePortalJSON(w, http.StatusOK, map[string]any{
		"available":    true,
		"number":       *number,
		"issued_at":    issuedAt,
		"download_url": fmt.Sprintf("/portal/invoices/%s/pdf?type=faktur_pajak", id.String()),
	})
}

// =============================================================================
// Device-token registration — customer side
//
// The matching staff-side endpoint lives in pkg/notifyx (registered
// by the api-gateway under /api/identity/device-token). Both rows
// land in platform.device_tokens; the dispatcher fans out by user
// vs customer id.
// =============================================================================

type registerCustomerTokenReq struct {
	Token    string `json:"token"`
	Platform string `json:"platform"` // ios | android | web
}

func (h *PortalHandler) registerCustomerDeviceToken(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	var in registerCustomerTokenReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writePortalErr(w, http.StatusBadRequest, "device.bad_body", err.Error())
		return
	}
	if in.Token == "" || in.Platform == "" {
		writePortalErr(w, http.StatusBadRequest, "device.missing_fields", "token and platform are required")
		return
	}
	if in.Platform != "ios" && in.Platform != "android" && in.Platform != "web" {
		writePortalErr(w, http.StatusBadRequest, "device.bad_platform", "platform must be ios|android|web")
		return
	}
	// UPSERT on token: a phone re-installing the app shouldn't create
	// a duplicate row, just refresh ownership + last_seen_at.
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO platform.device_tokens
			(customer_id, token, platform, app, last_seen_at)
		VALUES ($1, $2, $3, 'customer', NOW())
		ON CONFLICT (token) DO UPDATE
			SET customer_id = EXCLUDED.customer_id,
			    user_id = NULL,
			    platform = EXCLUDED.platform,
			    app = EXCLUDED.app,
			    last_seen_at = NOW()
	`, claims.UserID, in.Token, in.Platform); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "device.upsert_failed", err.Error())
		return
	}
	writePortalJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// =============================================================================
// Helpers — match the existing portal_auth.go writePortal* conventions.
// =============================================================================

func writePortalJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writePortalErr(w http.ResponseWriter, status int, code, msg string) {
	writePortalJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
