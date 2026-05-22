// Round-2 HTTP handlers: thresholds, alerts, opname workflow.
// Pulled into its own file so the r1 handler stays grep-friendly.
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (setThresholdRequest, alertDTO, opnameSessionDTO, …) live in dto.go.

// =====================================================================
// Thresholds — PUT /warehouses/{id}/items/{itemId}/threshold
// =====================================================================

func (h *Handler) setThreshold(w http.ResponseWriter, r *http.Request) {
	whID, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemId"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("stock_item.id_invalid", "itemId is not a valid uuid"))
		return
	}
	var req setThresholdRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.SetThreshold(r.Context(), port.SetThresholdInput{
		WarehouseID:  whID,
		StockItemID:  itemID,
		MinThreshold: req.MinThreshold,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"warehouse_id":  whID.String(),
		"stock_item_id": itemID.String(),
		"min_threshold": req.MinThreshold,
	})
}

// =====================================================================
// Alerts — GET /alerts
// =====================================================================

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.AlertFilter{}
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("alerts.branch_invalid", "branch_id is not a valid uuid"))
			return
		}
		f.BranchID = &id
	}
	out, err := h.uc.ListStockAlerts(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]alertDTO, 0, len(out))
	for _, a := range out {
		items = append(items, toAlertDTO(a))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// =====================================================================
// Opname — start / count / commit / cancel / list / get
// =====================================================================

func (h *Handler) listOpnameSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var whID *uuid.UUID
	if v := q.Get("warehouse_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("opname.warehouse_invalid", "warehouse_id is not a valid uuid"))
			return
		}
		whID = &id
	}
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	out, total, err := h.uc.ListOpnameSessions(r.Context(), whID, q.Get("status"), limit, (page-1)*limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]opnameSessionDTO, 0, len(out))
	for _, v := range out {
		items = append(items, toOpnameDTO(v))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getOpnameSession(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname")
	if !ok {
		return
	}
	v, err := h.uc.GetOpname(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameDTO(*v))
}

func (h *Handler) startOpname(w http.ResponseWriter, r *http.Request) {
	var req startOpnameRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	whID, err := uuid.Parse(req.WarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("opname.warehouse_invalid", "warehouse_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	v, err := h.uc.StartOpname(r.Context(), port.StartOpnameInput{
		WarehouseID: whID,
		StartedBy:   c.UserID,
		Notes:       req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toOpnameDTO(*v))
}

func (h *Handler) upsertOpnameCount(w http.ResponseWriter, r *http.Request) {
	sessID, ok := httpserver.ParseUUIDParam(w, r, "id", "opname")
	if !ok {
		return
	}
	var req upsertCountRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemID, err := uuid.Parse(req.StockItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("opname.item_invalid", "stock_item_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	in := port.UpsertOpnameCountInput{
		SessionID:   sessID,
		StockItemID: itemID,
		CountedQty:  req.CountedQty,
		Notes:       req.Notes,
		CountedBy:   c.UserID,
	}
	if req.CableRemnantDecision != nil && *req.CableRemnantDecision != "" {
		d := domain.CableRemnantDecision(*req.CableRemnantDecision)
		if d != domain.CableRemnantKeep && d != domain.CableRemnantScrap {
			httpserver.WriteError(w, errors.Validation("opname.decision_invalid",
				"cable_remnant_decision must be keep_partial or scrap"))
			return
		}
		in.CableRemnantDecision = &d
	}
	out, err := h.uc.UpsertOpnameCount(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Return the single line; caller can re-fetch the session for the full view.
	resp := opnameCountDTO{
		ID:          out.ID.String(),
		StockItemID: out.StockItemID.String(),
		ExpectedQty: out.ExpectedQty,
		CountedQty:  out.CountedQty,
		Variance:    out.Variance,
		Notes:       out.Notes,
		CountedAt:   httpserver.FormatRFC3339(out.CountedAt),
	}
	if out.CableRemnantDecision != nil {
		x := string(*out.CableRemnantDecision)
		resp.CableRemnantDecision = &x
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) commitOpname(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname")
	if !ok {
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	v, err := h.uc.CommitOpname(r.Context(), id, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameDTO(*v))
}

func (h *Handler) cancelOpname(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname")
	if !ok {
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	v, err := h.uc.CancelOpname(r.Context(), id, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameDTO(*v))
}
