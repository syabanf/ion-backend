package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// mountAdmin attaches admin/management routes onto the authenticated group.
// Called from Mount() — kept in its own file so the auth handler doesn't
// balloon as new admin surfaces are added.
func (h *Handler) mountAdmin(r chi.Router) {
	// Dashboard
	r.With(httpserver.RequirePermission("identity.user.read", "identity.branch.read")).
		Get("/dashboard/stats", h.dashboardStats)

	// Users list / activate-deactivate (CRUD create already exists)
	r.With(httpserver.RequirePermission("identity.user.read")).
		Get("/users", h.listUsers)

	r.With(httpserver.RequirePermission("identity.user.deactivate")).
		Post("/users/{id}/deactivate", h.deactivateUser)

	r.With(httpserver.RequirePermission("identity.user.update")).
		Post("/users/{id}/activate", h.activateUser)

	// Branches
	r.With(httpserver.RequirePermission("identity.branch.read")).
		Get("/branches", h.listBranches)

	r.With(httpserver.RequirePermission("identity.branch.manage")).
		Post("/branches", h.createBranch)

	r.With(httpserver.RequirePermission("identity.branch.manage")).
		Patch("/branches/{id}", h.updateBranch)

	// Wave 68 — branch geo + per-branch operational config fetch.
	// Lives on a separate route so the list/getBranch path stays a
	// cheap projection; the detail/edit page pulls extra data only
	// when the operator opens that surface.
	r.With(httpserver.RequirePermission("identity.branch.read")).
		Get("/branches/{id}/config", h.getBranchConfig)

	// Audit
	r.With(httpserver.RequirePermission("identity.audit.read")).
		Get("/audit-logs", h.listAudit)

	// Platform config
	r.With(httpserver.RequirePermission("admin.platform_config.read")).
		Get("/platform-config", h.listPlatformConfig)

	r.With(httpserver.RequirePermission("admin.platform_config.manage")).
		Put("/platform-config/{key}", h.updatePlatformConfig)
}

// =====================================================================
// Dashboard
// =====================================================================

func (h *Handler) dashboardStats(w http.ResponseWriter, r *http.Request) {
	out, err := h.uc.GetDashboardStats(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := dashboardStatsResponse{
		UsersTotal:       out.UsersTotal,
		UsersActive:      out.UsersActive,
		BranchesTotal:    out.BranchesTotal,
		BranchesByLevel:  out.BranchesByLevel,
		RolesTotal:       out.RolesTotal,
		PermissionsTotal: out.PermissionsTotal,
		RecentAudits:     toAuditEntryDTOs(out.RecentAudits),
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

// =====================================================================
// Users
// =====================================================================

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.UserListFilter{
		Search: q.Get("q"),
		Role:   q.Get("role"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 25),
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	f.Offset = (page - 1) * f.Limit

	if bid := q.Get("branch_id"); bid != "" {
		id, err := uuid.Parse(bid)
		if err == nil {
			f.BranchID = &id
		}
	}
	if a := q.Get("active"); a != "" {
		v := a == "true" || a == "1"
		f.Active = &v
	}

	items, total, err := h.uc.ListUsers(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	out := make([]userListItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, userListItemDTO{
			User:  toUserDTO(it.User),
			Roles: it.Roles,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, paginatedUsers{
		Items: out,
		Total: total,
		Page:  page,
		Size:  f.Limit,
	})
}

func (h *Handler) deactivateUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("user.id_invalid", "id is not a valid uuid"))
		return
	}
	if err := h.uc.SetUserActive(r.Context(), id, false); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) activateUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("user.id_invalid", "id is not a valid uuid"))
		return
	}
	if err := h.uc.SetUserActive(r.Context(), id, true); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// Branches
// =====================================================================

func (h *Handler) listBranches(w http.ResponseWriter, r *http.Request) {
	bs, err := h.uc.ListBranches(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]branchDTO, 0, len(bs))
	for _, b := range bs {
		out = append(out, toBranchDTO(b))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) createBranch(w http.ResponseWriter, r *http.Request) {
	var req createBranchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateBranchInput{
		Name:  req.Name,
		Code:  req.Code,
		Level: domain.BranchLevel(req.Level),
	}
	if req.ParentID != nil && *req.ParentID != "" {
		id, err := uuid.Parse(*req.ParentID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("branch.parent_id_invalid", "parent_id is not a valid uuid"))
			return
		}
		in.ParentID = &id
	}
	b, err := h.uc.CreateBranch(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBranchDTO(*b))
}

// Wave 68 — exposes the geo_shape (as GeoJSON) + odp_strategy +
// cable_distance + wo_auto_assign + sla_* columns for the branch
// editor. We read directly off the pool to avoid bloating the
// branch domain entity (which models only the "always-on" fields).
func (h *Handler) getBranchConfig(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("branch.id_invalid", "id is not a valid uuid"))
		return
	}
	cfg, err := h.uc.GetBranchConfig(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":                     id.String(),
		"geo_shape_geojson":      cfg.GeoShapeGeoJSON,
		"odp_strategy":           cfg.ODPStrategy,
		"cable_distance":         cfg.CableDistance,
		"wo_auto_assign":         cfg.WOAutoAssign,
		"sla_assignment_minutes": cfg.SLAAssignmentMinutes,
		"sla_dispatch_minutes":   cfg.SLADispatchMinutes,
		"sla_install_minutes":    cfg.SLAInstallMinutes,
	})
}

func (h *Handler) updateBranch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("branch.id_invalid", "id is not a valid uuid"))
		return
	}
	var req updateBranchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	b, err := h.uc.UpdateBranch(r.Context(), port.UpdateBranchInput{
		ID:     id,
		Name:   req.Name,
		Active: req.Active,
		// Wave 68 — pass through all the patch fields.
		GeoShapeGeoJSON:           req.GeoShapeGeoJSON,
		GeoShapeClear:             req.GeoShapeClear,
		ODPStrategy:               req.ODPStrategy,
		ODPStrategyClear:          req.ODPStrategyClear,
		CableDistance:             req.CableDistance,
		CableDistanceClear:        req.CableDistanceClear,
		WOAutoAssign:              req.WOAutoAssign,
		WOAutoAssignClear:         req.WOAutoAssignClear,
		SLAAssignmentMinutes:      req.SLAAssignmentMinutes,
		SLAAssignmentMinutesClear: req.SLAAssignmentMinutesClear,
		SLADispatchMinutes:        req.SLADispatchMinutes,
		SLADispatchMinutesClear:   req.SLADispatchMinutesClear,
		SLAInstallMinutes:         req.SLAInstallMinutes,
		SLAInstallMinutesClear:    req.SLAInstallMinutesClear,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBranchDTO(*b))
}

// =====================================================================
// Audit
// =====================================================================

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.AuditFilter{
		Module:     q.Get("module"),
		RecordType: q.Get("record_type"),
		Limit:      httpserver.ParseIntDefault(q.Get("page_size"), 25),
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	f.Offset = (page - 1) * f.Limit

	if uid := q.Get("user_id"); uid != "" {
		id, err := uuid.Parse(uid)
		if err == nil {
			f.UserID = &id
		}
	}
	if from := q.Get("from"); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			f.From = &t
		}
	}
	if to := q.Get("to"); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			f.To = &t
		}
	}

	items, total, err := h.uc.ListAuditEntries(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, paginatedAudit{
		Items: toAuditEntryDTOs(items),
		Total: total,
		Page:  page,
		Size:  f.Limit,
	})
}

// =====================================================================
// Platform Config
// =====================================================================

func (h *Handler) listPlatformConfig(w http.ResponseWriter, r *http.Request) {
	cs, err := h.uc.ListPlatformConfig(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]configDTO, 0, len(cs))
	for _, c := range cs {
		var ub *string
		if c.UpdatedBy != nil {
			s := c.UpdatedBy.String()
			ub = &s
		}
		out = append(out, configDTO{
			Key:       c.Key,
			Value:     c.Value,
			UpdatedBy: ub,
			UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) updatePlatformConfig(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		httpserver.WriteError(w, errors.Validation("config.key_required", "config key is required"))
		return
	}
	var req updateConfigRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.uc.UpdatePlatformConfig(r.Context(), key, req.Value, c.UserID); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// helpers
// =====================================================================

