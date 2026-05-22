// Package http is the driving adapter for the network bounded context.
package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
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

// Mount attaches every route under the given router.
//
//   public:   (none — network is internal-only)
//   authenticated:
//     GET    /node-types
//     POST   /node-types               [network.topology.manage]
//     PATCH  /node-types/{id}          [network.topology.manage]
//
//     GET    /nodes                    [network.topology.read]
//     GET    /nodes/{id}               [network.topology.read]
//     POST   /nodes                    [network.topology.manage]
//     PATCH  /nodes/{id}               [network.topology.manage]
//
//     GET    /nodes/{id}/ports         [network.topology.read]
//     POST   /ports/{id}/reserve       [network.topology.manage]
//     POST   /ports/{id}/activate      [network.topology.manage]
//     POST   /ports/{id}/release       [network.topology.manage]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// (pkg/httpserver mounts a public /healthz at the router root, so
		// we don't add one here — would just shadow it inside the auth group.)

		// --- Node types ---
		r.Get("/node-types", h.listNodeTypes)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Post("/node-types", h.createNodeType)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Patch("/node-types/{id}", h.updateNodeType)

		// --- Nodes ---
		r.With(httpserver.RequirePermission("network.topology.read")).
			Get("/nodes", h.listNodes)
		r.With(httpserver.RequirePermission("network.topology.read")).
			Get("/nodes/{id}", h.getNode)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Post("/nodes", h.createNode)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Patch("/nodes/{id}", h.updateNode)

		// --- Ports ---
		r.With(httpserver.RequirePermission("network.topology.read")).
			Get("/nodes/{id}/ports", h.listPorts)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Post("/ports/{id}/reserve", h.reservePort)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Post("/ports/{id}/activate", h.activatePort)
		r.With(httpserver.RequirePermission("network.topology.manage")).
			Post("/ports/{id}/release", h.releasePort)

		// --- Coverage / polygons / KMZ / impact (see coverage_handler.go) ---
		h.mountCoverage(r)
	})
}

// DTOs (nodeTypeDTO, nodeDTO, portDTO, createNodeRequest, …) live in dto.go.

// =====================================================================
// Handlers — Node types
// =====================================================================

func (h *Handler) listNodeTypes(w http.ResponseWriter, r *http.Request) {
	includeInactive := r.URL.Query().Get("include_inactive") == "true"
	out, err := h.uc.ListNodeTypes(r.Context(), includeInactive)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]nodeTypeDTO, 0, len(out))
	for _, t := range out {
		items = append(items, toNodeTypeDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createNodeType(w http.ResponseWriter, r *http.Request) {
	var req createNodeTypeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.uc.CreateNodeType(r.Context(), port.CreateNodeTypeInput{
		TypeKey: req.TypeKey, Label: req.Label, Description: req.Description,
		IconOnline: req.IconOnline, IconOffline: req.IconOffline, IconTrouble: req.IconTrouble,
		SortOrder: req.SortOrder, HasCoverageArea: req.HasCoverageArea,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toNodeTypeDTO(*t))
}

func (h *Handler) updateNodeType(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node_type.id_invalid", "id is not a valid uuid"))
		return
	}
	var req updateNodeTypeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.uc.UpdateNodeType(r.Context(), port.UpdateNodeTypeInput{
		ID: id, Label: req.Label, Description: req.Description,
		IconOnline: req.IconOnline, IconOffline: req.IconOffline, IconTrouble: req.IconTrouble,
		SortOrder: req.SortOrder, Active: req.Active, HasCoverageArea: req.HasCoverageArea,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNodeTypeDTO(*t))
}

// =====================================================================
// Handlers — Nodes
// =====================================================================

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.NodeListFilter{
		Search: q.Get("q"),
		Status: q.Get("status"),
		Limit:  parseIntDefault(q.Get("page_size"), 25),
	}
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	f.Offset = (page - 1) * f.Limit

	if v := q.Get("node_type_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.NodeTypeID = &id
		}
	}
	if v := q.Get("branch_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.BranchID = &id
		}
	}
	if v := q.Get("parent_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.ParentID = &id
		}
	}
	if v := q.Get("active"); v != "" {
		b := v == "true" || v == "1"
		f.Active = &b
	}

	items, total, err := h.uc.ListNodes(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	dtos := make([]nodeDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, toNodeDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     dtos,
		"total":     total,
		"page":      page,
		"page_size": f.Limit,
	})
}

func (h *Handler) getNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	it, err := h.uc.GetNode(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNodeDTO(*it))
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request) {
	var req createNodeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	typeID, err := uuid.Parse(req.NodeTypeID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.type_id_invalid", "node_type_id is not a valid uuid"))
		return
	}
	in := port.CreateNodeInput{
		NodeTypeID: typeID,
		Name:       req.Name,
		Code:       req.Code,
		Address:    req.Address,
		GPSLat:     req.GPSLat,
		GPSLng:     req.GPSLng,
		CoverageRadiusM: req.CoverageRadiusM,
		TotalPorts:      req.TotalPorts,
		PortRole:        domain.PortRole(req.PortRole),
		Metadata:        req.Metadata,
	}
	if req.ParentID != nil {
		if id, err := uuid.Parse(*req.ParentID); err == nil {
			in.ParentID = &id
		}
	}
	if req.UpstreamPortID != nil {
		if id, err := uuid.Parse(*req.UpstreamPortID); err == nil {
			in.UpstreamPortID = &id
		}
	}
	if req.BranchID != nil {
		if id, err := uuid.Parse(*req.BranchID); err == nil {
			in.BranchID = &id
		}
	}

	n, err := h.uc.CreateNode(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Re-fetch via the list shape so the response carries joined fields
	// (port counts, type label, branch name) — keeps it identical to GET /:id.
	it, err := h.uc.GetNode(r.Context(), n.ID)
	if err != nil {
		// Created successfully but couldn't re-read — still a successful create.
		httpserver.WriteJSON(w, http.StatusCreated, toBareNodeDTO(*n))
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toNodeDTO(*it))
}

func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	var req updateNodeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	in := port.UpdateNodeInput{
		ID:              id,
		Name:            req.Name,
		ClearParent:     req.ClearParent,
		ClearUpstream:   req.ClearUpstream,
		ClearBranch:     req.ClearBranch,
		Address:         req.Address,
		GPSLat:          req.GPSLat,
		GPSLng:          req.GPSLng,
		ClearGPS:        req.ClearGPS,
		CoverageRadiusM: req.CoverageRadiusM,
		ClearCoverage:   req.ClearCoverage,
		Active:          req.Active,
		Metadata:        req.Metadata,
	}
	if req.ParentID != nil {
		if pid, err := uuid.Parse(*req.ParentID); err == nil {
			in.ParentID = &pid
		}
	}
	if req.UpstreamPortID != nil {
		if pid, err := uuid.Parse(*req.UpstreamPortID); err == nil {
			in.UpstreamPortID = &pid
		}
	}
	if req.BranchID != nil {
		if pid, err := uuid.Parse(*req.BranchID); err == nil {
			in.BranchID = &pid
		}
	}
	if req.Status != nil {
		st := domain.NodeStatus(*req.Status)
		in.Status = &st
	}

	n, err := h.uc.UpdateNode(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBareNodeDTO(*n))
}

// =====================================================================
// Handlers — Ports
// =====================================================================

func (h *Handler) listPorts(w http.ResponseWriter, r *http.Request) {
	nodeID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	out, err := h.uc.ListPortsForNode(r.Context(), nodeID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]portDTO, 0, len(out))
	for _, p := range out {
		items = append(items, toPortDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) reservePort(w http.ResponseWriter, r *http.Request) {
	portID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("port.id_invalid", "id is not a valid uuid"))
		return
	}
	var req reservePortRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cid, err := uuid.Parse(req.CustomerID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("port.customer_id_invalid", "customer_id is not a valid uuid"))
		return
	}
	p, err := h.uc.ReservePort(r.Context(), portID, cid, req.HoldSeconds)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPortDTO(*p))
}

func (h *Handler) activatePort(w http.ResponseWriter, r *http.Request) {
	portID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("port.id_invalid", "id is not a valid uuid"))
		return
	}
	var req activatePortRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cid, err := uuid.Parse(req.CustomerID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("port.customer_id_invalid", "customer_id is not a valid uuid"))
		return
	}
	p, err := h.uc.ActivatePort(r.Context(), portID, cid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPortDTO(*p))
}

func (h *Handler) releasePort(w http.ResponseWriter, r *http.Request) {
	portID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("port.id_invalid", "id is not a valid uuid"))
		return
	}
	p, err := h.uc.ReleasePort(r.Context(), portID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPortDTO(*p))
}

// helpers
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
