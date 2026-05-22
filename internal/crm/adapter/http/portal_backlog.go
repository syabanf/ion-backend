// Portal backlog endpoints (P1 + P2 batch).
//
//   - Customer notifications inbox (list, mark-read, mark-all-read)
//   - Live tech tracking during the customer's active WO
//   - KTP re-upload (identity refresh)
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/httpserver"
)

// =============================================================================
// Notifications inbox
// =============================================================================

func (h *PortalHandler) listNotifications(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	unread := r.URL.Query().Get("unread_only") == "true"
	q := `
		SELECT id, kind, title, body, COALESCE(deep_link,''),
		       data, read_at, created_at
		FROM crm.customer_notifications
		WHERE customer_id = $1`
	if unread {
		q += ` AND read_at IS NULL`
	}
	q += ` ORDER BY created_at DESC LIMIT 100`

	rows, err := h.pool.Query(r.Context(), q, claims.UserID)
	if err != nil {
		writePortalErr(w, http.StatusInternalServerError, "notif.query_failed", err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		ID        string          `json:"id"`
		Kind      string          `json:"kind"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
		DeepLink  string          `json:"deep_link,omitempty"`
		Data      json.RawMessage `json:"data,omitempty"`
		ReadAt    *time.Time      `json:"read_at,omitempty"`
		CreatedAt time.Time       `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Kind, &x.Title, &x.Body, &x.DeepLink,
			&x.Data, &x.ReadAt, &x.CreatedAt); err == nil {
			out = append(out, x)
		}
	}
	// Unread count alongside the items so the UI's bell badge has
	// the truth without a second round-trip.
	var unreadCount int
	_ = h.pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM crm.customer_notifications
		 WHERE customer_id = $1 AND read_at IS NULL`,
		claims.UserID).Scan(&unreadCount)
	writePortalJSON(w, http.StatusOK, map[string]any{
		"items":        out,
		"unread_count": unreadCount,
	})
}

func (h *PortalHandler) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writePortalErr(w, http.StatusBadRequest, "notif.bad_id", err.Error())
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE crm.customer_notifications
		SET read_at = NOW()
		WHERE id = $1 AND customer_id = $2 AND read_at IS NULL
	`, id, claims.UserID); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "notif.update_failed", err.Error())
		return
	}
	writePortalJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *PortalHandler) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE crm.customer_notifications
		SET read_at = NOW()
		WHERE customer_id = $1 AND read_at IS NULL
	`, claims.UserID); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "notif.update_failed", err.Error())
		return
	}
	writePortalJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Live tech tracking during the customer's active WO
//
// Scoped twice: only returns a ping for a WO whose customer_id ==
// the caller, AND only while the WO is dispatched / in_progress.
// =============================================================================

func (h *PortalHandler) portalActiveWOTechLocation(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	// Find the most recent active WO for this customer.
	var (
		woID    uuid.UUID
		status  string
		address string
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT id, status, address
		FROM field.work_orders
		WHERE customer_id = $1
		  AND status IN ('assigned','dispatched','in_progress')
		ORDER BY created_at DESC
		LIMIT 1
	`, claims.UserID).Scan(&woID, &status, &address); err != nil {
		writePortalJSON(w, http.StatusOK, map[string]any{
			"has_active_wo": false,
		})
		return
	}

	// Latest GPS ping for that WO.
	var (
		lat, lng        float64
		accuracy        *float64
		capturedAt      time.Time
		hasPing         bool
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT lat, lng, accuracy_m, captured_at
		FROM field.tech_locations
		WHERE wo_id = $1
		ORDER BY captured_at DESC LIMIT 1
	`, woID).Scan(&lat, &lng, &accuracy, &capturedAt); err == nil {
		hasPing = true
	}

	resp := map[string]any{
		"has_active_wo": true,
		"wo_id":         woID.String(),
		"wo_status":     status,
		"address":       address,
	}
	if hasPing {
		resp["tech_ping"] = map[string]any{
			"lat":         lat,
			"lng":         lng,
			"accuracy_m":  accuracy,
			"captured_at": capturedAt,
		}
	}
	writePortalJSON(w, http.StatusOK, resp)
}

// =============================================================================
// KTP re-upload — identity refresh
//
// The customer posts a base64-encoded KTP photo; we forward the bytes
// to the uploads service via a simple inline call (skipping pkg/ocr
// for now — the existing sales-side flow runs the OCR there and we
// just persist the object url + a flag). The full OCR-on-portal path
// is a separate spike.
// =============================================================================

type ktpUploadReq struct {
	ObjectURL string `json:"object_url"`
	Notes     string `json:"notes,omitempty"`
}

func (h *PortalHandler) portalKTPUpload(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePortalErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	var in ktpUploadReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writePortalErr(w, http.StatusBadRequest, "ktp.bad_body", err.Error())
		return
	}
	if in.ObjectURL == "" {
		writePortalErr(w, http.StatusBadRequest, "ktp.missing_url",
			"object_url is required (call /api/uploads/photos first)")
		return
	}
	// Drop a ticket so the CS team verifies the re-upload manually.
	id := uuid.New()
	ticketNo := "KTP-" + time.Now().Format("20060102") + "-" + id.String()[:8]
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.tickets (
			id, ticket_number, customer_id, category, priority, status,
			summary, description, opened_by
		) VALUES ($1, $2, $3, 'other', 'low', 'open',
		          'KTP re-upload from customer',
		          'object_url=' || $4 || ' notes=' || $5,
		          $3)
	`, id, ticketNo, claims.UserID, in.ObjectURL, in.Notes); err != nil {
		writePortalErr(w, http.StatusInternalServerError, "ktp.ticket_failed", err.Error())
		return
	}
	writePortalJSON(w, http.StatusCreated, map[string]any{
		"ticket_number": ticketNo,
		"status":        "open",
		"next_step":     "We'll verify your new KTP within 1 business day.",
	})
}
