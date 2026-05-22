// Billing priority-followup endpoints: termination consolidation
// (unions billing.termination_requests with portal-OTP termination
// tickets that land in field.tickets) + Xendit payment webhook.
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/webhookx"
)

type PriorityHandler struct {
	pool        *pgxpool.Pool
	verifier    *auth.Verifier
	xenditHook  *webhookx.Verifier
	log         *slog.Logger
}

func NewPriorityHandler(pool *pgxpool.Pool, verifier *auth.Verifier) *PriorityHandler {
	log := slog.Default().With("component", "billing.priority")
	h := &PriorityHandler{pool: pool, verifier: verifier, log: log}
	// Set up the Xendit webhook verifier. Secret comes from env;
	// empty means "dev mode, accept everything" (signature check
	// disabled but IP allow-list + idempotency still apply).
	cfg := webhookx.Config{
		Secret:         os.Getenv("XENDIT_WEBHOOK_SECRET"),
		AllowedIPs:     splitCSV(os.Getenv("XENDIT_ALLOWED_IPS")),
		TrustedProxies: splitCSV(os.Getenv("WEBHOOK_TRUSTED_PROXIES")),
	}
	v, err := webhookx.New(webhookx.XenditProvider{}, cfg, pool)
	if err != nil {
		log.Error("webhookx setup failed; webhook will be disabled", "err", err)
	} else {
		h.xenditHook = v
	}
	return h
}

func splitCSV(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	out := strings.Split(s, ",")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

func (h *PriorityHandler) Mount(r chi.Router) {
	// Xendit webhook — PUBLIC route (no JWT). Authentication is the
	// HMAC + IP allow-list check inside webhookx.Middleware.
	if h.xenditHook != nil {
		r.With(h.xenditHook.Middleware).
			Post("/webhooks/xendit", h.handleXenditWebhook)
	}

	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("billing.termination.read")).
			Get("/terminations/consolidated", h.consolidatedTerminations)

		// Webhook deliveries admin — read-only forensic surface.
		r.With(httpserver.RequirePermission("platform.webhook_delivery.read")).
			Get("/webhook-deliveries", h.listWebhookDeliveries)
		r.With(httpserver.RequirePermission("platform.webhook_delivery.read")).
			Get("/webhook-deliveries/{id}", h.getWebhookDelivery)
	})
}

func (h *PriorityHandler) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	q := `
		SELECT id, provider, event_id, remote_ip, received_at,
		       processed_at, COALESCE(process_error, '')
		FROM platform.webhook_deliveries
		WHERE 1=1
	`
	args := []any{}
	if provider != "" {
		args = append(args, provider)
		q += " AND provider = $1"
	}
	q += " ORDER BY received_at DESC LIMIT 200"
	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID           string     `json:"id"`
		Provider     string     `json:"provider"`
		EventID      string     `json:"event_id"`
		RemoteIP     string     `json:"remote_ip"`
		ReceivedAt   time.Time  `json:"received_at"`
		ProcessedAt  *time.Time `json:"processed_at,omitempty"`
		ProcessError string     `json:"process_error,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Provider, &x.EventID, &x.RemoteIP,
			&x.ReceivedAt, &x.ProcessedAt, &x.ProcessError); err == nil {
			out = append(out, x)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
}

func (h *PriorityHandler) getWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var d struct {
		ID           string     `json:"id"`
		Provider     string     `json:"provider"`
		EventID      string     `json:"event_id"`
		Body         []byte     `json:"-"`
		BodyPreview  string     `json:"body_preview"`
		RemoteIP     string     `json:"remote_ip"`
		ReceivedAt   time.Time  `json:"received_at"`
		ProcessedAt  *time.Time `json:"processed_at,omitempty"`
		ProcessError string     `json:"process_error,omitempty"`
	}
	err := h.pool.QueryRow(r.Context(), `
		SELECT id, provider, event_id, body, remote_ip, received_at,
		       processed_at, COALESCE(process_error, '')
		FROM platform.webhook_deliveries WHERE id = $1
	`, id).Scan(&d.ID, &d.Provider, &d.EventID, &d.Body, &d.RemoteIP,
		&d.ReceivedAt, &d.ProcessedAt, &d.ProcessError)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, err)
		return
	}
	preview := string(d.Body)
	if len(preview) > 4096 {
		preview = preview[:4096] + "…(truncated)"
	}
	d.BodyPreview = preview
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

func writeJSONErr(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}

// handleXenditWebhook is the Xendit payment-confirmed callback.
// Idempotency + signature + IP gating are all handled by webhookx
// before we get here — this function only needs to translate the
// payload into the right invoice + payment_intent state changes.
//
// Xendit invoice-paid payload (subset we care about):
//
//	{
//	  "id":                 "5e2d1...",        // Xendit invoice id
//	  "external_id":        "<our intent_id>", // mirror of what we sent
//	  "user_id":            "...",
//	  "status":             "PAID" | "EXPIRED" | "PENDING",
//	  "merchant_name":      "ION",
//	  "amount":             277500,
//	  "paid_at":            "2026-05-19T17:30:00Z",
//	  "payment_method":     "BANK_TRANSFER",
//	  "payment_channel":    "BCA"
//	}
//
// We:
//   1. Look up the matching billing.payment_intents row by gateway_ref
//      OR by external_id (== intent_id we minted).
//   2. Update the intent.status accordingly.
//   3. On PAID: also flip the parent billing.invoices.status='paid'.
func (h *PriorityHandler) handleXenditWebhook(w http.ResponseWriter, r *http.Request) {
	d := webhookx.DeliveryFromContext(r.Context())
	if d == nil {
		http.Error(w, "no delivery in context", http.StatusInternalServerError)
		return
	}
	var payload struct {
		ID         string  `json:"id"`
		ExternalID string  `json:"external_id"`
		Status     string  `json:"status"`
		Amount     float64 `json:"amount"`
		PaidAt     string  `json:"paid_at"`
	}
	if err := json.Unmarshal(d.RawBody, &payload); err != nil {
		h.log.Warn("xendit webhook: bad payload", "event_id", d.EventID, "err", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Map Xendit status → our intent.status.
	var intentStatus string
	switch payload.Status {
	case "PAID", "SETTLED", "COMPLETED":
		intentStatus = "succeeded"
	case "EXPIRED":
		intentStatus = "expired"
	case "FAILED", "FAILED_TO_PROCESS":
		intentStatus = "failed"
	default:
		intentStatus = "pending"
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.log.Error("xendit webhook: begin tx", "err", err)
		http.Error(w, "tx", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Update the intent row by external_id (==our intent_id) first,
	// or by gateway_ref as the fallback when we stamped that from the
	// Xendit "id" field on intent creation.
	now := time.Now()
	var invoiceID string
	if err := tx.QueryRow(r.Context(), `
		UPDATE billing.payment_intents
		SET status = $1,
		    confirmed_at = CASE WHEN $1='succeeded' THEN $2 ELSE confirmed_at END,
		    gateway_payload = $3,
		    updated_at = $2
		WHERE id::text = $4 OR gateway_ref = $5
		RETURNING invoice_id
	`, intentStatus, now, d.RawBody, payload.ExternalID, payload.ID).Scan(&invoiceID); err != nil {
		h.log.Warn("xendit webhook: no matching intent",
			"event_id", d.EventID, "external_id", payload.ExternalID, "err", err)
		// Still 200 — webhook delivered and stored. We don't want
		// Xendit to retry; ops can replay manually if needed.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"matched":false}`))
		return
	}

	if intentStatus == "succeeded" && invoiceID != "" {
		if _, err := tx.Exec(r.Context(), `
			UPDATE billing.invoices
			SET status = 'paid', paid_at = $1, updated_at = $1
			WHERE id = $2 AND status != 'paid'
		`, now, invoiceID); err != nil {
			h.log.Error("xendit webhook: invoice update failed",
				"invoice_id", invoiceID, "err", err)
		}
	}

	// Mark webhook delivery as processed for forensic audit.
	_, _ = tx.Exec(r.Context(), `
		UPDATE platform.webhook_deliveries
		SET processed_at = $1
		WHERE event_id = $2 AND provider = 'xendit'
	`, now, d.EventID)

	if err := tx.Commit(r.Context()); err != nil {
		h.log.Error("xendit webhook: commit", "err", err)
		http.Error(w, "commit", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// consolidatedTerminations unions:
//   1. billing.termination_requests (finance-tracked voluntary terminations)
//   2. field.tickets WHERE summary ILIKE 'TERMINATION%' (portal-OTP self-served)
// So the staff dashboard sees both flows in one place.
func (h *PriorityHandler) consolidatedTerminations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT
		    t.id::text, 'billing' AS source,
		    t.customer_id::text,
		    COALESCE(c.full_name, '') AS customer_name,
		    t.status, COALESCE(t.reason, '') AS reason,
		    t.requested_at, NULL::timestamptz AS resolved_at,
		    NULL::text AS ticket_number, NULL::text AS wo_id
		FROM billing.termination_requests t
		LEFT JOIN crm.customers c ON c.id = t.customer_id

		UNION ALL

		SELECT
		    tk.id::text, 'portal' AS source,
		    tk.customer_id::text,
		    COALESCE(c.full_name, '') AS customer_name,
		    tk.status, COALESCE(tk.description, '') AS reason,
		    tk.created_at AS requested_at, tk.resolved_at,
		    tk.ticket_number, NULL::text AS wo_id
		FROM field.tickets tk
		LEFT JOIN crm.customers c ON c.id = tk.customer_id
		WHERE tk.summary ILIKE 'TERMINATION%'

		ORDER BY requested_at DESC NULLS LAST
		LIMIT 200
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": err.Error()},
		})
		return
	}
	defer rows.Close()
	type row struct {
		ID           string     `json:"id"`
		Source       string     `json:"source"`        // billing | portal
		CustomerID   string     `json:"customer_id"`
		CustomerName string     `json:"customer_name"`
		Status       string     `json:"status"`
		Reason       string     `json:"reason,omitempty"`
		RequestedAt  *time.Time `json:"requested_at,omitempty"`
		ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
		TicketNumber *string    `json:"ticket_number,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var wo *string
		if err := rows.Scan(&x.ID, &x.Source, &x.CustomerID, &x.CustomerName,
			&x.Status, &x.Reason, &x.RequestedAt, &x.ResolvedAt,
			&x.TicketNumber, &wo); err == nil {
			out = append(out, x)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
}
