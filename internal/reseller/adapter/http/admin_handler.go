package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// AdminHandler is the internal-admin HTTP surface for the reseller
// bounded context. Mounted on the main API (alongside enterprise,
// crm, warehouse, etc.) so admin tooling can manage reseller
// accounts + wholesale catalog + order approvals.
//
// Auth: every route requires a valid bearer token + a fine-grained
// permission key. Identity-svc owns the source of truth for which
// users hold which permission.
//
// Routes:
//
//	GET    /api/reseller/admin/accounts                    [reseller.account.read]
//	POST   /api/reseller/admin/accounts                    [reseller.account.write]
//	POST   /api/reseller/admin/accounts/{id}/approve       [reseller.account.write]
//	POST   /api/reseller/admin/accounts/{id}/suspend       [reseller.account.write]
//	GET    /api/reseller/admin/wholesale/skus              [reseller.wholesale.read]
//	POST   /api/reseller/admin/wholesale/skus              [reseller.wholesale.write]
//	GET    /api/reseller/admin/wholesale/orders            [reseller.wholesale.read]
//	POST   /api/reseller/admin/wholesale/orders/{id}/approve  [reseller.wholesale.write]
//	POST   /api/reseller/admin/wholesale/orders/{id}/fulfill  [reseller.wholesale.write]
type AdminHandler struct {
	onboarding port.OnboardingUseCase
	wholesale  port.WholesaleUseCase
	verifier   *auth.Verifier
}

func NewAdminHandler(onboarding port.OnboardingUseCase, wholesale port.WholesaleUseCase, verifier *auth.Verifier) *AdminHandler {
	return &AdminHandler{onboarding: onboarding, wholesale: wholesale, verifier: verifier}
}

func (h *AdminHandler) Mount(r chi.Router) {
	r.Route("/api/reseller/admin", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Accounts
		r.With(httpserver.RequirePermission("reseller.account.read")).
			Get("/accounts", h.listAccounts)
		r.With(httpserver.RequirePermission("reseller.account.write")).
			Post("/accounts", h.onboardAccount)
		r.With(httpserver.RequirePermission("reseller.account.write")).
			Post("/accounts/{id}/approve", h.approveAccount)
		r.With(httpserver.RequirePermission("reseller.account.write")).
			Post("/accounts/{id}/suspend", h.suspendAccount)

		// Wholesale catalog
		r.With(httpserver.RequirePermission("reseller.wholesale.read")).
			Get("/wholesale/skus", h.listSKUs)
		r.With(httpserver.RequirePermission("reseller.wholesale.write")).
			Post("/wholesale/skus", h.createSKU)

		// Wholesale orders
		r.With(httpserver.RequirePermission("reseller.wholesale.read")).
			Get("/wholesale/orders", h.listOrders)
		r.With(httpserver.RequirePermission("reseller.wholesale.write")).
			Post("/wholesale/orders/{id}/approve", h.approveOrder)
		r.With(httpserver.RequirePermission("reseller.wholesale.write")).
			Post("/wholesale/orders/{id}/fulfill", h.fulfillOrder)
	})
}

// ---------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------

func (h *AdminHandler) listAccounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.ResellerListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("parent_subsidiary_id"); s != "" {
		u, err := parseUUID(s, "parent_subsidiary_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ParentSubsidiaryID = &u
	}
	items, total, err := h.onboarding.ListAccounts(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]resellerAccountDTO, 0, len(items))
	for _, a := range items {
		out = append(out, toResellerAccountDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *AdminHandler) onboardAccount(w http.ResponseWriter, r *http.Request) {
	var req onboardResellerRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	in := port.OnboardResellerInput{
		Name:         req.Name,
		NPWP:         req.NPWP,
		ContactEmail: req.ContactEmail,
		ContactPhone: req.ContactPhone,
	}
	if req.ParentSubsidiaryID != nil && *req.ParentSubsidiaryID != "" {
		u, err := parseUUID(*req.ParentSubsidiaryID, "parent_subsidiary_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.ParentSubsidiaryID = &u
	}
	a, err := h.onboarding.OnboardReseller(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toResellerAccountDTO(*a))
}

func (h *AdminHandler) approveAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "reseller_account")
	if err != nil {
		writeErr(w, err)
		return
	}
	approver := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		approver = *u
	}
	if approver == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	a, err := h.onboarding.ApproveKYC(r.Context(), id, approver)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResellerAccountDTO(*a))
}

func (h *AdminHandler) suspendAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "reseller_account")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req suspendResellerRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	a, err := h.onboarding.Suspend(r.Context(), id, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResellerAccountDTO(*a))
}

// ---------------------------------------------------------------------
// Wholesale catalog
// ---------------------------------------------------------------------

func (h *AdminHandler) listSKUs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.WholesaleSKUListFilter{
		OnlyActive: q.Get("only_active") == "true",
		Limit:      pageSize,
		Offset:     (page - 1) * pageSize,
	}
	if s := q.Get("supplier_subsidiary_id"); s != "" {
		u, err := parseUUID(s, "supplier_subsidiary_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.SupplierSubsidiaryID = &u
	}
	items, total, err := h.wholesale.ListSKUs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]wholesaleSKUDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toWholesaleSKUDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *AdminHandler) createSKU(w http.ResponseWriter, r *http.Request) {
	var req createSKURequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	supplierID, err := parseUUID(req.SupplierSubsidiaryID, "supplier_subsidiary_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.wholesale.CreateSKU(r.Context(), port.CreateWholesaleSKUInput{
		SupplierSubsidiaryID: supplierID,
		Name:                 req.Name,
		SKUCode:              req.SKUCode,
		UnitPrice:            req.UnitPrice,
		Unit:                 req.Unit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWholesaleSKUDTO(*s))
}

// ---------------------------------------------------------------------
// Wholesale orders
// ---------------------------------------------------------------------

func (h *AdminHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.WholesaleOrderListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	// Admin can optionally filter by reseller.
	if s := q.Get("reseller_account_id"); s != "" {
		u, err := parseUUID(s, "reseller_account")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ResellerAccountID = u
	}
	items, total, err := h.wholesale.ListOrders(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]wholesaleOrderDTO, 0, len(items))
	for _, o := range items {
		out = append(out, toWholesaleOrderDTO(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *AdminHandler) approveOrder(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "wholesale_order")
	if err != nil {
		writeErr(w, err)
		return
	}
	approver := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		approver = *u
	}
	if approver == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	o, err := h.wholesale.ApproveOrder(r.Context(), id, approver)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWholesaleOrderDTO(*o))
}

func (h *AdminHandler) fulfillOrder(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "wholesale_order")
	if err != nil {
		writeErr(w, err)
		return
	}
	o, err := h.wholesale.FulfillOrder(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWholesaleOrderDTO(*o))
}
