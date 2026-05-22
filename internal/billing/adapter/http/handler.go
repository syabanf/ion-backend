// Package http is the driving adapter for the billing context.
package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	uc       port.UseCase
	verifier *auth.Verifier
	// portalRL throttles the public customer-portal endpoints
	// (/portal/termination/*). Nil disables throttling — only the
	// production wiring sets it.
	portalRL *httpserver.RateLimit
}

func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// WithPortalRateLimit attaches a per-IP rate limiter to the customer-
// portal endpoints. Tune burst tight enough that brute-forcing OTPs
// from a single IP is impractical, but loose enough that a customer can
// retry after a typo.
func (h *Handler) WithPortalRateLimit(rl *httpserver.RateLimit) *Handler {
	h.portalRL = rl
	return h
}

// Mount — route map:
//
//	GET  /invoices                        [billing.invoice.read]
//	GET  /invoices/{id}                   [billing.invoice.read]
//	POST /invoices                        [billing.invoice.create]
//	POST /invoices/{id}/issue             [billing.invoice.create]
//	POST /invoices/{id}/cancel            [billing.invoice.void]
//	POST /invoices/{id}/payments          [billing.payment.record]
//	GET  /orders/{id}/otc-status          [billing.invoice.read]
func (h *Handler) Mount(r chi.Router) {
	// Public customer-portal endpoints — no auth, OTP-gated, per-IP
	// rate limited. Mounted ahead of the authenticated group so the
	// shared chi.Router resolves them without ever entering the
	// RequireAuth middleware.
	r.Group(func(r chi.Router) {
		if h.portalRL != nil {
			r.Use(h.portalRL.Middleware())
		}
		r.Post("/portal/termination/request", h.portalRequestOTP)
		r.Post("/portal/termination/confirm", h.portalConfirm)
	})

	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("billing.invoice.read")).Get("/invoices", h.listInvoices)
		r.With(httpserver.RequirePermission("billing.invoice.read")).Get("/invoices/{id}", h.getInvoice)
		r.With(httpserver.RequirePermission("billing.invoice.create")).Post("/invoices", h.createInvoice)
		r.With(httpserver.RequirePermission("billing.invoice.create")).Post("/invoices/{id}/issue", h.issueInvoice)
		r.With(httpserver.RequirePermission("billing.invoice.void")).Post("/invoices/{id}/cancel", h.cancelInvoice)
		r.With(httpserver.RequirePermission("billing.payment.record")).Post("/invoices/{id}/payments", h.recordPayment)
		r.With(httpserver.RequirePermission("billing.invoice.read")).Get("/orders/{id}/otc-status", h.orderOTCStatus)

		// M6 r2 — policy / cycles / commissions / manual tick
		r.With(httpserver.RequirePermission("billing.policy.read")).Get("/policy", h.getPolicy)
		r.With(httpserver.RequirePermission("billing.policy.manage")).Patch("/policy", h.updatePolicy)
		r.With(httpserver.RequirePermission("billing.cycles.read")).Get("/cycles", h.listCycles)
		r.With(httpserver.RequirePermission("billing.cycles.run")).Post("/cycles/run", h.runTick)
		r.With(httpserver.RequirePermission("billing.commission.read")).Get("/commissions", h.listCommissions)

		// M6 r3 — voluntary termination + referral rewards.
		r.With(httpserver.RequirePermission("billing.termination.read")).Get("/terminations", h.listTerminations)
		r.With(httpserver.RequirePermission("billing.termination.read")).Get("/terminations/{id}", h.getTermination)
		r.With(httpserver.RequirePermission("billing.termination.request")).Post("/terminations", h.requestTermination)
		r.With(httpserver.RequirePermission("billing.termination.manage")).Post("/terminations/{id}/cancel", h.cancelTermination)
		r.With(httpserver.RequirePermission("billing.referral.read")).Get("/referral-rewards", h.listReferralRewards)
	})
}

// DTOs (invoiceDTO, createInvoiceRequest, recordPaymentRequest, …) live in dto.go.

// =====================================================================
// Handlers
// =====================================================================

func (h *Handler) listInvoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntDefault(q.Get("page_size"), 50)
	page := parseIntDefault(q.Get("page"), 1)
	f := port.InvoiceListFilter{
		Status:      q.Get("status"),
		InvoiceType: q.Get("invoice_type"),
		Search:      q.Get("q"),
		Limit:       limit,
		Offset:      (page - 1) * limit,
	}
	if v := q.Get("customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("invoice.customer_invalid", "customer_id is not a uuid"))
			return
		}
		f.CustomerID = &id
	}
	if v := q.Get("order_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("invoice.order_invalid", "order_id is not a uuid"))
			return
		}
		f.OrderID = &id
	}
	out, total, err := h.uc.ListInvoices(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]invoiceDTO, 0, len(out))
	for _, v := range out {
		items = append(items, toInvoiceDTO(v))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("invoice.id_invalid", "id is not a uuid"))
		return
	}
	v, err := h.uc.GetInvoice(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*v))
}

func (h *Handler) createInvoice(w http.ResponseWriter, r *http.Request) {
	var req createInvoiceRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cid, err := uuid.Parse(req.CustomerID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("invoice.customer_invalid", "customer_id is not a uuid"))
		return
	}
	in := port.CreateInvoiceInput{
		CustomerID:       cid,
		InvoiceType:      domain.InvoiceType(req.InvoiceType),
		PPNRate:          11.0,
		Notes:            req.Notes,
		IssueImmediately: req.Issue,
	}
	if req.PPNRate > 0 {
		in.PPNRate = req.PPNRate
	}
	if req.OrderID != "" {
		oid, err := uuid.Parse(req.OrderID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("invoice.order_invalid", "order_id is not a uuid"))
			return
		}
		in.OrderID = &oid
	}
	if req.DueDate != "" {
		t, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("invoice.due_invalid", "due_date must be YYYY-MM-DD"))
			return
		}
		in.DueDate = t
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c != nil {
		uid := c.UserID
		in.CreatedBy = &uid
	}
	for _, l := range req.Lines {
		qty := l.Quantity
		if qty == 0 {
			qty = 1
		}
		in.Lines = append(in.Lines, port.LineItemInput{
			Description: l.Description, ItemType: l.ItemType,
			Quantity: qty, UnitPrice: l.UnitPrice,
		})
	}
	v, err := h.uc.CreateInvoice(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toInvoiceDTO(*v))
}

func (h *Handler) issueInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("invoice.id_invalid", "id is not a uuid"))
		return
	}
	v, err := h.uc.IssueInvoice(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*v))
}

func (h *Handler) cancelInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("invoice.id_invalid", "id is not a uuid"))
		return
	}
	v, err := h.uc.CancelInvoice(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*v))
}

func (h *Handler) recordPayment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("invoice.id_invalid", "id is not a uuid"))
		return
	}
	var req recordPaymentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	v, err := h.uc.RecordPayment(r.Context(), port.RecordPaymentInput{
		InvoiceID:            id,
		Amount:               req.Amount,
		PaymentMethod:        req.PaymentMethod,
		GatewayTransactionID: req.GatewayTransactionID,
		Notes:                req.Notes,
		ConfirmedBy:          c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*v))
}

func (h *Handler) orderOTCStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("order.id_invalid", "id is not a uuid"))
		return
	}
	paid, err := h.uc.IsOrderOTCPaid(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"order_id":  id.String(),
		"otc_paid":  paid,
	})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
