// WO dispatch HTTP surface.
//
// Wired by Handler.MountWODispatch (called from cmd/warehouse-svc/main.go).
// Lives in a side handler file so the existing Mount stays focused on
// the round-1/r2 routes — same shape as supplier_handler.go.
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

// WithWODispatch attaches the dispatch usecase and registers the
// dispatch routes when Mount is called. Keep this strictly additive —
// callers that don't want the dispatch surface just don't call it.
func (h *Handler) WithWODispatch(uc port.WODispatchUseCase) *Handler {
	h.woDispatch = uc
	return h
}

// MountWODispatch — route map:
//
//	GET  /dispatch?wo_id=&warehouse_id=&status=    [warehouse.dispatch.read]
//	GET  /dispatch/{id}                            [warehouse.dispatch.read]
//	POST /dispatch                                 [warehouse.dispatch.manage]
//	POST /dispatch/{id}/stage                      [warehouse.dispatch.manage]
//	POST /dispatch/{id}/cancel                     [warehouse.dispatch.manage]
//	POST /dispatch/{id}/mark-picked-up             [warehouse.dispatch.manage]
//	POST /dispatch-items/{id}/scan                 [warehouse.dispatch.scan]
//	POST /dispatch-items/{id}/return               [warehouse.dispatch.manage]
func (h *Handler) MountWODispatch(r chi.Router) {
	if h.woDispatch == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("warehouse.dispatch.read")).
			Get("/dispatch", h.listDispatches)
		r.With(httpserver.RequirePermission("warehouse.dispatch.read")).
			Get("/dispatch/{id}", h.getDispatch)
		r.With(httpserver.RequirePermission("warehouse.dispatch.manage")).
			Post("/dispatch", h.createDispatch)
		r.With(httpserver.RequirePermission("warehouse.dispatch.manage")).
			Post("/dispatch/{id}/stage", h.stageDispatch)
		r.With(httpserver.RequirePermission("warehouse.dispatch.manage")).
			Post("/dispatch/{id}/cancel", h.cancelDispatch)
		r.With(httpserver.RequirePermission("warehouse.dispatch.manage")).
			Post("/dispatch/{id}/mark-picked-up", h.markDispatchPickedUp)
		r.With(httpserver.RequirePermission("warehouse.dispatch.scan")).
			Post("/dispatch-items/{id}/scan", h.scanDispatchItem)
		r.With(httpserver.RequirePermission("warehouse.dispatch.manage")).
			Post("/dispatch-items/{id}/return", h.returnDispatchItem)
	})
}

// =====================================================================
// DTOs
// =====================================================================

type woDispatchItemDTO struct {
	ID          string  `json:"id"`
	DispatchID  string  `json:"dispatch_id"`
	ItemID      string  `json:"item_id"`
	Qty         float64 `json:"qty"`
	ReturnedQty float64 `json:"returned_qty"`
	SerialOrQR  *string `json:"serial_or_qr,omitempty"`
	Status      string  `json:"status"`
	PickedAt    *string `json:"picked_at,omitempty"`
	PickedBy    *string `json:"picked_by,omitempty"`
	Notes       string  `json:"notes"`
}

type woDispatchDTO struct {
	ID           string              `json:"id"`
	WOID         string              `json:"wo_id"`
	WarehouseID  string              `json:"warehouse_id"`
	DispatchedBy *string             `json:"dispatched_by,omitempty"`
	Status       string              `json:"status"`
	PlannedAt    string              `json:"planned_at"`
	StagedAt     *string             `json:"staged_at,omitempty"`
	PickedUpAt   *string             `json:"picked_up_at,omitempty"`
	ReturnedAt   *string             `json:"returned_at,omitempty"`
	CancelledAt  *string             `json:"cancelled_at,omitempty"`
	CancelReason string              `json:"cancel_reason,omitempty"`
	Notes        string              `json:"notes,omitempty"`
	Revision     int                 `json:"revision"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
	Items        []woDispatchItemDTO `json:"items"`
	// Wave 89b — when set, identifies the BOM template that seeded
	// this dispatch (even if the operator hand-edited lines).
	SourceBOMTemplateID *string `json:"source_bom_template_id,omitempty"`
}

func toWODispatchItemDTO(it domain.WODispatchItem) woDispatchItemDTO {
	d := woDispatchItemDTO{
		ID:          it.ID.String(),
		DispatchID:  it.DispatchID.String(),
		ItemID:      it.ItemID.String(),
		Qty:         it.Qty,
		ReturnedQty: it.ReturnedQty,
		SerialOrQR:  it.SerialOrQR,
		Status:      string(it.Status),
		Notes:       it.Notes,
	}
	if it.PickedAt != nil {
		s := httpserver.FormatRFC3339(*it.PickedAt)
		d.PickedAt = &s
	}
	if it.PickedBy != nil {
		s := it.PickedBy.String()
		d.PickedBy = &s
	}
	return d
}

func toWODispatchDTO(d domain.WODispatch) woDispatchDTO {
	out := woDispatchDTO{
		ID:           d.ID.String(),
		WOID:         d.WOID.String(),
		WarehouseID:  d.WarehouseID.String(),
		Status:       string(d.Status),
		PlannedAt:    httpserver.FormatRFC3339(d.PlannedAt),
		CancelReason: d.CancelReason,
		Notes:        d.Notes,
		Revision:     d.Revision,
		CreatedAt:    httpserver.FormatRFC3339(d.CreatedAt),
		UpdatedAt:    httpserver.FormatRFC3339(d.UpdatedAt),
		Items:        []woDispatchItemDTO{},
	}
	if d.DispatchedBy != nil {
		s := d.DispatchedBy.String()
		out.DispatchedBy = &s
	}
	if d.StagedAt != nil {
		s := httpserver.FormatRFC3339(*d.StagedAt)
		out.StagedAt = &s
	}
	if d.PickedUpAt != nil {
		s := httpserver.FormatRFC3339(*d.PickedUpAt)
		out.PickedUpAt = &s
	}
	if d.ReturnedAt != nil {
		s := httpserver.FormatRFC3339(*d.ReturnedAt)
		out.ReturnedAt = &s
	}
	if d.CancelledAt != nil {
		s := httpserver.FormatRFC3339(*d.CancelledAt)
		out.CancelledAt = &s
	}
	if d.SourceBOMTemplateID != nil {
		s := d.SourceBOMTemplateID.String()
		out.SourceBOMTemplateID = &s
	}
	for _, it := range d.Items {
		out.Items = append(out.Items, toWODispatchItemDTO(it))
	}
	return out
}

type createWODispatchRequest struct {
	WOID        string `json:"wo_id"`
	WarehouseID string `json:"warehouse_id"`
	Notes       string `json:"notes,omitempty"`
	// Wave 89b — optional product_id; when set with empty items the
	// service materializes the BOM from the product's active template.
	ProductID string `json:"product_id,omitempty"`
	Items     []struct {
		ItemID string  `json:"item_id"`
		Qty    float64 `json:"qty"`
		Notes  string  `json:"notes,omitempty"`
	} `json:"items"`
}

type cancelDispatchRequest struct {
	Reason string `json:"reason"`
}

type scanItemRequest struct {
	SerialOrQR string `json:"serial_or_qr"`
}

type returnItemRequest struct {
	Qty   float64 `json:"qty"`
	Notes string  `json:"notes,omitempty"`
}

// =====================================================================
// Handlers
// =====================================================================

func (h *Handler) listDispatches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	f := port.WODispatchListFilter{
		Status: q.Get("status"),
		Limit:  limit,
		Offset: (page - 1) * limit,
	}
	if v := q.Get("wo_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.WOID = &id
		}
	}
	if v := q.Get("warehouse_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.WarehouseID = &id
		}
	}
	out, total, err := h.woDispatch.ListDispatches(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]woDispatchDTO, 0, len(out))
	for _, d := range out {
		items = append(items, toWODispatchDTO(d))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getDispatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "wo_dispatch")
	if !ok {
		return
	}
	d, err := h.woDispatch.GetDispatch(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODispatchDTO(*d))
}

func (h *Handler) createDispatch(w http.ResponseWriter, r *http.Request) {
	var req createWODispatchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	woID, err := uuid.Parse(req.WOID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo_dispatch.wo_invalid", "wo_id is not a valid uuid"))
		return
	}
	whID, err := uuid.Parse(req.WarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo_dispatch.warehouse_invalid", "warehouse_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	in := port.CreateWODispatchInput{
		WOID:         woID,
		WarehouseID:  whID,
		DispatchedBy: c.UserID,
		Notes:        req.Notes,
	}
	// Wave 89b — optional product_id triggers BOM pre-fill when items
	// is empty. Hand-edited items override the template; the template
	// id is still stamped on the dispatch in either case.
	if s := req.ProductID; s != "" {
		pid, err := uuid.Parse(s)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("wo_dispatch.product_invalid",
				"product_id is not a valid uuid"))
			return
		}
		in.ProductID = &pid
	}
	for _, it := range req.Items {
		iid, err := uuid.Parse(it.ItemID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("wo_dispatch.item_invalid", "item_id is not a valid uuid"))
			return
		}
		in.Items = append(in.Items, port.CreateWODispatchItemInput{
			ItemID: iid, Qty: it.Qty, Notes: it.Notes,
		})
	}
	d, err := h.woDispatch.CreateDispatch(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toWODispatchDTO(*d))
}

func (h *Handler) stageDispatch(w http.ResponseWriter, r *http.Request) {
	h.dispatchSimpleTransition(w, r, func(ctx, id, by uuid.UUID) (*domain.WODispatch, error) {
		return h.woDispatch.StageDispatch(r.Context(), id, by)
	})
}

func (h *Handler) markDispatchPickedUp(w http.ResponseWriter, r *http.Request) {
	h.dispatchSimpleTransition(w, r, func(ctx, id, by uuid.UUID) (*domain.WODispatch, error) {
		return h.woDispatch.MarkPickedUp(r.Context(), id, by)
	})
}

func (h *Handler) cancelDispatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "wo_dispatch")
	if !ok {
		return
	}
	var req cancelDispatchRequest
	// Body is optional — a cancel without a reason is permitted.
	_ = httpserver.DecodeJSON(r, &req)
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	d, err := h.woDispatch.CancelDispatch(r.Context(), id, c.UserID, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODispatchDTO(*d))
}

func (h *Handler) scanDispatchItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := httpserver.ParseUUIDParam(w, r, "id", "wo_dispatch_item")
	if !ok {
		return
	}
	var req scanItemRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	it, err := h.woDispatch.PickUpItemByScan(r.Context(), itemID, req.SerialOrQR, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODispatchItemDTO(*it))
}

func (h *Handler) returnDispatchItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := httpserver.ParseUUIDParam(w, r, "id", "wo_dispatch_item")
	if !ok {
		return
	}
	var req returnItemRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	it, err := h.woDispatch.ReturnItem(r.Context(), itemID, req.Qty, req.Notes, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODispatchItemDTO(*it))
}

// dispatchSimpleTransition — shared shape for {stage, mark-picked-up}.
func (h *Handler) dispatchSimpleTransition(
	w http.ResponseWriter, r *http.Request,
	fn func(ctx, id, by uuid.UUID) (*domain.WODispatch, error),
) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "wo_dispatch")
	if !ok {
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	d, err := fn(uuid.Nil, id, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODispatchDTO(*d))
}
