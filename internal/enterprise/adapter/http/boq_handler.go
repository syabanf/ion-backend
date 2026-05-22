package http

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// BOQHandler is the Phase 3 HTTP surface. It depends on the BOQUseCase
// interface (a narrowed view of Service) so unit tests can stub the
// usecase without standing up the whole enterprise service.
type BOQHandler struct {
	uc       port.BOQUseCase
	verifier *auth.Verifier
}

func NewBOQHandler(uc port.BOQUseCase, verifier *auth.Verifier) *BOQHandler {
	return &BOQHandler{uc: uc, verifier: verifier}
}

// Mount — Phase 3 route map:
//
//	SLA templates (admin)
//	  GET   /sla-templates                            [enterprise.sla_template.read]
//	  POST  /sla-templates                            [enterprise.sla_template.manage]
//	  PATCH /sla-templates/{id}                       [enterprise.sla_template.manage]
//
//	Approval templates (admin)
//	  GET   /approval-templates                       [enterprise.approval_template.read]
//	  GET   /approval-templates/{id}                  [enterprise.approval_template.read]
//	  POST  /approval-templates                       [enterprise.approval_template.manage]
//	  PATCH /approval-templates/{id}                  [enterprise.approval_template.manage]
//	  POST  /approval-templates/{id}/publish          [enterprise.approval_template.manage]
//
//	BOQ — header CRUD + lifecycle
//	  GET   /boqs                                     [enterprise.boq.read]
//	  GET   /boqs/{id}                                [enterprise.boq.read]
//	  POST  /boqs                                     [enterprise.boq.write]
//	  PATCH /boqs/{id}                                [enterprise.boq.write]
//	  POST  /boqs/{id}/submit                         [enterprise.boq.submit]
//	  POST  /boqs/{id}/start-revision                 [enterprise.boq.write]
//
//	BOQ — lines
//	  POST  /boqs/{id}/lines                          [enterprise.boq.write]
//	  PATCH /boq-lines/{id}                           [enterprise.boq.write]
//	  DELETE /boq-lines/{id}                          [enterprise.boq.write]
//	  PUT   /boq-lines/{id}/vendor-cost               [enterprise.boq.vendor_cost]
//
//	Approval actions
//	  GET   /approval-instances                       [enterprise.boq.read]  (filtered by ?pending_for_me=true)
//	  POST  /approval-instances/{id}/approve          [enterprise.boq.approve]
//	  POST  /approval-instances/{id}/reject           [enterprise.boq.approve]
func (h *BOQHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		// Apply field-masking middleware before any BOQ-touching handler
		// so vendor responses never leak sell/margin/discount even if a
		// handler forgets to strip them. NFR-011 — server-side masking,
		// not just UI.
		r.Use(BOQFieldMaskMiddleware)

		// SLA templates
		r.With(httpserver.RequirePermission("enterprise.sla_template.read")).
			Get("/sla-templates", h.listSLATemplates)
		r.With(httpserver.RequirePermission("enterprise.sla_template.manage")).
			Post("/sla-templates", h.createSLATemplate)
		r.With(httpserver.RequirePermission("enterprise.sla_template.manage")).
			Patch("/sla-templates/{id}", h.updateSLATemplate)

		// Approval templates
		r.With(httpserver.RequirePermission("enterprise.approval_template.read")).
			Get("/approval-templates", h.listApprovalTemplates)
		r.With(httpserver.RequirePermission("enterprise.approval_template.read")).
			Get("/approval-templates/{id}", h.getApprovalTemplate)
		r.With(httpserver.RequirePermission("enterprise.approval_template.manage")).
			Post("/approval-templates", h.createApprovalTemplate)
		r.With(httpserver.RequirePermission("enterprise.approval_template.manage")).
			Patch("/approval-templates/{id}", h.updateApprovalTemplate)
		r.With(httpserver.RequirePermission("enterprise.approval_template.manage")).
			Post("/approval-templates/{id}/publish", h.publishApprovalTemplate)

		// BOQ
		r.With(httpserver.RequirePermission("enterprise.boq.read")).
			Get("/boqs", h.listBOQs)
		r.With(httpserver.RequirePermission("enterprise.boq.read")).
			Get("/boqs/{id}", h.getBOQ)
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Post("/boqs", h.createBOQ)
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Patch("/boqs/{id}", h.updateBOQ)
		r.With(httpserver.RequirePermission("enterprise.boq.submit")).
			Post("/boqs/{id}/submit", h.submitBOQ)
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Post("/boqs/{id}/start-revision", h.startRevision)

		// BOQ lines
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Post("/boqs/{id}/lines", h.createBOQLine)
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Patch("/boq-lines/{id}", h.updateBOQLine)
		r.With(httpserver.RequirePermission("enterprise.boq.write")).
			Delete("/boq-lines/{id}", h.deleteBOQLine)
		r.With(httpserver.RequirePermission("enterprise.boq.vendor_cost")).
			Put("/boq-lines/{id}/vendor-cost", h.setVendorCost)

		// Approval instances
		r.With(httpserver.RequirePermission("enterprise.boq.read")).
			Get("/approval-instances", h.listApprovalInstances)
		r.With(httpserver.RequirePermission("enterprise.boq.approve")).
			Post("/approval-instances/{id}/approve", h.approveStep)
		r.With(httpserver.RequirePermission("enterprise.boq.approve")).
			Post("/approval-instances/{id}/reject", h.rejectStep)
		// E3 — reassign a pending step to a different user.
		r.With(httpserver.RequirePermission("enterprise.approval.reassign")).
			Post("/approval-instances/{id}/reassign", h.reassignStep)
	})
}

// =====================================================================
// SLA template handlers
// =====================================================================

func (h *BOQHandler) listSLATemplates(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") == "true"
	items, err := h.uc.ListSLATemplates(r.Context(), activeOnly)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]slaTemplateDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toSLATemplateDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BOQHandler) createSLATemplate(w http.ResponseWriter, r *http.Request) {
	var req createSLATemplateRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.uc.CreateSLATemplate(r.Context(), port.CreateSLATemplateInput{
		Key:         req.Key,
		Name:        req.Name,
		Description: req.Description,
		Details:     req.Details,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSLATemplateDTO(*t))
}

func (h *BOQHandler) updateSLATemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "sla_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateSLATemplateRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.uc.UpdateSLATemplate(r.Context(), port.UpdateSLATemplateInput{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Details:     req.Details,
		Active:      req.Active,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSLATemplateDTO(*t))
}

// =====================================================================
// Approval template handlers
// =====================================================================

func (h *BOQHandler) listApprovalTemplates(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") == "true"
	items, err := h.uc.ListApprovalTemplates(r.Context(), activeOnly)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]approvalTemplateDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toApprovalTemplateDTO(t, nil))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BOQHandler) getApprovalTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, members, err := h.uc.GetApprovalTemplate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toApprovalTemplateDTO(*t, members))
}

func (h *BOQHandler) createApprovalTemplate(w http.ResponseWriter, r *http.Request) {
	var req createApprovalTemplateRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	members := make([]port.ApprovalTemplateMemberInput, 0, len(req.Members))
	for _, m := range req.Members {
		uid, perr := parseUUIDLocal(m.UserID, "user_id")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		members = append(members, port.ApprovalTemplateMemberInput{
			UserID:  uid,
			StepNo:  m.StepNo,
			RoleTag: m.RoleTag,
		})
	}
	t, err := h.uc.CreateApprovalTemplate(r.Context(), port.CreateApprovalTemplateInput{
		Key:         req.Key,
		Name:        req.Name,
		Mode:        req.Mode,
		Description: req.Description,
		Members:     members,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Echo back with members for FE convenience.
	_, members2, _ := h.uc.GetApprovalTemplate(r.Context(), t.ID)
	httpserver.WriteJSON(w, http.StatusCreated, toApprovalTemplateDTO(*t, members2))
}

func (h *BOQHandler) updateApprovalTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateApprovalTemplateRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateApprovalTemplateInput{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Active:      req.Active,
	}
	if req.Members != nil {
		members := make([]port.ApprovalTemplateMemberInput, 0, len(*req.Members))
		for _, m := range *req.Members {
			uid, perr := parseUUIDLocal(m.UserID, "user_id")
			if perr != nil {
				httpserver.WriteError(w, perr)
				return
			}
			members = append(members, port.ApprovalTemplateMemberInput{
				UserID:  uid,
				StepNo:  m.StepNo,
				RoleTag: m.RoleTag,
			})
		}
		in.Members = &members
	}
	t, err := h.uc.UpdateApprovalTemplate(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	_, members2, _ := h.uc.GetApprovalTemplate(r.Context(), t.ID)
	httpserver.WriteJSON(w, http.StatusOK, toApprovalTemplateDTO(*t, members2))
}

func (h *BOQHandler) publishApprovalTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.uc.PublishApprovalTemplate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	_, members, _ := h.uc.GetApprovalTemplate(r.Context(), t.ID)
	httpserver.WriteJSON(w, http.StatusOK, toApprovalTemplateDTO(*t, members))
}

// =====================================================================
// BOQ handlers
// =====================================================================

func (h *BOQHandler) listBOQs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.BOQListFilter{
		Status: q.Get("status"),
		Search: q.Get("q"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUIDLocal(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.OpportunityID = &u
	}
	if s := q.Get("approval_template_id"); s != "" {
		u, err := parseUUIDLocal(s, "approval_template_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.ApprovalTemplateID = &u
	}
	boqs, total, err := h.uc.ListBOQs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]boqDTO, 0, len(boqs))
	for _, b := range boqs {
		out = append(out, toBOQDTO(b))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *BOQHandler) getBOQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	b, lines, err := h.uc.GetBOQ(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Vendor RBAC line-ownership (NFR-011 / TC-RBAC-IV-008): when the
	// actor is a vendor user, only return lines where they're the
	// assigned provider_user_id. The field-mask middleware separately
	// strips commercial fields, but THIS check prevents a vendor token
	// from enumerating lines they don't own at all.
	if IsVendorActor(r.Context()) {
		actor := actorUserIDLocal(r.Context())
		if actor == nil {
			lines = nil
		} else {
			filtered := make([]domain.BOQLine, 0, len(lines))
			for _, l := range lines {
				if l.ProviderUserID != nil && *l.ProviderUserID == *actor {
					filtered = append(filtered, l)
				}
			}
			lines = filtered
		}
	}
	lineOut := make([]boqLineDTO, 0, len(lines))
	for _, l := range lines {
		lineOut = append(lineOut, toBOQLineDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"boq":   toBOQDTO(*b),
		"lines": lineOut,
	})
}

func (h *BOQHandler) createBOQ(w http.ResponseWriter, r *http.Request) {
	var req createBOQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	oppID, err := parseUUIDLocal(req.OpportunityID, "opportunity_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	pbID, err := parseUUIDLocal(req.PricebookID, "pricebook_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateBOQInput{
		OpportunityID: oppID,
		PricebookID:   pbID,
		Notes:         req.Notes,
	}
	if uid := actorUserIDLocal(r.Context()); uid != nil {
		in.CreatedBy = uid
	}
	b, err := h.uc.CreateBOQ(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBOQDTO(*b))
}

func (h *BOQHandler) updateBOQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateBOQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	b, err := h.uc.UpdateBOQ(r.Context(), port.UpdateBOQInput{
		ID:         id,
		Notes:      req.Notes,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBOQDTO(*b))
}

func (h *BOQHandler) submitBOQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req submitBOQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	aptID, err := parseUUIDLocal(req.ApprovalTemplateID, "approval_template_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	b, instances, err := h.uc.SubmitBOQ(r.Context(), port.SubmitBOQInput{
		BOQVersionID:       id,
		ApprovalTemplateID: aptID,
		IfRevision:         req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	insts := make([]approvalInstanceDTO, 0, len(instances))
	for _, ai := range instances {
		insts = append(insts, toApprovalInstanceDTO(ai))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"boq":                 toBOQDTO(*b),
		"approval_instances":  insts,
	})
}

func (h *BOQHandler) startRevision(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	b, err := h.uc.StartRevision(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBOQDTO(*b))
}

// =====================================================================
// BOQ line handlers
// =====================================================================

func (h *BOQHandler) createBOQLine(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createBOQLineRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	plID, err := parseUUIDLocal(req.PricebookLineID, "pricebook_line_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	slaID, err := parseUUIDLocal(req.SLATemplateID, "sla_template_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	l, err := h.uc.CreateBOQLine(r.Context(), port.CreateBOQLineInput{
		BOQVersionID:    boqID,
		PricebookLineID: plID,
		SLATemplateID:   slaID,
		Quantity:        req.Quantity,
		Notes:           req.Notes,
		SortOrder:       req.SortOrder,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBOQLineDTO(*l))
}

func (h *BOQHandler) updateBOQLine(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq_line")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateBOQLineRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateBOQLineInput{
		ID:              id,
		Quantity:        req.Quantity,
		SellUnitPrice:   req.SellUnitPrice,
		LineDiscountPct: req.LineDiscountPct,
		Notes:           req.Notes,
		SortOrder:       req.SortOrder,
	}
	if req.AssignedProviderCompanyID != nil {
		u, perr := uuidPtr(*req.AssignedProviderCompanyID)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("boq_line.provider_company_invalid", "assigned_provider_company_id is not a valid uuid"))
			return
		}
		in.AssignedProviderCompanyID = u
	}
	if req.ProviderUserID != nil {
		u, perr := uuidPtr(*req.ProviderUserID)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("boq_line.provider_user_invalid", "provider_user_id is not a valid uuid"))
			return
		}
		in.ProviderUserID = u
	}
	if req.SLATemplateID != nil {
		u, perr := uuidPtr(*req.SLATemplateID)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("boq_line.sla_template_invalid", "sla_template_id is not a valid uuid"))
			return
		}
		in.SLATemplateID = u
	}
	l, err := h.uc.UpdateBOQLine(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBOQLineDTO(*l))
}

func (h *BOQHandler) deleteBOQLine(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq_line")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.DeleteBOQLine(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BOQHandler) setVendorCost(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "boq_line")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req setVendorCostRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("auth.no_actor", "no authenticated actor"))
		return
	}
	l, err := h.uc.SetVendorCost(r.Context(), port.SetVendorCostInput{
		LineID:         id,
		VendorUnitCost: req.VendorUnitCost,
		ActorUserID:    *actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBOQLineDTO(*l))
}

// =====================================================================
// Approval instance handlers
// =====================================================================

func (h *BOQHandler) listApprovalInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ApprovalInstanceListFilter{
		Limit:  parseIntDefaultLocal(q.Get("page_size"), 100),
		Offset: 0,
	}
	if q.Get("pending_for_me") == "true" {
		uid := actorUserIDLocal(r.Context())
		if uid != nil {
			f.PendingForUserID = uid
		}
	}
	if s := q.Get("boq_id"); s != "" {
		u, err := parseUUIDLocal(s, "boq_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.BOQVersionID = &u
	}
	items, err := h.uc.ListApprovalInstances(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]approvalInstanceDTO, 0, len(items))
	for _, a := range items {
		out = append(out, toApprovalInstanceDTO(a))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BOQHandler) approveStep(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_instance")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("auth.no_actor", "no authenticated actor"))
		return
	}
	ai, b, err := h.uc.ApproveStep(r.Context(), port.ApprovalActionInput{
		InstanceID:  id,
		ActorUserID: *actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"instance": toApprovalInstanceDTO(*ai),
		"boq":      toBOQDTO(*b),
	})
}

func (h *BOQHandler) rejectStep(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_instance")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req approvalActionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("auth.no_actor", "no authenticated actor"))
		return
	}
	ai, b, err := h.uc.RejectStep(r.Context(), port.ApprovalActionInput{
		InstanceID:  id,
		ActorUserID: *actor,
		ReasonCode:  req.ReasonCode,
		Comment:     req.Comment,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"instance": toApprovalInstanceDTO(*ai),
		"boq":      toBOQDTO(*b),
	})
}

// reassignStep — E3 pre-launch. Hands a still-pending approval step
// off to a different user. Audit-friendly: reason mandatory, both
// previous + new approver are notified.
func (h *BOQHandler) reassignStep(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval_instance")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req reassignStepRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	newApprover, err := parseUUIDLocal(req.NewApproverUserID, "new_approver_user_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	ai, err := h.uc.ReassignStep(r.Context(), port.ReassignStepInput{
		InstanceID:  id,
		NewApprover: newApprover,
		ActorUserID: actor,
		Reason:      req.Reason,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toApprovalInstanceDTO(*ai))
}

type reassignStepRequest struct {
	NewApproverUserID string `json:"new_approver_user_id"`
	Reason            string `json:"reason"`
}

// =====================================================================
// Local helpers — kept in this file (not shared with handler.go) so
// the Phase-3 surface is self-contained.
// =====================================================================

func parseUUIDLocal(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(field+".id_invalid", field+" is not a valid uuid")
	}
	return id, nil
}

func parseIntDefaultLocal(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func actorUserIDLocal(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}
