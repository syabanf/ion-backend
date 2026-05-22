// Package http is the driving adapter for the field bounded context.
package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
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
//	Work orders
//	  GET   /work-orders                          [field.wo.read]
//	  GET   /work-orders/{id}                     [field.wo.read]
//	  POST  /work-orders                          [field.wo.create]
//	  POST  /work-orders/{id}/route               [field.wo.assign]
//	  POST  /work-orders/{id}/assign              [field.wo.assign]
//	  POST  /work-orders/{id}/status              [field.wo.update]
//	  POST  /work-orders/{id}/checklist           [field.wo.update]
//	  POST  /work-orders/{id}/resolution          [field.wo.update]
//	  POST  /work-orders/{id}/bast                [field.wo.submit_bast]
//	  POST  /basts/{id}/verify                    [field.bast.noc_verify]
//
//	Teams
//	  GET   /teams                                [field.team.read]
//	  GET   /teams/{id}                           [field.team.read]
//	  POST  /teams                                [field.team.manage]
//	  GET   /teams/{id}/members                   [field.team.read]
//	  POST  /teams/{id}/members                   [field.team.manage]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("field.wo.read")).Get("/work-orders", h.listWOs)
		r.With(httpserver.RequirePermission("field.wo.read")).Get("/work-orders/{id}", h.getWO)
		r.With(httpserver.RequirePermission("field.wo.create")).Post("/work-orders", h.createWO)
		r.With(httpserver.RequirePermission("field.wo.assign")).Post("/work-orders/{id}/route", h.routeWO)
		r.With(httpserver.RequirePermission("field.wo.assign")).Post("/work-orders/{id}/assign", h.assignWO)
		r.With(httpserver.RequirePermission("field.wo.update")).Post("/work-orders/{id}/status", h.statusWO)
		r.With(httpserver.RequirePermission("field.wo.update")).Post("/work-orders/{id}/checklist", h.submitChecklist)
		r.With(httpserver.RequirePermission("field.wo.update")).Post("/work-orders/{id}/resolution", h.addResolution)
		r.With(httpserver.RequirePermission("field.wo.submit_bast")).Post("/work-orders/{id}/bast", h.submitBAST)

		r.With(httpserver.RequirePermission("field.bast.noc_verify")).Post("/basts/{id}/verify", h.verifyBAST)

		// M5 r2 — OTP verification for remote sign-off.
		r.With(httpserver.RequirePermission("field.wo.submit_bast")).Post("/basts/{id}/verify-otp", h.verifyBASTOTP)

		r.With(httpserver.RequirePermission("field.team.read")).Get("/teams", h.listTeams)
		r.With(httpserver.RequirePermission("field.team.read")).Get("/teams/{id}", h.getTeam)
		r.With(httpserver.RequirePermission("field.team.manage")).Post("/teams", h.createTeam)
		r.With(httpserver.RequirePermission("field.team.read")).Get("/teams/{id}/members", h.listTeamMembers)
		r.With(httpserver.RequirePermission("field.team.manage")).Post("/teams/{id}/members", h.addTeamMember)

		// M5 r2 — Reschedule + SLA view.
		r.With(httpserver.RequirePermission("field.wo.reschedule")).Post("/work-orders/{id}/reschedule", h.rescheduleWO)
		r.With(httpserver.RequirePermission("field.wo.read")).Get("/work-orders/{id}/reschedules", h.listReschedules)
		r.With(httpserver.RequirePermission("field.sla.read")).Get("/work-orders/sla-breaches", h.listSLABreaches)

		// M5 r3 — ONT config (active-WO gate enforced in the usecase).
		r.With(httpserver.RequirePermission("field.wo.read")).Get("/work-orders/{id}/ont-config", h.getONTConfig)
	})
}

// DTOs (woDTO, woDetailDTO, assignmentDTO, checklistItemDTO,
// checklistResponseDTO, resolutionItemDTO, bastDTO, teamDTO,
// teamMemberDTO, request shapes, …) live in dto.go.

// =====================================================================
// WO handlers
// =====================================================================

func (h *Handler) listWOs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntDefault(q.Get("page_size"), 50)
	page := parseIntDefault(q.Get("page"), 1)
	f := port.WOListFilter{
		Status: q.Get("status"),
		Search: q.Get("q"),
		Limit:  limit,
		Offset: (page - 1) * limit,
	}
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("wo.branch_invalid", "branch_id is not a uuid"))
			return
		}
		f.BranchID = &id
	}
	if v := q.Get("team_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("wo.team_invalid", "team_id is not a uuid"))
			return
		}
		f.TeamID = &id
	}
	out, total, err := h.uc.ListWOs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]woDTO, 0, len(out))
	for _, d := range out {
		items = append(items, toWODTO(d))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getWO(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	d, err := h.uc.GetWO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODetailDTO(*d))
}

func (h *Handler) createWO(w http.ResponseWriter, r *http.Request) {
	var req createWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.order_invalid", "order_id is not a uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	// Wave 71 — pre-cast enum validation. An invalid priority used to
	// flow through the cast unchecked, fail the DB CHECK on insert, and
	// surface to the client as a 500. The Valid() guard short-circuits
	// to a clean 400 with the offending value cited.
	priority := domain.Priority(req.Priority)
	if req.Priority != "" && !priority.Valid() {
		httpserver.WriteError(w, errors.Validation("wo.priority_invalid",
			"priority must be high|medium|low"))
		return
	}
	in := port.CreateWOFromOrderInput{
		OrderID:   orderID,
		Priority:  priority,
		Notes:     req.Notes,
		CreatedBy: c.UserID,
	}
	if req.ScheduledDate != nil {
		if t, err := time.Parse(time.RFC3339, *req.ScheduledDate); err == nil {
			in.ScheduledDate = &t
		}
	}
	d, err := h.uc.CreateWOFromOrder(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toWODetailDTO(*d))
}

func (h *Handler) routeWO(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req routeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	teamID, err := uuid.Parse(req.TeamID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.team_invalid", "team_id is not a uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	d, err := h.uc.RouteToTeam(r.Context(), port.RouteToTeamInput{
		WOID: id, TeamID: teamID, By: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODetailDTO(*d))
}

func (h *Handler) assignWO(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req assignRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	leadID, err := uuid.Parse(req.LeadID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("assign.lead_invalid", "lead_id is not a uuid"))
		return
	}
	leadGrade := domain.TechGrade(req.LeadGrade)
	if !leadGrade.Valid() {
		httpserver.WriteError(w, errors.Validation("assign.lead_grade_invalid",
			"lead_grade must be senior|junior"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	in := port.AssignTechniciansInput{
		WOID:       id,
		LeadID:     leadID,
		LeadGrade:  leadGrade,
		AssignedBy: c.UserID,
	}
	if req.ObserverID != nil && *req.ObserverID != "" {
		oid, err := uuid.Parse(*req.ObserverID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("assign.observer_invalid", "observer_id is not a uuid"))
			return
		}
		in.ObserverID = &oid
		if req.ObserverGrade != nil {
			g := domain.TechGrade(*req.ObserverGrade)
			if !g.Valid() {
				httpserver.WriteError(w, errors.Validation("assign.observer_grade_invalid",
					"observer_grade must be senior|junior"))
				return
			}
			in.ObserverGrade = &g
		}
	}
	d, err := h.uc.AssignTechnicians(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODetailDTO(*d))
}

func (h *Handler) statusWO(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req statusRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	status := domain.WOStatus(req.Status)
	if !status.Valid() {
		httpserver.WriteError(w, errors.Validation("wo.status_invalid",
			"status not in the allowed set (see WOStatus enum)"))
		return
	}
	d, err := h.uc.UpdateStatus(r.Context(), port.UpdateWOStatusInput{
		WOID: id, Status: status, Notes: req.Notes, By: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODetailDTO(*d))
}

func (h *Handler) submitChecklist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req checklistRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemID, err := uuid.Parse(req.TemplateItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("checklist.item_invalid", "template_item_id is not a uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	rsp, err := h.uc.SubmitChecklistResponse(r.Context(), port.SubmitChecklistResponseInput{
		WOID: id, TemplateItemID: itemID,
		ResponseText: req.ResponseText, FileURL: req.FileURL,
		GPSLat: req.GPSLat, GPSLng: req.GPSLng, GPSAccuracyM: req.GPSAccuracyM,
		SubmittedBy: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, checklistResponseDTO{
		ID: rsp.ID.String(), TemplateItemID: rsp.TemplateItemID.String(),
		ResponseText: rsp.ResponseText, FileURL: rsp.FileURL,
		GPSLat: rsp.GPSLat, GPSLng: rsp.GPSLng, GPSAccuracyM: rsp.GPSAccuracyM,
		SubmittedAt: rsp.SubmittedAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) addResolution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req resolutionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	category := domain.ResolutionCategory(req.Category)
	if !category.Valid() {
		httpserver.WriteError(w, errors.Validation("resolution.category_invalid",
			"category not in the allowed set (config|hardware|cabling|signal|software|other)"))
		return
	}
	resStatus := domain.ResolutionStatus(req.ResolutionStatus)
	if !resStatus.Valid() {
		httpserver.WriteError(w, errors.Validation("resolution.status_invalid",
			"resolution_status not in the allowed set"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	out, err := h.uc.AddResolutionItem(r.Context(), port.AddResolutionItemInput{
		WOID: id, ItemLabel: req.ItemLabel,
		Category: category,
		Finding:  req.Finding, ActionTaken: req.ActionTaken,
		ResolutionStatus: resStatus,
		TimeSpentMinutes: req.TimeSpentMinutes,
		ResolvedBy:       c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, resolutionItemDTO{
		ID: out.ID.String(), ItemOrder: out.ItemOrder, ItemLabel: out.ItemLabel,
		Category:         string(out.Category),
		Finding:          out.Finding,
		ActionTaken:      out.ActionTaken,
		ResolutionStatus: string(out.ResolutionStatus),
		TimeSpentMinutes: out.TimeSpentMinutes,
		LoggedAt:         out.LoggedAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) submitBAST(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req bastRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	mode := domain.SignOffMode(req.SignOffMode)
	if mode == "" {
		mode = domain.SignOffOnSite
	}
	if !mode.Valid() {
		httpserver.WriteError(w, errors.Validation("bast.sign_off_mode_invalid",
			"sign_off_mode must be on_site|remote"))
		return
	}
	b, err := h.uc.SubmitBAST(r.Context(), port.SubmitBASTInput{
		WOID:           id,
		SignOffMode:    mode,
		CustomerSigURL: req.CustomerSigURL,
		OTPUsed:        req.OTPUsed,
		GPSLat:         req.GPSLat,
		GPSLng:         req.GPSLng,
		SubmittedBy:    c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBASTDTO(b))
}

func (h *Handler) verifyBAST(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("bast.id_invalid", "id is not a uuid"))
		return
	}
	var req verifyBASTRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	decision := domain.NOCStatus(req.Decision)
	// Only approved|rejected are valid here — pending is the default
	// state at row creation, not a verify-time outcome. Both Valid()
	// values + the explicit subset check defend against silent 500s.
	if !decision.Valid() || decision == domain.NOCStatusPending {
		httpserver.WriteError(w, errors.Validation("bast.decision_invalid",
			"decision must be approved|rejected"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	b, err := h.uc.VerifyBAST(r.Context(), port.VerifyBASTInput{
		BASTID: id, Decision: decision,
		Notes: req.Notes, VerifiedBy: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBASTDTO(b))
}

// =====================================================================
// Team handlers
// =====================================================================

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var branchID *uuid.UUID
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("team.branch_invalid", "branch_id is not a uuid"))
			return
		}
		branchID = &id
	}
	out, err := h.uc.ListTeams(r.Context(), branchID, q.Get("active_only") == "true")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]teamDTO, 0, len(out))
	for _, t := range out {
		items = append(items, toTeamDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getTeam(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("team.id_invalid", "id is not a uuid"))
		return
	}
	t, err := h.uc.GetTeam(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTeamDTO(*t))
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	var req createTeamRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	bID, err := uuid.Parse(req.BranchID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("team.branch_invalid", "branch_id is not a uuid"))
		return
	}
	in := port.CreateTeamInput{Code: req.Code, Name: req.Name, BranchID: bID}
	if req.TeamLeaderID != nil && *req.TeamLeaderID != "" {
		lid, err := uuid.Parse(*req.TeamLeaderID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("team.leader_invalid", "team_leader_id is not a uuid"))
			return
		}
		in.TeamLeaderID = &lid
	}
	t, err := h.uc.CreateTeam(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toTeamDTO(*t))
}

func (h *Handler) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("team.id_invalid", "id is not a uuid"))
		return
	}
	out, err := h.uc.ListTeamMembers(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]teamMemberDTO, 0, len(out))
	for _, m := range out {
		items = append(items, toTeamMemberDTO(m))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) addTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("team.id_invalid", "id is not a uuid"))
		return
	}
	var req addMemberRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	uid, err := uuid.Parse(req.UserID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("member.user_invalid", "user_id is not a uuid"))
		return
	}
	grade := domain.TechGrade(req.Grade)
	if grade != domain.GradeSenior && grade != domain.GradeJunior {
		httpserver.WriteError(w, errors.Validation("member.grade_invalid", "grade must be senior or junior"))
		return
	}
	v, err := h.uc.AddTeamMember(r.Context(), port.AddTeamMemberInput{
		TeamID: teamID, UserID: uid, Grade: grade,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toTeamMemberDTO(*v))
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
