package http

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// mountCoverage attaches the coverage / polygon / KMZ / impact routes
// to the existing authenticated group inside Mount(). Kept in its own
// file so handler.go doesn't balloon.
func (h *Handler) mountCoverage(r chi.Router) {
	// Coverage check — read-permission gated. Used by sales/self-order at
	// pin time, by NOC for "is this address covered?" checks.
	r.With(httpserver.RequirePermission("network.topology.read")).
		Post("/coverage/check", h.coverageCheck)

	// Per-node polygon read / write / clear.
	r.With(httpserver.RequirePermission("network.topology.read")).
		Get("/nodes/{id}/polygon", h.getPolygon)
	r.With(httpserver.RequirePermission("network.topology.manage")).
		Put("/nodes/{id}/polygon", h.savePolygon)
	r.With(httpserver.RequirePermission("network.topology.manage")).
		Delete("/nodes/{id}/polygon", h.clearPolygon)

	// KMZ / KML import. Preview parses + returns placemarks (no DB write).
	// Apply commits with an explicit placemark→node assignment map.
	r.With(httpserver.RequirePermission("network.odp.manage")).
		Post("/coverage/import-kmz/preview", h.kmzPreview)
	r.With(httpserver.RequirePermission("network.odp.manage")).
		Post("/coverage/import-kmz/apply", h.kmzApply)

	// Downstream impact (read).
	r.With(httpserver.RequirePermission("network.topology.read")).
		Get("/nodes/{id}/impact", h.impact)
}

// DTOs (coverageCheckRequest, coverageResultDTO, savePolygonRequest, kmzApplyRequest, impactResponse, …) live in dto.go.

// =====================================================================
// Coverage check
// =====================================================================

func (h *Handler) coverageCheck(w http.ResponseWriter, r *http.Request) {
	var req coverageCheckRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out, err := h.uc.CheckCoverage(r.Context(), port.CoverageCheckInput{
		Lat:           req.Lat,
		Lng:           req.Lng,
		OnlyAvailable: req.OnlyAvailable,
		MaxCandidates: req.MaxCandidates,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := coverageResultDTO{
		Verdict:          string(out.Verdict),
		MaxCableRunM:     out.MaxCableRunM,
		CableRouteFactor: out.CableRouteFactor,
		ExcessPricePerM:  out.ExcessPricePerM,
		OtherCandidates:  []coverageCandidateDTO{},
	}
	if out.BestCandidate != nil {
		d := toCandidateDTO(*out.BestCandidate)
		resp.BestCandidate = &d
	}
	for _, c := range out.OtherCandidates {
		resp.OtherCandidates = append(resp.OtherCandidates, toCandidateDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

// =====================================================================
// Polygon read/write
// =====================================================================

func (h *Handler) getPolygon(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	poly, err := h.uc.GetNodePolygon(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if poly == nil {
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"polygon": nil})
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"polygon": poly})
}

func (h *Handler) savePolygon(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	var req savePolygonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.SaveNodePolygon(r.Context(), port.SaveCoveragePolygonInput{
		NodeID:  id,
		Polygon: req.Polygon,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) clearPolygon(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	if err := h.uc.ClearNodePolygon(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// KMZ import
// =====================================================================

// kmzPreview accepts the KMZ/KML body — either multipart form file or raw
// application/octet-stream body. Returns the parsed placemarks so the FE
// can show "found 8 polygons; pick which node each maps to".
func (h *Handler) kmzPreview(w http.ResponseWriter, r *http.Request) {
	body, err := readUploadBody(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	preview, err := h.uc.PreviewKMZ(r.Context(), body)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, preview)
}

func (h *Handler) kmzApply(w http.ResponseWriter, r *http.Request) {
	var req kmzApplyRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	assignments := make(map[string]uuid.UUID, len(req.Assignments))
	for name, s := range req.Assignments {
		id, err := uuid.Parse(s)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("kmz.node_id_invalid", "assignment node id is not a valid uuid: "+name))
			return
		}
		assignments[name] = id
	}
	n, err := h.uc.ApplyKMZ(r.Context(), port.KMZImportApply{
		Polygons:    req.Placemarks,
		Assignments: assignments,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, kmzApplyResponse{Applied: n})
}

// readUploadBody handles either multipart (file field "kmz") or raw body.
// The FE's simple FormData upload uses multipart; raw is for curl / scripts.
func readUploadBody(r *http.Request) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if len(ct) >= 19 && ct[:19] == "multipart/form-data" {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, errors.Validation("kmz.multipart", "could not parse multipart body")
		}
		f, _, err := r.FormFile("kmz")
		if err != nil {
			return nil, errors.Validation("kmz.field_missing", "missing 'kmz' file field")
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, 32<<20))
		if err != nil {
			return nil, errors.Validation("kmz.read", "could not read upload")
		}
		return body, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return nil, errors.Validation("kmz.read", "could not read body")
	}
	return body, nil
}

// =====================================================================
// Downstream impact
// =====================================================================

func (h *Handler) impact(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("node.id_invalid", "id is not a valid uuid"))
		return
	}
	res, err := h.uc.DownstreamImpact(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := impactResponse{
		RootID:       res.RootID.String(),
		RootName:     res.RootName,
		CustomersHit: res.CustomersHit,
		Nodes:        make([]impactRowDTO, 0, len(res.Nodes)),
	}
	for _, n := range res.Nodes {
		row := impactRowDTO{
			NodeID:        n.NodeID.String(),
			Name:          n.Name,
			Code:          n.Code,
			NodeTypeKey:   n.NodeTypeKey,
			NodeTypeLabel: n.NodeTypeLabel,
			Depth:         n.Depth,
			ParentName:    n.ParentName,
		}
		if n.ParentID != nil {
			s := n.ParentID.String()
			row.ParentID = &s
		}
		resp.Nodes = append(resp.Nodes, row)
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}
