// Wave 115 — Add-On Billing handler.
//
// The mobile customer_app's buy_addon_page.dart calls CRM's
// /portal/addons/buy today; this handler adds the parallel
// /portal/billing/add-ons/* surface the brief asks for. Both paths
// coexist — the CRM path remains the authoritative purchase write
// (it pushes RADIUS for digital add-ons + mints the install WO for
// physical), and this billing-side surface keeps billing.add_on_
// purchases in sync + serves customer-portal reads.
package http

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// AddOnHandler wires the four /portal/billing/add-ons/* routes plus a
// pair of finance-admin reads. The constructor is separate from the
// main billing Handler so existing wiring stays untouched; main.go
// mounts both onto the same chi.Router.
type AddOnHandler struct {
	svc      port.AddOnUseCase
	verifier *auth.Verifier
}

func NewAddOnHandler(svc port.AddOnUseCase, verifier *auth.Verifier) *AddOnHandler {
	return &AddOnHandler{svc: svc, verifier: verifier}
}

// Mount wires the routes. The customer-facing /portal/billing/add-ons/*
// paths require RequireAuth + the customer.portal.access permission
// (the same gate the existing /portal/* endpoints in crm use). The
// admin-facing reads live under /api/billing/add-ons/*.
//
//   Customer portal:
//     GET    /portal/billing/add-ons/available
//     POST   /portal/billing/add-ons/purchase
//     GET    /portal/billing/add-ons/active
//     POST   /portal/billing/add-ons/{id}/cancel
//
//   Admin read:
//     GET    /api/billing/customers/{customer_id}/add-ons
func (h *AddOnHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Customer-portal — the access gate matches the existing
		// /portal/* endpoints. Self-scope is enforced by reading
		// claims.UserID inside the handler (the portal JWT puts
		// customer_id into UserID).
		r.With(httpserver.RequirePermission("customer.portal.access")).
			Get("/portal/billing/add-ons/available", h.portalListAvailable)
		r.With(httpserver.RequirePermission("customer.portal.access")).
			Post("/portal/billing/add-ons/purchase", h.portalPurchase)
		r.With(httpserver.RequirePermission("customer.portal.access")).
			Get("/portal/billing/add-ons/active", h.portalListActive)
		r.With(httpserver.RequirePermission("customer.portal.access")).
			Post("/portal/billing/add-ons/{id}/cancel", h.portalCancel)

		// Admin / finance read — covers TC-AOB-007 multi-add-on stack
		// visibility from the finance UI.
		r.With(httpserver.RequirePermission("billing.addon.read")).
			Get("/api/billing/customers/{customer_id}/add-ons", h.adminListForCustomer)
	})
}

// ---------------------------------------------------------------------
// Customer-portal handlers
// ---------------------------------------------------------------------

type purchaseRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Notes    string `json:"notes,omitempty"`
}

type cancelAddonRequest struct {
	Reason string `json:"reason"`
}

type catalogItemDTO struct {
	ID              string  `json:"id"`
	SKU             string  `json:"sku"`
	Name            string  `json:"name"`
	Category        string  `json:"category"`
	OneTimeFee      float64 `json:"one_time_fee"`
	MonthlyFee      float64 `json:"monthly_fee"`
	RequiresInstall bool    `json:"requires_install"`
}

type addonPurchaseDTO struct {
	ID           string  `json:"id"`
	CustomerID   string  `json:"customer_id"`
	SKU          string  `json:"sku"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Quantity     int     `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
	Total        float64 `json:"total"`
	InvoiceID    string  `json:"invoice_id,omitempty"`
	Status       string  `json:"status"`
	ValidFrom    string  `json:"valid_from,omitempty"`
	ValidUntil   string  `json:"valid_until,omitempty"`
	CancelledAt  string  `json:"cancelled_at,omitempty"`
	CancelReason string  `json:"cancel_reason,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

func toCatalogItemDTO(it port.CatalogItem) catalogItemDTO {
	return catalogItemDTO{
		ID:              it.ID.String(),
		SKU:             it.SKU,
		Name:            it.Name,
		Category:        string(it.Category),
		OneTimeFee:      it.OneTimeFee,
		MonthlyFee:      it.MonthlyFee,
		RequiresInstall: it.RequiresInstall,
	}
}

func toAddOnPurchaseDTO(p domain.AddOnPurchase) addonPurchaseDTO {
	out := addonPurchaseDTO{
		ID:           p.ID.String(),
		CustomerID:   p.CustomerID.String(),
		SKU:          p.AddOnSKU,
		Name:         p.AddOnName,
		Category:     string(p.Category),
		Quantity:     p.Quantity,
		UnitPrice:    p.UnitPrice,
		Total:        p.Total,
		Status:       string(p.Status),
		ValidFrom:    httpserver.FormatRFC3339Ptr(p.ValidFrom),
		ValidUntil:   httpserver.FormatRFC3339Ptr(p.ValidUntil),
		CancelledAt:  httpserver.FormatRFC3339Ptr(p.CancelledAt),
		CancelReason: p.CancelReason,
		CreatedAt:    httpserver.FormatRFC3339(p.CreatedAt),
	}
	if p.InvoiceID != nil {
		out.InvoiceID = p.InvoiceID.String()
	}
	return out
}

func (h *AddOnHandler) portalListAvailable(w http.ResponseWriter, r *http.Request) {
	cid := portalCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("addon.no_customer", "no customer context"))
		return
	}
	items, err := h.svc.ListAvailable(r.Context(), cid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]catalogItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toCatalogItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *AddOnHandler) portalPurchase(w http.ResponseWriter, r *http.Request) {
	cid := portalCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("addon.no_customer", "no customer context"))
		return
	}
	var req purchaseRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.PurchaseInput{
		CustomerID: cid,
		SKU:        strings.TrimSpace(req.SKU),
		Quantity:   req.Quantity,
		Notes:      req.Notes,
	}
	p, err := h.svc.Purchase(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toAddOnPurchaseDTO(*p))
}

func (h *AddOnHandler) portalListActive(w http.ResponseWriter, r *http.Request) {
	cid := portalCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("addon.no_customer", "no customer context"))
		return
	}
	items, err := h.svc.ListActive(r.Context(), cid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]addonPurchaseDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toAddOnPurchaseDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *AddOnHandler) portalCancel(w http.ResponseWriter, r *http.Request) {
	cid := portalCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("addon.no_customer", "no customer context"))
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("addon.id_invalid", "add-on id is not a valid uuid"))
		return
	}
	var req cancelAddonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.svc.Cancel(r.Context(), cid, id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAddOnPurchaseDTO(*p))
}

// ---------------------------------------------------------------------
// Admin handlers
// ---------------------------------------------------------------------

func (h *AddOnHandler) adminListForCustomer(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "customer_id")
	cid, err := uuid.Parse(idStr)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("addon.customer_id_invalid", "customer_id is not a valid uuid"))
		return
	}
	items, err := h.svc.ListActive(r.Context(), cid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]addonPurchaseDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toAddOnPurchaseDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// portalCustomerID reads the customer_id baked into the portal JWT.
// Per the convention in crm/adapter/http/portal_auth.go, the
// customer_id rides in claims.UserID.
func portalCustomerID(r *http.Request) uuid.UUID {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		return uuid.Nil
	}
	return c.UserID
}
