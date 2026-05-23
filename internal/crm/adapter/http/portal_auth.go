// Customer-portal authentication + data endpoints. Powers the
// customer_app (Flutter) and the public web portal.
//
// Auth shape:
//   - OTP-based, per PRD §10. Customer enters their customer_number
//     + phone last-4 (or email); we mint a 6-digit OTP, hash it, and
//     stash a row in crm.customer_portal_otp.
//   - On verify, we mint two tokens:
//       access_token  — short-lived JWT with role='customer'
//       refresh_token — opaque random string; its bcrypt hash lives
//                       in crm.customer_sessions
//
// Demo mode: when CRM_PORTAL_OTP_DEMO=true the OTP is returned in the
// request-otp response so a demoer doesn't need SMS/WhatsApp wired.
// Production cuts this to false and the WhatsApp gateway delivers it.
package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jung-kurt/gofpdf"
	"golang.org/x/crypto/bcrypt"

	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/notifyx"
	"github.com/ion-core/backend/pkg/ratelimit"
)

type PortalHandler struct {
	pool     *pgxpool.Pool
	verifier *auth.Verifier
	issuer   *auth.Issuer
	// notifier dispatches push notifications. Nil-safe — callers
	// guard with `if h.notifier != nil` so the handler still boots
	// in test rigs that don't wire one.
	notifier *notifyx.Dispatcher
	// Wave 83 (TC-RAD-013/014) — RADIUS profile refresh on addon
	// buy. Nil-safe; without it the handler keeps shipping addon
	// rows but the network adapter is never touched.
	radius port.RadiusGateway
}

func NewPortalHandler(pool *pgxpool.Pool, verifier *auth.Verifier, issuer *auth.Issuer) *PortalHandler {
	return &PortalHandler{pool: pool, verifier: verifier, issuer: issuer}
}

// WithNotifier injects the push-notification dispatcher. Pattern
// matches the rest of the codebase (`WithFinance`, `WithBOQ`, etc.) —
// optional dependency injected after construction.
func (h *PortalHandler) WithNotifier(n *notifyx.Dispatcher) *PortalHandler {
	h.notifier = n
	return h
}

// WithRadiusGateway injects the Wave 83 RADIUS profile-refresh hook.
// When set, buyAddon calls into it after the addon row lands so the
// network profile picks up the new bandwidth. Nil-safe.
func (h *PortalHandler) WithRadiusGateway(g port.RadiusGateway) *PortalHandler {
	h.radius = g
	return h
}

// Mount — surface the routes under /portal/* (the gateway proxies the
// whole /portal prefix to crm-svc).
func (h *PortalHandler) Mount(r chi.Router) {
	// Public — no auth. These power the self-order funnel for
	// prospective customers who don't yet have a portal session.
	// Each gets its own bucket (endpoint name + client IP) so
	// they don't share quota.
	otpRL := ratelimit.Middleware(h.pool, ratelimit.Config{
		Endpoint: "otp-request", Limit: 10, Window: time.Minute,
	})
	coverageRL := ratelimit.Middleware(h.pool, ratelimit.Config{
		Endpoint: "coverage-check", Limit: 60, Window: time.Minute,
	})
	productsRL := ratelimit.Middleware(h.pool, ratelimit.Config{
		Endpoint: "public-products", Limit: 120, Window: time.Minute,
	})
	selfOrderRL := ratelimit.Middleware(h.pool, ratelimit.Config{
		Endpoint: "self-order", Limit: 20, Window: time.Hour,
	})

	r.With(otpRL).Post("/portal/auth/otp-request", h.otpRequest)
	r.Post("/portal/auth/otp-verify", h.otpVerify)
	r.Post("/portal/auth/refresh", h.refresh)
	r.With(coverageRL).Post("/portal/public/coverage-check", h.publicCoverageCheck)
	r.With(selfOrderRL).Post("/portal/public/self-order", h.publicSelfOrder)
	r.With(productsRL).Get("/portal/public/products", h.publicListProducts)

	// Authenticated as customer.
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.Use(httpserver.RequirePermission("customer.portal.access"))

		r.Get("/portal/me", h.me)
		r.Get("/portal/services", h.services)
		r.Get("/portal/invoices", h.invoices)
		r.Get("/portal/invoices/{id}/pdf", h.invoicePDF)
		r.Get("/portal/invoices/{id}/faktur-pajak", h.invoiceFakturPajak)
		r.Post("/portal/invoices/{id}/pay", h.payInvoice)
		r.Get("/portal/addons", h.myAddons)
		r.Get("/portal/tickets", h.myTickets)
		r.Get("/portal/tickets/{id}", h.ticketDetail)
		r.Get("/portal/tickets/{id}/messages", h.ticketMessages)
		r.Post("/portal/tickets/{id}/messages", h.postTicketMessage)
		r.Post("/portal/tickets/{id}/csat", h.submitCSAT)
		r.Post("/portal/tickets", h.createTicket)
		r.Post("/portal/logout", h.logout)
		r.Post("/portal/device-token", h.registerCustomerDeviceToken)

		// Notifications inbox.
		r.Get("/portal/notifications", h.listNotifications)
		r.Post("/portal/notifications/{id}/read", h.markNotificationRead)
		r.Post("/portal/notifications/mark-all-read", h.markAllNotificationsRead)

		// Live tech tracking during the customer's active WO.
		r.Get("/portal/active-wo/tech-location", h.portalActiveWOTechLocation)

		// KTP re-upload (identity refresh).
		r.Post("/portal/ktp", h.portalKTPUpload)

		// Customer-initiated self-service flows. The token's UserID
		// slot carries the customer_id, so we don't need a path
		// param — every action is implicitly "for this customer".
		r.Get("/portal/products", h.listProducts)
		r.Get("/portal/addons-catalog", h.listAddonCatalog)
		r.Post("/portal/plan-change", h.requestPlanChange)
		r.Post("/portal/relocation", h.requestRelocation)
		r.Post("/portal/addons/buy", h.buyAddon)
		r.Post("/portal/termination", h.requestTermination)
	})
}

// =============================================================================
// OTP request / verify / refresh
// =============================================================================

type otpRequestInput struct {
	CustomerNumber string `json:"customer_number"` // public ref the customer knows
	PhoneLast4     string `json:"phone_last4,omitempty"`
	Email          string `json:"email,omitempty"`
}

func (h *PortalHandler) otpRequest(w http.ResponseWriter, r *http.Request) {
	var in otpRequestInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.CustomerNumber == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"customer_number is required"})
		return
	}
	// Lookup customer by their public number + matching phone last-4 or email.
	var customerID uuid.UUID
	var phone, email string
	q := `SELECT id, COALESCE(phone,''), COALESCE(email,'') FROM crm.customers WHERE customer_number = $1`
	if err := h.pool.QueryRow(r.Context(), q, in.CustomerNumber).Scan(&customerID, &phone, &email); err != nil {
		if err == pgx.ErrNoRows {
			// Don't leak which side mismatched; surface a generic error so
			// the form doesn't help an attacker enumerate accounts.
			writeJSON(w, http.StatusOK, map[string]any{"sent": true})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	matched := false
	if in.PhoneLast4 != "" && len(phone) >= 4 && strings.HasSuffix(phone, in.PhoneLast4) {
		matched = true
	}
	if in.Email != "" && strings.EqualFold(email, in.Email) {
		matched = true
	}
	if !matched {
		writeJSON(w, http.StatusOK, map[string]any{"sent": true})
		return
	}

	// Mint a fresh OTP. 6 digits, log-only in demo mode.
	otp, err := randomOTP6()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.customer_portal_otp
			(customer_id, purpose, otp_hash, expires_at)
		VALUES ($1, 'login', $2, NOW() + INTERVAL '10 minutes')
	`, customerID, string(hash)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{"sent": true}
	if os.Getenv("CRM_PORTAL_OTP_DEMO") == "true" {
		// Surface the OTP so demoers don't need SMS wired. Strip in prod.
		resp["debug_otp"] = otp
	}
	writeJSON(w, http.StatusOK, resp)
}

type otpVerifyInput struct {
	CustomerNumber string `json:"customer_number"`
	OTP            string `json:"otp"`
	Device         string `json:"device,omitempty"`
}

type tokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (h *PortalHandler) otpVerify(w http.ResponseWriter, r *http.Request) {
	var in otpVerifyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.OTP == "" || in.CustomerNumber == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"customer_number + otp are required"})
		return
	}
	// Pull the latest unverified OTP for this customer.
	var (
		otpRowID  uuid.UUID
		custID    uuid.UUID
		otpHash   string
		attempts  int
		expiresAt time.Time
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT o.id, o.customer_id, o.otp_hash, o.attempts, o.expires_at
		FROM crm.customer_portal_otp o
		JOIN crm.customers c ON c.id = o.customer_id
		WHERE c.customer_number = $1
		  AND o.purpose = 'login'
		  AND o.verified_at IS NULL
		  AND o.expires_at > NOW()
		ORDER BY o.created_at DESC
		LIMIT 1
	`, in.CustomerNumber).Scan(&otpRowID, &custID, &otpHash, &attempts, &expiresAt); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusUnauthorized, errMsg{"otp expired or not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if attempts >= 5 {
		writeErr(w, http.StatusTooManyRequests, errMsg{"too many attempts"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(otpHash), []byte(in.OTP)); err != nil {
		_, _ = h.pool.Exec(r.Context(),
			`UPDATE crm.customer_portal_otp SET attempts = attempts + 1 WHERE id = $1`, otpRowID)
		writeErr(w, http.StatusUnauthorized, errMsg{"otp does not match"})
		return
	}
	// Mark OTP verified.
	if _, err := h.pool.Exec(r.Context(),
		`UPDATE crm.customer_portal_otp SET verified_at = NOW() WHERE id = $1`, otpRowID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Issue tokens. UserID slot in Claims carries the customer_id.
	pair, err := h.issueTokens(r.Context(), custID, in.Device, r.Header.Get("User-Agent"), r.RemoteAddr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

type refreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *PortalHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var in refreshInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parts := strings.SplitN(in.RefreshToken, ".", 2)
	if len(parts) != 2 {
		writeErr(w, http.StatusUnauthorized, errMsg{"malformed refresh token"})
		return
	}
	sessionID, err := uuid.Parse(parts[0])
	if err != nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"malformed refresh token"})
		return
	}
	var (
		custID       uuid.UUID
		refreshHash  string
		expiresAt    time.Time
		revokedAt    *time.Time
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT customer_id, refresh_hash, expires_at, revoked_at
		FROM crm.customer_sessions WHERE id = $1
	`, sessionID).Scan(&custID, &refreshHash, &expiresAt, &revokedAt); err != nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"session not found"})
		return
	}
	if revokedAt != nil || time.Now().After(expiresAt) {
		writeErr(w, http.StatusUnauthorized, errMsg{"session revoked or expired"})
		return
	}
	// Validate the secret half.
	if err := bcrypt.CompareHashAndPassword([]byte(refreshHash), []byte(parts[1])); err != nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"refresh token mismatch"})
		return
	}
	// Touch the session + mint a fresh access token (keep the same refresh).
	_, _ = h.pool.Exec(r.Context(),
		`UPDATE crm.customer_sessions SET last_seen_at = NOW() WHERE id = $1`, sessionID)
	access, exp, err := h.signAccess(custID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access,
		"expires_at":   exp,
	})
}

func (h *PortalHandler) logout(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	_, _ = h.pool.Exec(r.Context(),
		`UPDATE crm.customer_sessions SET revoked_at = NOW() WHERE customer_id = $1 AND revoked_at IS NULL`,
		claims.UserID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Data endpoints — customer-scoped reads.
// =============================================================================

func (h *PortalHandler) me(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	row := h.pool.QueryRow(r.Context(), `
		SELECT id, customer_number, full_name, phone, COALESCE(email,''),
		       COALESCE(address,''), status, COALESCE(branch_id::text,'')
		FROM crm.customers WHERE id = $1
	`, claims.UserID)
	var id uuid.UUID
	var num, name, phone, email, addr, status, branchID string
	if err := row.Scan(&id, &num, &name, &phone, &email, &addr, &status, &branchID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              id.String(),
		"customer_number": num,
		"full_name":       name,
		"phone":           phone,
		"email":           email,
		"address":         addr,
		"status":          status,
		"branch_id":       branchID,
	})
}

func (h *PortalHandler) services(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	// Latest order = current plan.
	var (
		planCode, planName string
		speed              int
		monthly            float64
		hasPlan            bool
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT p.code, p.name, p.speed_mbps, p.monthly_price
		FROM crm.orders o
		JOIN crm.products p ON p.id = o.product_id
		WHERE o.customer_id = $1
		ORDER BY o.created_at DESC LIMIT 1
	`, claims.UserID).Scan(&planCode, &planName, &speed, &monthly); err == nil {
		hasPlan = true
	}
	// Addons.
	addonRows, err := h.pool.Query(r.Context(), `
		SELECT pa.code, pa.name, pa.addon_type, ca.status, ca.quantity,
		       ca.monthly_fee, ca.requested_at, ca.activated_at
		FROM crm.customer_addons ca
		JOIN crm.product_addons pa ON pa.id = ca.addon_id
		WHERE ca.customer_id = $1
		ORDER BY ca.requested_at DESC
	`, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer addonRows.Close()
	type addonRow struct {
		Code       string     `json:"code"`
		Name       string     `json:"name"`
		Type       string     `json:"addon_type"`
		Status     string     `json:"status"`
		Quantity   int        `json:"quantity"`
		MonthlyFee float64    `json:"monthly_fee"`
		Requested  time.Time  `json:"requested_at"`
		Activated  *time.Time `json:"activated_at,omitempty"`
	}
	addons := []addonRow{}
	for addonRows.Next() {
		var a addonRow
		if err := addonRows.Scan(&a.Code, &a.Name, &a.Type, &a.Status, &a.Quantity,
			&a.MonthlyFee, &a.Requested, &a.Activated); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		addons = append(addons, a)
	}
	// RADIUS session state — best-effort. The radius_sessions table is
	// the staging area for the network-svc's RADIUS reader. We don't
	// hard-fail when missing; the customer-app shows "Unknown" instead.
	var (
		radState   string = "unknown"
		lastSeenAt *time.Time
	)
	_ = h.pool.QueryRow(r.Context(), `
		SELECT state, last_seen_at
		FROM network.radius_sessions
		WHERE customer_id = $1
		ORDER BY last_seen_at DESC NULLS LAST LIMIT 1
	`, claims.UserID).Scan(&radState, &lastSeenAt)

	resp := map[string]any{
		"addons": addons,
		"radius": map[string]any{
			"state":        radState,
			"last_seen_at": lastSeenAt,
		},
	}
	if hasPlan {
		resp["plan"] = map[string]any{
			"code":       planCode,
			"name":       planName,
			"speed_mbps": speed,
			"monthly":    monthly,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// =============================================================================
// Ticket detail + timeline + CSAT
// =============================================================================

func (h *PortalHandler) ticketDetail(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var (
		ticketNo, cat, prio, status, summary, description string
		woID                                              *uuid.UUID
		csat                                              *int
		csatComment                                       *string
		createdAt                                         time.Time
		// Wave 20 — `sla_resolve_due` is TIMESTAMPTZ nullable per the
		// schema (0036_phase2_foundation.up.sql); scanning it into a
		// non-nullable `time.Time` blew up with 500 when policy never
		// set a target. Same for resolved_at. Pointer = NULL-tolerant.
		slaResolveDue *time.Time
		resolvedAt    *time.Time
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT ticket_number, category, priority, status, summary,
		       COALESCE(description,''), wo_id, csat_score, csat_comment,
		       created_at, sla_resolve_due, resolved_at
		FROM field.tickets WHERE id = $1 AND customer_id = $2
	`, id, claims.UserID).Scan(&ticketNo, &cat, &prio, &status, &summary,
		&description, &woID, &csat, &csatComment, &createdAt, &slaResolveDue, &resolvedAt); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusNotFound, errMsg{"ticket not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{
		"id":            id.String(),
		"ticket_number": ticketNo,
		"category":      cat,
		"priority":      prio,
		"status":        status,
		"summary":       summary,
		"description":   description,
		"created_at":    createdAt,
		"resolved_at":   resolvedAt,
	}
	if slaResolveDue != nil {
		resp["sla_resolve_due"] = *slaResolveDue
	}
	if woID != nil {
		resp["wo_id"] = woID.String()
	}
	if csat != nil {
		resp["csat_score"] = *csat
		resp["csat_comment"] = csatComment
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *PortalHandler) ticketMessages(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Verify the ticket is owned by the caller first.
	var ok bool
	if err := h.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM field.tickets WHERE id=$1 AND customer_id=$2)`,
		id, claims.UserID).Scan(&ok); err != nil || !ok {
		writeErr(w, http.StatusNotFound, errMsg{"ticket not found"})
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, author_kind, body, attachments, created_at
		FROM field.ticket_messages
		WHERE ticket_id = $1 AND is_internal_note = FALSE
		ORDER BY created_at ASC
	`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID          string          `json:"id"`
		AuthorKind  string          `json:"author_kind"`
		Body        string          `json:"body"`
		Attachments json.RawMessage `json:"attachments,omitempty"`
		CreatedAt   time.Time       `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.AuthorKind, &x.Body, &x.Attachments, &x.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type postMessageInput struct {
	Body        string   `json:"body"`
	Attachments []string `json:"attachments,omitempty"` // object_urls from /api/uploads/photos
}

func (h *PortalHandler) postTicketMessage(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in postMessageInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Body == "" && len(in.Attachments) == 0 {
		writeErr(w, http.StatusBadRequest, errMsg{"body or attachments required"})
		return
	}
	var ok bool
	if err := h.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM field.tickets WHERE id=$1 AND customer_id=$2)`,
		id, claims.UserID).Scan(&ok); err != nil || !ok {
		writeErr(w, http.StatusNotFound, errMsg{"ticket not found"})
		return
	}
	atts, _ := json.Marshal(in.Attachments)
	if string(atts) == "null" {
		atts = []byte("[]")
	}
	msgID := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.ticket_messages (id, ticket_id, author_kind, author_customer_id, body, attachments)
		VALUES ($1, $2, 'customer', $3, $4, $5::jsonb)
	`, msgID, id, claims.UserID, in.Body, atts); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = h.pool.Exec(r.Context(),
		`UPDATE field.tickets SET last_message_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
	writeJSON(w, http.StatusCreated, map[string]any{"id": msgID.String()})
}

type csatInput struct {
	Score   int    `json:"score"`
	Comment string `json:"comment,omitempty"`
}

func (h *PortalHandler) submitCSAT(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in csatInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Score < 1 || in.Score > 5 {
		writeErr(w, http.StatusBadRequest, errMsg{"score must be 1..5"})
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE field.tickets
		SET csat_score = $1, csat_comment = NULLIF($2,''), updated_at = NOW()
		WHERE id = $3 AND customer_id = $4 AND status IN ('resolved','closed')
	`, in.Score, in.Comment, id, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusConflict, errMsg{"ticket not resolved yet, or not yours"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "score": in.Score})
}

// invoicePDF renders a real PDF (gofpdf, single page). The layout
// puts the company header on top, invoice metadata in a two-column
// block, line items in a table, and totals + Faktur Pajak in a
// summary box at the bottom. `?type=faktur_pajak` returns the FP
// document layout instead of the standard invoice — both share the
// same header so the client can use one endpoint.
func (h *PortalHandler) invoicePDF(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	isFP := r.URL.Query().Get("type") == "faktur_pajak"

	var (
		number, invoiceType, status string
		subtotal, ppnAmount, total  float64
		invoiceDate, dueDate        time.Time
		fakturNo                    *string
		fakturIssuedAt              *time.Time
		customerName                string
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT i.invoice_number, i.invoice_type, i.status,
		       i.subtotal, i.ppn_amount, i.total,
		       i.invoice_date, i.due_date,
		       i.faktur_pajak_number, i.faktur_pajak_issued_at,
		       COALESCE(c.full_name, '')
		FROM billing.invoices i
		LEFT JOIN crm.customers c ON c.id = i.customer_id
		WHERE i.id = $1 AND i.customer_id = $2
	`, id, claims.UserID).Scan(&number, &invoiceType, &status,
		&subtotal, &ppnAmount, &total,
		&invoiceDate, &dueDate,
		&fakturNo, &fakturIssuedAt, &customerName); err != nil {
		writeErr(w, http.StatusNotFound, errMsg{"invoice not found"})
		return
	}

	if isFP && (fakturNo == nil || *fakturNo == "") {
		writeErr(w, http.StatusNotFound, errMsg{"Faktur Pajak not yet issued"})
		return
	}

	// Pull line items.
	type lineItem struct {
		Desc  string
		Qty   float64
		Unit  string
		Price float64
		Total float64
	}
	items := []lineItem{}
	rows, _ := h.pool.Query(r.Context(), `
		SELECT description, COALESCE(quantity, 1), COALESCE(unit, ''),
		       COALESCE(unit_price, 0), COALESCE(line_total, 0)
		FROM billing.invoice_items
		WHERE invoice_id = $1
		ORDER BY id
	`, id)
	if rows != nil {
		for rows.Next() {
			var li lineItem
			if err := rows.Scan(&li.Desc, &li.Qty, &li.Unit, &li.Price, &li.Total); err == nil {
				items = append(items, li)
			}
		}
		rows.Close()
	}

	pdf := newInvoicePDF()
	title := "INVOICE"
	if isFP {
		title = "FAKTUR PAJAK"
	}
	// Header band.
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(15, 23, 42)
	pdf.Cell(0, 10, "ION Network")
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(100, 116, 139)
	pdf.Cell(0, 5, "PT ION Network Indonesia — Jakarta")
	pdf.Ln(5)
	pdf.Cell(0, 5, "support@ion.local · ion.local")
	pdf.Ln(10)

	// Document title.
	pdf.SetFont("Helvetica", "B", 22)
	pdf.SetTextColor(14, 165, 233)
	pdf.Cell(0, 9, title)
	pdf.Ln(12)

	// Metadata two-column.
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Invoice #", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(60, 5, number, "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Customer", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(0, 5, customerName, "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Date", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(60, 5, invoiceDate.Format("02 Jan 2006"), "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Type", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(0, 5, invoiceType, "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Due", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(60, 5, dueDate.Format("02 Jan 2006"), "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 5, "Status", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(0, 5, strings.ToUpper(status), "", 1, "L", false, 0, "")

	if fakturNo != nil && *fakturNo != "" {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetTextColor(71, 85, 105)
		pdf.CellFormat(40, 5, "Faktur Pajak", "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(15, 23, 42)
		pdf.CellFormat(0, 5, *fakturNo, "", 1, "L", false, 0, "")
	}

	pdf.Ln(4)

	// Line items table.
	pdf.SetFillColor(241, 245, 249)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(95, 7, "Description", "", 0, "L", true, 0, "")
	pdf.CellFormat(20, 7, "Qty", "", 0, "R", true, 0, "")
	pdf.CellFormat(25, 7, "Unit", "", 0, "L", true, 0, "")
	pdf.CellFormat(40, 7, "Total (IDR)", "", 1, "R", true, 0, "")

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(15, 23, 42)
	if len(items) == 0 {
		pdf.CellFormat(0, 6, "(no line items)", "", 1, "L", false, 0, "")
	}
	for _, li := range items {
		pdf.CellFormat(95, 6, truncate(li.Desc, 60), "", 0, "L", false, 0, "")
		pdf.CellFormat(20, 6, fmt.Sprintf("%.0f", li.Qty), "", 0, "R", false, 0, "")
		pdf.CellFormat(25, 6, li.Unit, "", 0, "L", false, 0, "")
		pdf.CellFormat(40, 6, fmt.Sprintf("%.0f", li.Total), "", 1, "R", false, 0, "")
	}
	pdf.Ln(4)

	// Totals box.
	pdf.SetFillColor(248, 250, 252)
	pdf.Rect(115, pdf.GetY(), 80, 24, "F")
	pdf.SetX(120)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 6, "Subtotal", "", 0, "L", false, 0, "")
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(35, 6, fmt.Sprintf("%.0f", subtotal), "", 1, "R", false, 0, "")
	pdf.SetX(120)
	pdf.SetTextColor(71, 85, 105)
	pdf.CellFormat(40, 6, "PPN", "", 0, "L", false, 0, "")
	pdf.SetTextColor(15, 23, 42)
	pdf.CellFormat(35, 6, fmt.Sprintf("%.0f", ppnAmount), "", 1, "R", false, 0, "")
	pdf.SetX(120)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(14, 165, 233)
	pdf.CellFormat(40, 7, "TOTAL", "", 0, "L", false, 0, "")
	pdf.CellFormat(35, 7, fmt.Sprintf("%.0f", total), "", 1, "R", false, 0, "")
	pdf.Ln(8)

	// Footer.
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(148, 163, 184)
	if isFP {
		pdf.CellFormat(0, 4,
			"Faktur Pajak ini sesuai dengan ketentuan DJP. Pertanyaan: support@ion.local",
			"", 1, "L", false, 0, "")
	} else {
		pdf.CellFormat(0, 4,
			"Thank you for your business. Questions? support@ion.local · +62 21 1500 ION",
			"", 1, "L", false, 0, "")
	}

	w.Header().Set("Content-Type", "application/pdf")
	fname := number
	if isFP && fakturNo != nil {
		fname = *fakturNo
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.pdf"`, fname))
	w.WriteHeader(http.StatusOK)
	_ = pdf.Output(w)
}

// truncate clips a string to n runes + an ellipsis. Used so a long
// product description doesn't break the table layout.
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func (h *PortalHandler) invoices(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, invoice_number, invoice_type, status, total,
		       invoice_date, due_date, paid_at
		FROM billing.invoices
		WHERE customer_id = $1
		ORDER BY invoice_date DESC
		LIMIT 100
	`, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID         string     `json:"id"`
		Number     string     `json:"invoice_number"`
		Kind       string     `json:"kind"`
		Status     string     `json:"status"`
		Total      float64    `json:"total"`
		IssuedAt   time.Time  `json:"issued_at"`
		DueDate    *time.Time `json:"due_date,omitempty"`
		PaidAt     *time.Time `json:"paid_at,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Number, &x.Kind, &x.Status, &x.Total,
			&x.IssuedAt, &x.DueDate, &x.PaidAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *PortalHandler) myAddons(w http.ResponseWriter, r *http.Request) {
	// Convenience — same content as /services.addons but flat.
	h.services(w, r)
}

func (h *PortalHandler) myTickets(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, ticket_number, category, priority, status, summary,
		       created_at, resolved_at
		FROM field.tickets
		WHERE customer_id = $1
		ORDER BY created_at DESC
		LIMIT 50
	`, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID         string     `json:"id"`
		Number     string     `json:"ticket_number"`
		Category   string     `json:"category"`
		Priority   string     `json:"priority"`
		Status     string     `json:"status"`
		Summary    string     `json:"summary"`
		CreatedAt  time.Time  `json:"created_at"`
		ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Number, &x.Category, &x.Priority, &x.Status,
			&x.Summary, &x.CreatedAt, &x.ResolvedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type createTicketByCustomerInput struct {
	Category    string `json:"category"`
	Priority    string `json:"priority"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
}

func (h *PortalHandler) createTicket(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var in createTicketByCustomerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Summary == "" || in.Category == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"summary + category required"})
		return
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	id := uuid.New()
	ticketNo := fmt.Sprintf("TKT-%s-%s", time.Now().Format("20060102"), id.String()[:8])
	var responseHrs, resolveHrs int
	switch in.Priority {
	case "high":
		responseHrs, resolveHrs = 1, 4
	case "low":
		responseHrs, resolveHrs = 24, 72
	default:
		responseHrs, resolveHrs = 4, 24
	}
	now := time.Now()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.tickets (
			id, ticket_number, customer_id, category, priority, status,
			summary, description, sla_response_due, sla_resolve_due
		) VALUES ($1,$2,$3,$4,$5,'open',$6,NULLIF($7,''),$8,$9)
	`, id, ticketNo, claims.UserID, in.Category, in.Priority,
		in.Summary, in.Description,
		now.Add(time.Duration(responseHrs)*time.Hour),
		now.Add(time.Duration(resolveHrs)*time.Hour)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":            id.String(),
		"ticket_number": ticketNo,
		"status":        "open",
	})
}

// =============================================================================
// Customer-initiated self-service flows
// =============================================================================

func (h *PortalHandler) listProducts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, code, name, speed_mbps, monthly_price, otc_price
		FROM crm.products
		WHERE active = TRUE
		ORDER BY monthly_price ASC
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID         string  `json:"id"`
		Code       string  `json:"code"`
		Name       string  `json:"name"`
		Speed      int     `json:"speed_mbps"`
		Monthly    float64 `json:"monthly_price"`
		OTC        float64 `json:"otc_price"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.Speed, &x.Monthly, &x.OTC); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *PortalHandler) listAddonCatalog(w http.ResponseWriter, r *http.Request) {
	// Customer-side catalog = active addons only; identical shape to
	// the staff `GET /api/crm/addons` so the mobile client can reuse
	// the same DTO.
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, code, name, addon_type, one_time_fee, monthly_fee,
		       requires_install, COALESCE(description,'')
		FROM crm.product_addons
		WHERE active = TRUE
		ORDER BY addon_type, name
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID              string  `json:"id"`
		Code            string  `json:"code"`
		Name            string  `json:"name"`
		AddonType       string  `json:"addon_type"`
		OneTimeFee      float64 `json:"one_time_fee"`
		MonthlyFee      float64 `json:"monthly_fee"`
		RequiresInstall bool    `json:"requires_install"`
		Description     string  `json:"description,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.AddonType, &x.OneTimeFee,
			&x.MonthlyFee, &x.RequiresInstall, &x.Description); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type portalPlanChangeInput struct {
	ToProductID string `json:"to_product_id"`
	ChangeKind  string `json:"change_kind"`
	Reason      string `json:"reason,omitempty"`
}

func (h *PortalHandler) requestPlanChange(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var in portalPlanChangeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	toProductID, err := uuid.Parse(in.ToProductID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.ChangeKind != "upgrade" && in.ChangeKind != "downgrade" {
		writeErr(w, http.StatusBadRequest, errMsg{"change_kind must be upgrade or downgrade"})
		return
	}
	// Same shape as the staff endpoint — we just bypass it because
	// the customer JWT lacks crm.plan_change.create.
	var fromProductID uuid.UUID
	if err := h.pool.QueryRow(r.Context(), `
		SELECT product_id FROM crm.orders WHERE customer_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, claims.UserID).Scan(&fromProductID); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusBadRequest, errMsg{"no active plan on file"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.plan_change_requests (
			id, customer_id, from_product_id, to_product_id, change_kind,
			reason, status
		) VALUES ($1,$2,$3,$4,$5, NULLIF($6,''), 'pending')
	`, id, claims.UserID, fromProductID, toProductID, in.ChangeKind, in.Reason); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          id.String(),
		"status":      "pending",
		"change_kind": in.ChangeKind,
	})
}

type portalRelocationInput struct {
	ToAddress string   `json:"to_address"`
	ToGPSLat  *float64 `json:"to_gps_lat,omitempty"`
	ToGPSLng  *float64 `json:"to_gps_lng,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

func (h *PortalHandler) requestRelocation(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var in portalRelocationInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.ToAddress == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"to_address is required"})
		return
	}
	var fromAddress string
	if err := h.pool.QueryRow(r.Context(),
		`SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1`,
		claims.UserID).Scan(&fromAddress); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.customer_relocations (
			id, customer_id, from_address, to_address,
			to_gps_lat, to_gps_lng, status, survey_note
		) VALUES ($1,$2,$3,$4,$5,$6,'pending_survey', NULLIF($7,''))
	`, id, claims.UserID, fromAddress, in.ToAddress, in.ToGPSLat, in.ToGPSLng, in.Notes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          id.String(),
		"status":      "pending_survey",
		"to_address":  in.ToAddress,
	})
}

type portalBuyAddonInput struct {
	AddonID  string `json:"addon_id"`
	Quantity int    `json:"quantity,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

func (h *PortalHandler) buyAddon(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var in portalBuyAddonInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	addonID, err := uuid.Parse(in.AddonID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	// Snapshot catalog prices + requires_install flag.
	var (
		oneTime, monthly float64
		requiresInstall  bool
		custAddr         string
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT one_time_fee, monthly_fee, requires_install
		FROM crm.product_addons
		WHERE id = $1 AND active = TRUE
	`, addonID).Scan(&oneTime, &monthly, &requiresInstall); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusBadRequest, errMsg{"addon not found or inactive"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = h.pool.QueryRow(r.Context(),
		`SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1`,
		claims.UserID).Scan(&custAddr)

	id := uuid.New()
	status := "active"
	if requiresInstall {
		status = "pending_install"
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO crm.customer_addons (
			id, customer_id, addon_id, status, quantity,
			one_time_fee, monthly_fee, notes
		) VALUES ($1,$2,$3,$4,$5,$6,$7, NULLIF($8,''))
	`, id, claims.UserID, addonID, status, in.Quantity,
		oneTime*float64(in.Quantity), monthly*float64(in.Quantity), in.Notes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var installWoID *uuid.UUID
	if requiresInstall {
		woID := uuid.New()
		woNumber := fmt.Sprintf("WO-%s-%s", time.Now().Format("20060102"), woID.String()[:8])
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.work_orders (
				id, wo_number, customer_id, wo_type, product_type, address,
				status, priority
			) VALUES ($1, $2, $3, 'new_installation', 'addon', $4, 'unassigned', 'medium')
		`, woID, woNumber, claims.UserID,
			fallbackPortal(custAddr, "(address unknown)")); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := tx.Exec(ctx,
			`UPDATE crm.customer_addons SET install_wo_id = $1 WHERE id = $2`,
			woID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		installWoID = &woID
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Wave 83 (TC-RAD-013) — refresh the RADIUS profile so the new
	// bandwidth takes effect immediately. We only call this for
	// addons that don't require install (the ones that flip live on
	// purchase); install-required addons activate at BAST verify
	// where the provisioning path runs anyway. Nil-safe + best-
	// effort: a failure here doesn't undo the customer_addons write,
	// the next periodic sync will reconcile.
	if h.radius != nil && !requiresInstall {
		_ = h.radius.RefreshForCustomer(ctx, claims.UserID, "addon_buy")
	}
	resp := map[string]any{
		"id":               id.String(),
		"status":           status,
		"requires_install": requiresInstall,
	}
	if installWoID != nil {
		resp["install_wo_id"] = installWoID.String()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func fallbackPortal(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

type portalTerminationInput struct {
	Reason string `json:"reason"`
	Notes  string `json:"notes,omitempty"`
}

func (h *PortalHandler) requestTermination(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var in portalTerminationInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Reason == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"reason is required"})
		return
	}
	// MVP: open a CS ticket with category 'other' and category_meta
	// summary marking it as a termination request. CS Supervisor
	// owns the actual approval per PRD §10. Full integration with
	// the M7 voluntary-termination flow happens once finance signs
	// off on the policy.
	id := uuid.New()
	ticketNo := fmt.Sprintf("TKT-%s-%s", time.Now().Format("20060102"), id.String()[:8])
	now := time.Now()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.tickets (
			id, ticket_number, customer_id, category, priority, status,
			summary, description, sla_response_due, sla_resolve_due
		) VALUES ($1,$2,$3,'other','medium','open',$4,NULLIF($5,''),$6,$7)
	`, id, ticketNo, claims.UserID,
		fmt.Sprintf("TERMINATION REQUEST — %s", in.Reason),
		in.Notes,
		now.Add(4*time.Hour),
		now.Add(72*time.Hour)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":            id.String(),
		"ticket_number": ticketNo,
		"status":        "open",
		"note":          "A CS Supervisor will reach out within 4 hours.",
	})
}

// =============================================================================
// Helpers
// =============================================================================

func (h *PortalHandler) issueTokens(
	ctx context.Context,
	customerID uuid.UUID,
	device, ua, ip string,
) (tokenPair, error) {
	access, exp, err := h.signAccess(customerID)
	if err != nil {
		return tokenPair{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return tokenPair{}, err
	}
	secretStr := hex.EncodeToString(secret)
	hash, err := bcrypt.GenerateFromPassword([]byte(secretStr), bcrypt.DefaultCost)
	if err != nil {
		return tokenPair{}, err
	}
	sessionID := uuid.New()
	expRefresh := time.Now().Add(30 * 24 * time.Hour)
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO crm.customer_sessions
			(id, customer_id, refresh_hash, device_label, user_agent, ip_address, expires_at)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), $7)
	`, sessionID, customerID, string(hash), device, ua, ip, expRefresh); err != nil {
		return tokenPair{}, err
	}
	return tokenPair{
		AccessToken:  access,
		RefreshToken: fmt.Sprintf("%s.%s", sessionID.String(), secretStr),
		ExpiresAt:    exp,
	}, nil
}

// signAccess mints a customer-scoped JWT. We reuse the staff JWT
// Claims shape (UserID slot carries customer_id), with role=customer
// and a single permission so the existing RequirePermission middleware
// can gate the portal routes.
func (h *PortalHandler) signAccess(customerID uuid.UUID) (string, time.Time, error) {
	exp := time.Now().Add(15 * time.Minute)
	c := auth.Claims{
		UserID:      customerID,
		Email:       "",
		Roles:       []string{"customer"},
		Permissions: []string{"customer.portal.access"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   customerID.String(),
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok, err := h.issuer.Issue(c)
	return tok, exp, err
}

func randomOTP6() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// newInvoicePDF returns a fresh gofpdf instance configured for the
// invoice + Faktur Pajak layout: A4 portrait, 15 mm margins, single
// page, UTF-8 Helvetica. The font choice keeps the binary trivial
// (no external font assets needed).
func newInvoicePDF() *gofpdf.Fpdf {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 18)
	pdf.AddPage()
	return pdf
}

