// Package http is the driving adapter for the enterprise bounded
// context — translates HTTP into UseCase calls. Same conventions as
// the warehouse / crm HTTP adapters.
package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	uc       port.UseCase
	verifier *auth.Verifier
}

func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// Mount — route map:
//
//	Pricebooks
//	  GET   /pricebooks                          [enterprise.pricebook.read]
//	  GET   /pricebooks/{id}                     [enterprise.pricebook.read]
//	  POST  /pricebooks                          [enterprise.pricebook.manage]
//	  PATCH /pricebooks/{id}                     [enterprise.pricebook.manage]
//	  POST  /pricebooks/{id}/publish             [enterprise.pricebook.manage]
//
//	Pricebook lines
//	  GET   /pricebooks/{id}/lines               [enterprise.pricebook.read]
//	  POST  /pricebooks/{id}/lines               [enterprise.pricebook.manage]
//	  PATCH /pricebook-lines/{id}                [enterprise.pricebook.manage]
//	  DELETE /pricebook-lines/{id}               [enterprise.pricebook.manage]
//
//	Opportunities
//	  GET   /opportunities                       [enterprise.opportunity.read]
//	  GET   /opportunities/{id}                  [enterprise.opportunity.read]
//	  POST  /opportunities                       [enterprise.opportunity.write]
//	  PATCH /opportunities/{id}                  [enterprise.opportunity.write]
//	  POST  /opportunities/{id}/advance          [enterprise.opportunity.advance]
//	  POST  /opportunities/{id}/lost             [enterprise.opportunity.write]
//	  PUT   /opportunities/{id}/pre-boq          [enterprise.opportunity.write]
//	  POST  /opportunities/{id}/pricebook        [enterprise.opportunity.write]
//
//	Maintenance
//	  POST  /maintenance/auto-lost-sweep         [enterprise.opportunity.manage]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Pricebooks
		r.With(httpserver.RequirePermission("enterprise.pricebook.read")).
			Get("/pricebooks", h.listPricebooks)
		r.With(httpserver.RequirePermission("enterprise.pricebook.read")).
			Get("/pricebooks/{id}", h.getPricebook)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Post("/pricebooks", h.createPricebook)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Patch("/pricebooks/{id}", h.updatePricebook)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Post("/pricebooks/{id}/publish", h.publishPricebook)

		// Pricebook lines (nested list, flat CRUD by line id)
		r.With(httpserver.RequirePermission("enterprise.pricebook.read")).
			Get("/pricebooks/{id}/lines", h.listPricebookLines)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Post("/pricebooks/{id}/lines", h.createPricebookLine)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Patch("/pricebook-lines/{id}", h.updatePricebookLine)
		r.With(httpserver.RequirePermission("enterprise.pricebook.manage")).
			Delete("/pricebook-lines/{id}", h.deletePricebookLine)

		// Opportunities
		r.With(httpserver.RequirePermission("enterprise.opportunity.read")).
			Get("/opportunities", h.listOpportunities)
		r.With(httpserver.RequirePermission("enterprise.opportunity.read")).
			Get("/opportunities/{id}", h.getOpportunity)
		r.With(httpserver.RequirePermission("enterprise.opportunity.write")).
			Post("/opportunities", h.createOpportunity)
		r.With(httpserver.RequirePermission("enterprise.opportunity.write")).
			Patch("/opportunities/{id}", h.updateOpportunity)
		r.With(httpserver.RequirePermission("enterprise.opportunity.advance")).
			Post("/opportunities/{id}/advance", h.advanceStage)
		r.With(httpserver.RequirePermission("enterprise.opportunity.write")).
			Post("/opportunities/{id}/lost", h.markLost)
		r.With(httpserver.RequirePermission("enterprise.opportunity.write")).
			Put("/opportunities/{id}/pre-boq", h.completePreBOQ)
		r.With(httpserver.RequirePermission("enterprise.opportunity.write")).
			Post("/opportunities/{id}/pricebook", h.pinPricebook)

		// Maintenance — exposed for cron / ops dashboard. In a future
		// deploy this could move behind a service-token rather than a
		// user JWT.
		r.With(httpserver.RequirePermission("enterprise.opportunity.manage")).
			Post("/maintenance/auto-lost-sweep", h.runAutoLostSweep)
	})
}

// =====================================================================
// Pricebook handlers
// =====================================================================

func (h *Handler) listPricebooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), 50)
	f := port.PricebookListFilter{
		Status:           q.Get("status"),
		HoldingCompanyID: q.Get("holding_company_id"),
		Code:             q.Get("code"),
		Limit:            pageSize,
		Offset:           (page - 1) * pageSize,
	}
	items, total, err := h.uc.ListPricebooks(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]pricebookDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toPricebookDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getPricebook(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.uc.GetPricebook(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPricebookDTO(*p))
}

func (h *Handler) createPricebook(w http.ResponseWriter, r *http.Request) {
	var req createPricebookRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreatePricebookInput{
		Code:             req.Code,
		Name:             req.Name,
		Currency:         req.Currency,
		EffectiveFrom:    req.EffectiveFrom,
		EffectiveTo:      req.EffectiveTo,
		HoldingCompanyID: req.HoldingCompanyID,
		Notes:            req.Notes,
	}
	if uid := actorUserID(r.Context()); uid != nil {
		in.CreatedBy = uid
	}
	p, err := h.uc.CreatePricebook(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toPricebookDTO(*p))
}

func (h *Handler) updatePricebook(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updatePricebookRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdatePricebookInput{
		ID:            id,
		Name:          req.Name,
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
		Notes:         req.Notes,
	}
	p, err := h.uc.UpdatePricebook(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPricebookDTO(*p))
}

func (h *Handler) publishPricebook(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.uc.PublishPricebook(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPricebookDTO(*p))
}

// =====================================================================
// Pricebook line handlers
// =====================================================================

func (h *Handler) listPricebookLines(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.uc.ListPricebookLines(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]pricebookLineDTO, 0, len(items))
	for _, l := range items {
		out = append(out, toPricebookLineDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) createPricebookLine(w http.ResponseWriter, r *http.Request) {
	pricebookID, err := parseUUID(chi.URLParam(r, "id"), "pricebook")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createPricebookLineRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	allowed := make([]uuid.UUID, 0, len(req.AllowedProviderCompanyIDs))
	for _, s := range req.AllowedProviderCompanyIDs {
		u, perr := parseUUID(s, "allowed_provider_company_ids")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		allowed = append(allowed, u)
	}
	in := port.CreatePricebookLineInput{
		PricebookID:               pricebookID,
		SKU:                       req.SKU,
		Name:                      req.Name,
		Category:                  req.Category,
		Description:               req.Description,
		Unit:                      req.Unit,
		BasePrice:                 req.BasePrice,
		DefaultMarginPct:          req.DefaultMarginPct,
		MinMarginPct:              req.MinMarginPct,
		MaxDiscountPct:            req.MaxDiscountPct,
		AllowedProviderCompanyIDs: allowed,
		OwnerRole:                 req.OwnerRole,
		SortOrder:                 req.SortOrder,
	}
	l, err := h.uc.CreatePricebookLine(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toPricebookLineDTO(*l))
}

func (h *Handler) updatePricebookLine(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook_line")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updatePricebookLineRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdatePricebookLineInput{
		ID:               id,
		Name:             req.Name,
		Category:         req.Category,
		Description:      req.Description,
		Unit:             req.Unit,
		BasePrice:        req.BasePrice,
		DefaultMarginPct: req.DefaultMarginPct,
		MinMarginPct:     req.MinMarginPct,
		MaxDiscountPct:   req.MaxDiscountPct,
		OwnerRole:        req.OwnerRole,
		SortOrder:        req.SortOrder,
		Active:           req.Active,
	}
	if req.AllowedProviderCompanyIDs != nil {
		allowed := make([]uuid.UUID, 0, len(*req.AllowedProviderCompanyIDs))
		for _, s := range *req.AllowedProviderCompanyIDs {
			u, perr := parseUUID(s, "allowed_provider_company_ids")
			if perr != nil {
				httpserver.WriteError(w, perr)
				return
			}
			allowed = append(allowed, u)
		}
		in.AllowedProviderCompanyIDs = &allowed
	}
	l, err := h.uc.UpdatePricebookLine(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPricebookLineDTO(*l))
}

func (h *Handler) deletePricebookLine(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "pricebook_line")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.DeletePricebookLine(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// Opportunity handlers
// =====================================================================

func (h *Handler) listOpportunities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), 50)
	f := port.OpportunityListFilter{
		Stage:               q.Get("stage"),
		Search:              q.Get("q"),
		IncludeArchivedLost: q.Get("include_lost") == "true",
		Limit:               pageSize,
		Offset:              (page - 1) * pageSize,
	}
	if s := q.Get("owner_user_id"); s != "" {
		u, err := parseUUID(s, "owner_user_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.OwnerUserID = &u
	}
	if s := q.Get("branch_id"); s != "" {
		u, err := parseUUID(s, "branch_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.BranchID = &u
	}
	items, total, err := h.uc.ListOpportunities(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]opportunityDTO, 0, len(items))
	for _, o := range items {
		out = append(out, toOpportunityDTO(o))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getOpportunity(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	o, err := h.uc.GetOpportunity(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) createOpportunity(w http.ResponseWriter, r *http.Request) {
	var req createOpportunityRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateOpportunityInput{
		AccountName:     req.AccountName,
		AccountIndustry: req.AccountIndustry,
		AccountSize:     req.AccountSize,
		PICName:         req.PICName,
		PICTitle:        req.PICTitle,
		PICPhone:        req.PICPhone,
		PICEmail:        req.PICEmail,
		EstimatedValue:  req.EstimatedValue,
		Currency:        req.Currency,
		Source:          req.Source,
		Notes:           req.Notes,
	}
	if req.ExpectedCloseAt != "" {
		in.ExpectedCloseAt = &req.ExpectedCloseAt
	}
	if req.OwnerUserID != "" {
		u, err := parseUUID(req.OwnerUserID, "owner_user_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		in.OwnerUserID = &u
	}
	if req.BranchID != "" {
		u, err := parseUUID(req.BranchID, "branch_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		in.BranchID = &u
	}
	if req.CustomerID != "" {
		u, err := parseUUID(req.CustomerID, "customer_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		in.CustomerID = &u
	}
	if req.ReferrerCustomerID != "" {
		u, err := parseUUID(req.ReferrerCustomerID, "referrer_customer_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		in.ReferrerCustomerID = &u
	}
	o, err := h.uc.CreateOpportunity(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toOpportunityDTO(*o))
}

func (h *Handler) updateOpportunity(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateOpportunityRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateOpportunityInput{
		ID:              id,
		AccountName:     req.AccountName,
		AccountIndustry: req.AccountIndustry,
		AccountSize:     req.AccountSize,
		PICName:         req.PICName,
		PICTitle:        req.PICTitle,
		PICPhone:        req.PICPhone,
		PICEmail:        req.PICEmail,
		EstimatedValue:  req.EstimatedValue,
		ExpectedCloseAt: req.ExpectedCloseAt,
		Notes:           req.Notes,
		IfRevision:      req.IfRevision,
	}
	if req.OwnerUserID != nil {
		if *req.OwnerUserID == "" {
			// Empty string = clear (we don't currently expose that
			// concept, but reserve the semantic).
		} else {
			u, err := parseUUID(*req.OwnerUserID, "owner_user_id")
			if err != nil {
				httpserver.WriteError(w, err)
				return
			}
			in.OwnerUserID = &u
		}
	}
	if req.BranchID != nil {
		if *req.BranchID != "" {
			u, err := parseUUID(*req.BranchID, "branch_id")
			if err != nil {
				httpserver.WriteError(w, err)
				return
			}
			in.BranchID = &u
		}
	}
	o, err := h.uc.UpdateOpportunity(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) advanceStage(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req advanceStageRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	o, err := h.uc.AdvanceStage(r.Context(), port.AdvanceStageInput{
		ID:          id,
		TargetStage: req.TargetStage,
		POReference: req.POReference,
		IfRevision:  req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) markLost(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req markLostRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	o, err := h.uc.MarkLost(r.Context(), port.MarkLostInput{
		ID:         id,
		ReasonCode: req.ReasonCode,
		Reason:     req.Reason,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) completePreBOQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req completePreBOQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if len(req.Snapshot) == 0 {
		httpserver.WriteError(w, errors.Validation(
			"opportunity.pre_boq_empty", "snapshot must be a non-empty JSON object"))
		return
	}
	o, err := h.uc.CompletePreBOQ(r.Context(), port.CompletePreBOQInput{
		ID:         id,
		Snapshot:   req.Snapshot,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) pinPricebook(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req pinPricebookRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	pbID, err := parseUUID(req.PricebookID, "pricebook_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	o, err := h.uc.PinPricebook(r.Context(), port.PinPricebookInput{
		ID:          id,
		PricebookID: pbID,
		IfRevision:  req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpportunityDTO(*o))
}

func (h *Handler) runAutoLostSweep(w http.ResponseWriter, r *http.Request) {
	ids, err := h.uc.RunAutoLostSweep(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"flipped":       out,
		"flipped_count": len(out),
	})
}

// =====================================================================
// Helpers
// =====================================================================

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(
			field+".id_invalid",
			field+" is not a valid uuid",
		)
	}
	return id, nil
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
