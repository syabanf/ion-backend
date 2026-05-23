// Package http is the driving adapter for the identity context.
// It converts HTTP requests to port.UseCase calls and writes results back.
//
// No business logic lives here — only translation, validation of wire-level
// shapes, and status code selection (delegated to pkg/httpserver.WriteError).
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	uc       port.UseCase
	verifier *auth.Verifier
	loginRL  *httpserver.RateLimit // optional; throttles /auth/login
}

// NewHandler constructs the identity HTTP adapter. The verifier is held
// here so the handler can mount its own auth middleware on protected
// sub-routes; callers don't have to wire it externally.
func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// WithLoginRateLimit attaches a per-IP rate limiter to /auth/login.
// Pass nil to disable. Optional — useful in tests where we don't want
// throttling.
func (h *Handler) WithLoginRateLimit(rl *httpserver.RateLimit) *Handler {
	h.loginRL = rl
	return h
}

// Mount attaches the identity routes under the given router.
//
// Layout:
//   public:
//     POST /auth/login         — exchange credentials for a token pair
//     POST /auth/refresh       — rotate a refresh token
//     POST /auth/logout        — revoke a refresh token (best-effort)
//
//   authenticated:
//     GET  /auth/me            — current user + roles + permissions
//     GET  /users/{id}         — lookup user
//     POST /users              — create user           [identity.user.create]
//     PUT  /users/{id}/roles   — replace user's roles  [identity.role.assign]
//     GET  /roles              — list roles            [identity.role.read]
//     GET  /permissions        — list permission catalog [identity.permission.read]
//     GET  /roles/{id}/permissions — list perms for role [identity.role.read]
func (h *Handler) Mount(r chi.Router) {
	// Public — /auth/login optionally throttled by per-IP rate limit.
	if h.loginRL != nil {
		r.With(h.loginRL.Middleware()).Post("/auth/login", h.login)
	} else {
		r.Post("/auth/login", h.login)
	}
	r.Post("/auth/refresh", h.refresh)
	r.Post("/auth/logout", h.logout)

	// Authenticated.
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.Get("/auth/me", h.me)

		r.Get("/users/{id}", h.getUser)

		r.With(httpserver.RequirePermission("identity.user.read")).
			Get("/users/{id}/detail", h.getUserDetail)

		r.With(httpserver.RequirePermission("identity.user.create")).
			Post("/users", h.createUser)

		r.With(httpserver.RequirePermission("identity.user.update")).
			Patch("/users/{id}", h.updateUser)

		r.With(httpserver.RequirePermission("identity.role.assign")).
			Put("/users/{id}/roles", h.replaceUserRoles)

		r.With(httpserver.RequirePermission("identity.role.read")).
			Get("/roles", h.listRoles)

		r.With(httpserver.RequirePermission("identity.role.read")).
			Get("/roles/{id}", h.getRole)

		r.With(httpserver.RequirePermission("identity.role.manage")).
			Post("/roles", h.createRole)

		r.With(httpserver.RequirePermission("identity.role.manage")).
			Patch("/roles/{id}", h.updateRole)

		r.With(httpserver.RequirePermission("identity.role.manage")).
			Delete("/roles/{id}", h.deleteRole)

		r.With(httpserver.RequirePermission("identity.role.read")).
			Get("/roles/{id}/permissions", h.permissionsForRole)

		r.With(httpserver.RequirePermission("identity.role.manage")).
			Put("/roles/{id}/permissions", h.setRolePermissions)

		r.With(httpserver.RequirePermission("identity.permission.read")).
			Get("/permissions", h.listPermissions)

		// Wave 118 — composite permission bundles for the role-builder UX.
		h.mountPermissionBundles(r)

		// M5 r3 — Availability (HRIS stub)
		r.With(httpserver.RequirePermission("identity.availability.read")).
			Get("/availability/roster", h.listRoster)
		r.With(httpserver.RequirePermission("identity.availability.manage")).
			Put("/availability/{user_id}", h.setAvailability)

		// Admin surfaces (dashboard, branches, audit, platform config, users list).
		h.mountAdmin(r)
	})
}

// DTOs for this package live in dto.go (auth, users, roles, permissions).
// Conversion helpers (toUserDTO, toPermissionDTOs) live next to their
// target type for one-file traceability.

// =====================================================================
// Handlers
// =====================================================================

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Email == "" || req.Password == "" {
		httpserver.WriteError(w, errors.Validation("login.fields_required", "email and password are required"))
		return
	}

	out, err := h.uc.Login(r.Context(), port.LoginInput{
		Email:     req.Email,
		Password:  req.Password,
		UserAgent: r.UserAgent(),
		IP:        clientIP(r),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTokenPairResponse(out))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.RefreshToken == "" {
		httpserver.WriteError(w, errors.Validation("refresh.required", "refresh_token is required"))
		return
	}
	out, err := h.uc.Refresh(r.Context(), port.RefreshInput{
		RefreshToken: req.RefreshToken,
		UserAgent:    r.UserAgent(),
		IP:           clientIP(r),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTokenPairResponse(out))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		// Logout is best-effort; even a malformed body returns 204.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = h.uc.Logout(r.Context(), port.LogoutInput{RefreshToken: req.RefreshToken})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	out, err := h.uc.Me(r.Context(), c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, meResponse{
		User:        toUserDTO(out.User),
		Roles:       out.Roles,
		Permissions: out.Permissions,
	})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	in := port.CreateUserInput{
		EmployeeID: req.EmployeeID,
		FullName:   req.FullName,
		Email:      req.Email,
		Phone:      req.Phone,
		Password:   req.Password,
		RoleNames:  req.Roles,
	}

	if req.BranchID != nil {
		id, err := uuid.Parse(*req.BranchID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("user.branch_id_invalid", "branch_id is not a valid uuid"))
			return
		}
		in.BranchID = &id
	}
	if req.BranchLevel != nil {
		lvl := domain.BranchLevel(*req.BranchLevel)
		in.BranchLevel = &lvl
	}
	if req.ReportsToID != nil {
		id, err := uuid.Parse(*req.ReportsToID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("user.reports_to_invalid", "reports_to_id is not a valid uuid"))
			return
		}
		in.ReportsToID = &id
	}
	if req.SalesType != nil {
		st := domain.SalesType(*req.SalesType)
		in.SalesType = &st
	}
	if req.TechnicianGrade != nil {
		g := domain.TechnicianGrade(*req.TechnicianGrade)
		in.TechnicianGrade = &g
	}

	u, err := h.uc.CreateUser(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toUserDTO(*u))
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "user")
	if !ok {
		return
	}

	var req updateUserRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	in := port.UpdateUserInput{
		ID:             id,
		EmployeeID:     req.EmployeeID,
		FullName:       req.FullName,
		Phone:          req.Phone,
		ClearBranch:    req.ClearBranch,
		ClearReportsTo: req.ClearReportsTo,
		ClearSalesType: req.ClearSalesType,
		ClearTechGrade: req.ClearTechGrade,
	}

	if req.BranchID != nil {
		bid, err := uuid.Parse(*req.BranchID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("user.branch_id_invalid", "branch_id is not a valid uuid"))
			return
		}
		in.BranchID = &bid
	}
	if req.BranchLevel != nil {
		lvl := domain.BranchLevel(*req.BranchLevel)
		in.BranchLevel = &lvl
	}
	if req.ReportsToID != nil {
		rid, err := uuid.Parse(*req.ReportsToID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("user.reports_to_invalid", "reports_to_id is not a valid uuid"))
			return
		}
		in.ReportsToID = &rid
	}
	if req.SalesType != nil {
		st := domain.SalesType(*req.SalesType)
		in.SalesType = &st
	}
	if req.TechnicianGrade != nil {
		g := domain.TechnicianGrade(*req.TechnicianGrade)
		in.TechnicianGrade = &g
	}

	u, err := h.uc.UpdateUser(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserDTO(*u))
}

func (h *Handler) getUserDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "user")
	if !ok {
		return
	}
	d, err := h.uc.GetUserDetail(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	resp := userDetailResponse{
		User:  toUserDTO(d.User),
		Roles: d.Roles,
	}
	if d.SalesType != nil {
		s := string(*d.SalesType)
		resp.SalesType = &s
	}
	if d.TechnicianGrade != nil {
		g := string(*d.TechnicianGrade)
		resp.TechnicianGrade = &g
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "user")
	if !ok {
		return
	}
	u, err := h.uc.GetUser(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserDTO(*u))
}

func (h *Handler) replaceUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, ok := httpserver.ParseUUIDParam(w, r, "id", "user")
	if !ok {
		return
	}
	var req assignRolesRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.AssignRolesToUser(r.Context(), port.AssignRolesInput{
		UserID:    userID,
		RoleNames: req.Roles,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.uc.ListRoles(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]roleDTO, 0, len(roles))
	for _, r := range roles {
		out = append(out, roleDTO{ID: r.ID.String(), Name: r.Name, Description: r.Description})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) listPermissions(w http.ResponseWriter, r *http.Request) {
	perms, err := h.uc.ListPermissions(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": toPermissionDTOs(perms)})
}

func (h *Handler) permissionsForRole(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "role")
	if !ok {
		return
	}
	perms, err := h.uc.PermissionsForRole(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": toPermissionDTOs(perms)})
}

func (h *Handler) getRole(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "role")
	if !ok {
		return
	}
	role, err := h.uc.GetRole(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, roleDTO{
		ID:          role.ID.String(),
		Name:        role.Name,
		Description: role.Description,
	})
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	var req createRoleRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	role, err := h.uc.CreateRole(r.Context(), port.CreateRoleInput{
		Name:        req.Name,
		Description: req.Description,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, roleDTO{
		ID:          role.ID.String(),
		Name:        role.Name,
		Description: role.Description,
	})
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "role")
	if !ok {
		return
	}
	var req createRoleRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	role, err := h.uc.UpdateRole(r.Context(), port.UpdateRoleInput{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, roleDTO{
		ID:          role.ID.String(),
		Name:        role.Name,
		Description: role.Description,
	})
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "role")
	if !ok {
		return
	}
	if err := h.uc.DeleteRole(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setRolePermissions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "role")
	if !ok {
		return
	}
	var req setRolePermissionsRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	pids := make([]uuid.UUID, 0, len(req.PermissionIDs))
	for _, s := range req.PermissionIDs {
		pid, perr := uuid.Parse(s)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation(
				"role.permission_id_invalid",
				"permission_ids contains an invalid uuid",
			))
			return
		}
		pids = append(pids, pid)
	}
	if err := h.uc.SetRolePermissions(r.Context(), port.SetRolePermissionsInput{
		RoleID:        id,
		PermissionIDs: pids,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// Internal helpers
// =====================================================================

func toTokenPairResponse(out *port.LoginOutput) tokenPairResponse {
	return tokenPairResponse{
		AccessToken:      out.AccessToken,
		RefreshToken:     out.RefreshToken,
		RefreshExpiresAt: out.RefreshExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		User:             toUserDTO(out.User),
		Roles:            out.Roles,
		Permissions:      out.Permissions,
	}
}

// clientIP returns the originating IP for audit/forensic purposes.
// Chi's RealIP middleware already normalizes X-Forwarded-For; we just read
// RemoteAddr after that filter has run.
func clientIP(r *http.Request) string {
	if r.RemoteAddr == "" {
		return ""
	}
	// RemoteAddr is "host:port" — strip the port for a cleaner audit value.
	// We tolerate IPv6 by taking everything up to the last colon.
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
