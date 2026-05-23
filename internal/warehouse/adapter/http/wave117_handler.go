// Wave 117 — HTTP handlers for warehouse depth.
//
// Extends the existing Handler with a focused MountDepth() entry point.
// Each surface is opt-in: cmd/warehouse-svc/main.go calls With* on the
// usecase Service before mounting. Permission gates align with the new
// permissions seeded by migration 0080.
package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/internal/warehouse/usecase"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DepthService is the subset of *usecase.Service the Wave 117 handlers
// consume. Keeping it as a concrete pointer (not an interface) is
// deliberate: the surface is wide + churns. The handler is wired only
// when cmd/warehouse-svc has built the full Service.
type DepthService = *usecase.Service

// WithDepth attaches the Wave 117 service surface.
func (h *Handler) WithDepth(s DepthService) *Handler {
	h.depth = s
	return h
}

// MountDepth mounts the Wave 117 routes. Called from Handler.Mount when
// h.depth is non-nil.
func (h *Handler) MountDepth(r chi.Router) {
	if h.depth == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// ----- Item categories -----
		r.With(httpserver.RequirePermission("warehouse.item.category.read")).
			Get("/item-categories", h.listItemCategories)
		r.With(httpserver.RequirePermission("warehouse.item.category.read")).
			Get("/item-categories/{id}", h.getItemCategory)
		r.With(httpserver.RequirePermission("warehouse.item.category.write")).
			Post("/item-categories", h.createItemCategory)
		r.With(httpserver.RequirePermission("warehouse.item.category.write")).
			Patch("/item-categories/{id}", h.updateItemCategory)

		// ----- Cable lots -----
		r.With(httpserver.RequirePermission("warehouse.cable.track")).
			Get("/cable-lots", h.listCableLots)
		r.With(httpserver.RequirePermission("warehouse.cable.track")).
			Get("/cable-lots/{id}", h.getCableLot)
		r.With(httpserver.RequirePermission("warehouse.cable.track")).
			Post("/cable-lots", h.receiveCableLot)
		r.With(httpserver.RequirePermission("warehouse.cable.cut")).
			Post("/cable-lots/{id}/cut", h.cutCable)
		r.With(httpserver.RequirePermission("warehouse.cable.track")).
			Get("/cable-lots/{id}/cuts", h.listCableCuts)
		r.With(httpserver.RequirePermission("warehouse.cable.track")).
			Get("/cable-lots/low-remaining", h.listLowRemainingCableLots)
		r.With(httpserver.RequirePermission("warehouse.cable.cut")).
			Post("/cable-lots/{id}/dispose", h.disposeCableLot)

		// ----- Consumable batches -----
		r.With(httpserver.RequirePermission("warehouse.consumable.track")).
			Get("/consumable-batches", h.listConsumableBatches)
		r.With(httpserver.RequirePermission("warehouse.consumable.track")).
			Get("/consumable-batches/{id}", h.getConsumableBatch)
		r.With(httpserver.RequirePermission("warehouse.consumable.track")).
			Post("/consumable-batches", h.receiveConsumableBatch)
		r.With(httpserver.RequirePermission("warehouse.consumable.consume")).
			Post("/consumable-batches/{id}/consume", h.consumeBatch)
		r.With(httpserver.RequirePermission("warehouse.consumable.consume")).
			Post("/consumables/{item_id}/consume-fifo", h.consumeFIFO)
		r.With(httpserver.RequirePermission("warehouse.consumable.track")).
			Get("/consumable-batches/{id}/log", h.listConsumptionForBatch)
		r.With(httpserver.RequirePermission("warehouse.consumable.track")).
			Get("/consumable-batches/expiring", h.listExpiringSoon)

		// ----- Sub-warehouses -----
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.read")).
			Get("/sub-warehouses", h.listSubWarehouses)
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.read")).
			Get("/sub-warehouses/{id}", h.getSubWarehouse)
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.manage")).
			Post("/sub-warehouses", h.createSubWarehouse)
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.read")).
			Get("/sub-warehouses/my", h.mySubWarehouses)
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.manage")).
			Post("/sub-warehouses/{id}/transfer-in", h.transferIntoSub)
		r.With(httpserver.RequirePermission("warehouse.sub_warehouse.manage")).
			Post("/sub-warehouses/{id}/receive-from", h.receiveFromSub)

		// ----- Asset location -----
		r.With(httpserver.RequirePermission("warehouse.asset.location.read")).
			Get("/assets/{id}/location-history", h.assetLocationHistory)
		r.With(httpserver.RequirePermission("warehouse.asset.location.read")).
			Get("/assets/{id}/current-location", h.assetCurrentLocation)
		r.With(httpserver.RequirePermission("warehouse.asset.location.read")).
			Get("/assets/in-transit-anomalies", h.listInTransitAnomalies)

		// ----- QR codes -----
		r.With(httpserver.RequirePermission("warehouse.qr.generate")).
			Get("/items/{id}/qr", h.getItemQR)
		r.With(httpserver.RequirePermission("warehouse.qr.generate")).
			Post("/items/{id}/regenerate-qr", h.regenerateItemQR)
		r.With(httpserver.RequirePermission("warehouse.qr.scan")).
			Post("/qr/scan", h.scanQR)

		// ----- Opname tablet -----
		r.With(httpserver.RequirePermission("warehouse.opname.tablet.sync")).
			Post("/opname-tablet/sessions", h.startOpnameTabletSession)
		r.With(httpserver.RequirePermission("warehouse.opname.tablet.sync")).
			Post("/opname-tablet/sessions/{id}/sync", h.syncOpnameTabletPayload)
		r.With(httpserver.RequirePermission("warehouse.opname.tablet.sync")).
			Post("/opname-tablet/sessions/{id}/reconcile", h.reconcileOpnameTablet)
		r.With(httpserver.RequirePermission("warehouse.opname.tablet.sync")).
			Get("/opname-tablet/sessions/{id}", h.getOpnameTabletSession)

		// ----- Typed dispatch (Wave 117 router) -----
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Post("/typed-dispatch", h.dispatchTyped)
	})
}

// =====================================================================
// Item category handlers
// =====================================================================

type itemCategoryDTO struct {
	ID                         string  `json:"id"`
	Code                       string  `json:"code"`
	Name                       string  `json:"name"`
	ParentID                   *string `json:"parent_id,omitempty"`
	TypeCode                   string  `json:"type_code"`
	Description                string  `json:"description"`
	DefaultUnit                string  `json:"default_unit"`
	SubWarehouseAllowedDefault bool    `json:"sub_warehouse_allowed_default"`
	RequiresSerialAtIntake     bool    `json:"requires_serial_at_intake"`
	Active                     bool    `json:"active"`
	CreatedAt                  string  `json:"created_at"`
	UpdatedAt                  string  `json:"updated_at"`
}

func toItemCategoryDTO(c domain.ItemCategoryDef) itemCategoryDTO {
	var parentID *string
	if c.ParentID != nil {
		s := c.ParentID.String()
		parentID = &s
	}
	return itemCategoryDTO{
		ID:                         c.ID.String(),
		Code:                       c.Code,
		Name:                       c.Name,
		ParentID:                   parentID,
		TypeCode:                   string(c.TypeCode),
		Description:                c.Description,
		DefaultUnit:                c.DefaultUnit,
		SubWarehouseAllowedDefault: c.SubWarehouseAllowedDefault,
		RequiresSerialAtIntake:     c.RequiresSerialAtIntake,
		Active:                     c.Active,
		CreatedAt:                  httpserver.FormatRFC3339(c.CreatedAt),
		UpdatedAt:                  httpserver.FormatRFC3339(c.UpdatedAt),
	}
}

type createItemCategoryRequest struct {
	Code                       string  `json:"code"`
	Name                       string  `json:"name"`
	TypeCode                   string  `json:"type_code"`
	ParentID                   *string `json:"parent_id,omitempty"`
	Description                string  `json:"description"`
	DefaultUnit                string  `json:"default_unit"`
	SubWarehouseAllowedDefault *bool   `json:"sub_warehouse_allowed_default,omitempty"`
	RequiresSerialAtIntake     *bool   `json:"requires_serial_at_intake,omitempty"`
}

func (h *Handler) createItemCategory(w http.ResponseWriter, r *http.Request) {
	var req createItemCategoryRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateItemCategoryInput{
		Code:                       req.Code,
		Name:                       req.Name,
		TypeCode:                   domain.ItemType(req.TypeCode),
		Description:                req.Description,
		DefaultUnit:                req.DefaultUnit,
		SubWarehouseAllowedDefault: req.SubWarehouseAllowedDefault,
		RequiresSerialAtIntake:     req.RequiresSerialAtIntake,
	}
	if req.ParentID != nil && *req.ParentID != "" {
		pid, err := uuid.Parse(*req.ParentID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("item_category.parent_id_invalid", "parent_id is not a valid uuid"))
			return
		}
		in.ParentID = &pid
	}
	out, err := h.depth.CreateItemCategory(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toItemCategoryDTO(*out))
}

func (h *Handler) getItemCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "item_category")
	if !ok {
		return
	}
	out, err := h.depth.GetItemCategory(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toItemCategoryDTO(*out))
}

func (h *Handler) listItemCategories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ItemCategoryListFilter{
		TypeCode:   q.Get("type_code"),
		ActiveOnly: q.Get("active_only") == "true",
	}
	if pid := q.Get("parent_id"); pid != "" {
		id, err := uuid.Parse(pid)
		if err == nil {
			f.ParentID = &id
		}
	}
	out, err := h.depth.ListItemCategories(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]itemCategoryDTO, 0, len(out))
	for _, c := range out {
		items = append(items, toItemCategoryDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type updateItemCategoryRequest struct {
	Name                       *string `json:"name,omitempty"`
	Description                *string `json:"description,omitempty"`
	ParentID                   *string `json:"parent_id,omitempty"`
	ClearParent                bool    `json:"clear_parent,omitempty"`
	DefaultUnit                *string `json:"default_unit,omitempty"`
	SubWarehouseAllowedDefault *bool   `json:"sub_warehouse_allowed_default,omitempty"`
	RequiresSerialAtIntake     *bool   `json:"requires_serial_at_intake,omitempty"`
	Active                     *bool   `json:"active,omitempty"`
}

func (h *Handler) updateItemCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "item_category")
	if !ok {
		return
	}
	var req updateItemCategoryRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateItemCategoryInput{
		ID:                         id,
		Name:                       req.Name,
		Description:                req.Description,
		DefaultUnit:                req.DefaultUnit,
		SubWarehouseAllowedDefault: req.SubWarehouseAllowedDefault,
		RequiresSerialAtIntake:     req.RequiresSerialAtIntake,
		Active:                     req.Active,
		ClearParent:                req.ClearParent,
	}
	if req.ParentID != nil && *req.ParentID != "" {
		pid, err := uuid.Parse(*req.ParentID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("item_category.parent_id_invalid", "parent_id is not a valid uuid"))
			return
		}
		in.ParentID = &pid
	}
	out, err := h.depth.UpdateItemCategory(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toItemCategoryDTO(*out))
}

// =====================================================================
// Cable lot handlers
// =====================================================================

type cableLotDTO struct {
	ID                    string   `json:"id"`
	ItemID                string   `json:"item_id"`
	LotNumber             string   `json:"lot_number"`
	TotalLengthMeters     float64  `json:"total_length_meters"`
	RemainingLengthMeters float64  `json:"remaining_length_meters"`
	DrumSerial            string   `json:"drum_serial"`
	Status                string   `json:"status"`
	CurrentWarehouseID    *string  `json:"current_warehouse_id,omitempty"`
	UnitCostPerMeter      *float64 `json:"unit_cost_per_meter,omitempty"`
	ReceivedAt            string   `json:"received_at"`
}

func toCableLotDTO(l domain.CableLot) cableLotDTO {
	var wh *string
	if l.CurrentWarehouseID != nil {
		s := l.CurrentWarehouseID.String()
		wh = &s
	}
	return cableLotDTO{
		ID:                    l.ID.String(),
		ItemID:                l.ItemID.String(),
		LotNumber:             l.LotNumber,
		TotalLengthMeters:     l.TotalLengthMeters,
		RemainingLengthMeters: l.RemainingLengthMeters,
		DrumSerial:            l.DrumSerial,
		Status:                string(l.Status),
		CurrentWarehouseID:    wh,
		UnitCostPerMeter:      l.UnitCostPerMeter,
		ReceivedAt:            httpserver.FormatRFC3339(l.ReceivedAt),
	}
}

type receiveCableLotRequest struct {
	ItemID            string   `json:"item_id"`
	LotNumber         string   `json:"lot_number"`
	TotalLengthMeters float64  `json:"total_length_meters"`
	DrumSerial        string   `json:"drum_serial"`
	SupplierID        *string  `json:"supplier_id,omitempty"`
	WarehouseID       string   `json:"warehouse_id"`
	UnitCostPerMeter  *float64 `json:"unit_cost_per_meter,omitempty"`
	Notes             string   `json:"notes"`
}

func (h *Handler) receiveCableLot(w http.ResponseWriter, r *http.Request) {
	var req receiveCableLotRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemID, err := uuid.Parse(req.ItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("cable_lot.item_id_invalid", "item_id is not a valid uuid"))
		return
	}
	whID, err := uuid.Parse(req.WarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("cable_lot.warehouse_id_invalid", "warehouse_id is not a valid uuid"))
		return
	}
	in := port.ReceiveCableLotInput{
		ItemID:            itemID,
		LotNumber:         req.LotNumber,
		TotalLengthMeters: req.TotalLengthMeters,
		DrumSerial:        req.DrumSerial,
		WarehouseID:       whID,
		UnitCostPerMeter:  req.UnitCostPerMeter,
		Notes:             req.Notes,
	}
	if req.SupplierID != nil && *req.SupplierID != "" {
		sid, err := uuid.Parse(*req.SupplierID)
		if err == nil {
			in.SupplierID = &sid
		}
	}
	out, err := h.depth.ReceiveCableLot(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCableLotDTO(*out))
}

func (h *Handler) listCableLots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.CableLotListFilter{
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset: httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	if v := q.Get("item_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.ItemID = &id
		}
	}
	if v := q.Get("warehouse_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.WarehouseID = &id
		}
	}
	lots, total, err := h.depth.ListCableLots(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]cableLotDTO, 0, len(lots))
	for _, l := range lots {
		items = append(items, toCableLotDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) getCableLot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "cable_lot")
	if !ok {
		return
	}
	out, err := h.depth.GetCableLot(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCableLotDTO(*out))
}

type cutCableRequest struct {
	LengthMeters float64 `json:"length_meters"`
	WOID         *string `json:"wo_id,omitempty"`
	Notes        string  `json:"notes"`
}

func (h *Handler) cutCable(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "cable_lot")
	if !ok {
		return
	}
	var req cutCableRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	var woID *uuid.UUID
	if req.WOID != nil && *req.WOID != "" {
		wid, err := uuid.Parse(*req.WOID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("cable_lot.wo_id_invalid", "wo_id is not a valid uuid"))
			return
		}
		woID = &wid
	}
	cut, err := h.depth.CutSegment(r.Context(), id, req.LengthMeters, woID, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":               cut.ID.String(),
		"cable_lot_id":     cut.CableLotID.String(),
		"cut_length_meters": cut.CutLengthMeters,
		"cut_at":           httpserver.FormatRFC3339(cut.CutAt),
	})
}

func (h *Handler) listCableCuts(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "cable_lot")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("limit"), 50)
	offset := httpserver.ParseIntDefault(q.Get("offset"), 0)
	cuts, total, err := h.depth.ListCableCuts(r.Context(), id, limit, offset)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(cuts))
	for _, c := range cuts {
		items = append(items, map[string]any{
			"id":                c.ID.String(),
			"cable_lot_id":      c.CableLotID.String(),
			"cut_length_meters": c.CutLengthMeters,
			"used_for_wo_id":    nilOrStr(c.UsedForWOID),
			"cut_at":            httpserver.FormatRFC3339(c.CutAt),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) listLowRemainingCableLots(w http.ResponseWriter, r *http.Request) {
	threshold, err := strconv.ParseFloat(r.URL.Query().Get("threshold_meters"), 64)
	if err != nil || threshold <= 0 {
		threshold = 50 // sensible default for the dashboard widget
	}
	lots, err := h.depth.ListLowRemainingCableLots(r.Context(), threshold)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]cableLotDTO, 0, len(lots))
	for _, l := range lots {
		items = append(items, toCableLotDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "threshold_meters": threshold})
}

type disposeCableLotRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) disposeCableLot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "cable_lot")
	if !ok {
		return
	}
	var req disposeCableLotRequest
	_ = httpserver.DecodeJSON(r, &req)
	out, err := h.depth.DisposeCableLot(r.Context(), id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCableLotDTO(*out))
}

// =====================================================================
// Consumable batch handlers
// =====================================================================

type consumableBatchDTO struct {
	ID                 string   `json:"id"`
	ItemID             string   `json:"item_id"`
	BatchNo            string   `json:"batch_no"`
	TotalQty           int      `json:"total_qty"`
	RemainingQty       int      `json:"remaining_qty"`
	ExpiryDate         *string  `json:"expiry_date,omitempty"`
	ReceivedAt         string   `json:"received_at"`
	Status             string   `json:"status"`
	CurrentWarehouseID *string  `json:"current_warehouse_id,omitempty"`
	UnitCost           *float64 `json:"unit_cost,omitempty"`
}

func toConsumableBatchDTO(b domain.ConsumableBatch) consumableBatchDTO {
	var exp *string
	if b.ExpiryDate != nil {
		s := b.ExpiryDate.Format("2006-01-02")
		exp = &s
	}
	var wh *string
	if b.CurrentWarehouseID != nil {
		s := b.CurrentWarehouseID.String()
		wh = &s
	}
	return consumableBatchDTO{
		ID:                 b.ID.String(),
		ItemID:             b.ItemID.String(),
		BatchNo:            b.BatchNo,
		TotalQty:           b.TotalQty,
		RemainingQty:       b.RemainingQty,
		ExpiryDate:         exp,
		ReceivedAt:         httpserver.FormatRFC3339(b.ReceivedAt),
		Status:             string(b.Status),
		CurrentWarehouseID: wh,
		UnitCost:           b.UnitCost,
	}
}

type receiveConsumableBatchRequest struct {
	ItemID      string   `json:"item_id"`
	BatchNo     string   `json:"batch_no"`
	TotalQty    int      `json:"total_qty"`
	ExpiryDate  *string  `json:"expiry_date,omitempty"`
	WarehouseID string   `json:"warehouse_id"`
	UnitCost    *float64 `json:"unit_cost,omitempty"`
	Notes       string   `json:"notes"`
}

func (h *Handler) receiveConsumableBatch(w http.ResponseWriter, r *http.Request) {
	var req receiveConsumableBatchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemID, err := uuid.Parse(req.ItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("consumable.item_id_invalid", "item_id is not a valid uuid"))
		return
	}
	whID, err := uuid.Parse(req.WarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("consumable.warehouse_id_invalid", "warehouse_id is not a valid uuid"))
		return
	}
	in := port.ReceiveConsumableBatchInput{
		ItemID:      itemID,
		BatchNo:     req.BatchNo,
		TotalQty:    req.TotalQty,
		WarehouseID: whID,
		UnitCost:    req.UnitCost,
		Notes:       req.Notes,
	}
	if req.ExpiryDate != nil && *req.ExpiryDate != "" {
		t, err := timeParseDate(*req.ExpiryDate)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("consumable.expiry_invalid", "expiry_date must be YYYY-MM-DD"))
			return
		}
		in.ExpiryDate = &t
	}
	out, err := h.depth.ReceiveConsumableBatch(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toConsumableBatchDTO(*out))
}

func (h *Handler) listConsumableBatches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ConsumableBatchListFilter{
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset: httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	if v := q.Get("item_id"); v != "" {
		id, _ := uuid.Parse(v)
		if id != uuid.Nil {
			f.ItemID = &id
		}
	}
	if v := q.Get("warehouse_id"); v != "" {
		id, _ := uuid.Parse(v)
		if id != uuid.Nil {
			f.WarehouseID = &id
		}
	}
	batches, total, err := h.depth.ListConsumableBatches(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]consumableBatchDTO, 0, len(batches))
	for _, b := range batches {
		items = append(items, toConsumableBatchDTO(b))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) getConsumableBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "consumable_batch")
	if !ok {
		return
	}
	out, err := h.depth.GetConsumableBatch(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toConsumableBatchDTO(*out))
}

type consumeBatchRequest struct {
	Qty   int     `json:"qty"`
	WOID  *string `json:"wo_id,omitempty"`
	Notes string  `json:"notes"`
}

func (h *Handler) consumeBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "consumable_batch")
	if !ok {
		return
	}
	var req consumeBatchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	var woID *uuid.UUID
	if req.WOID != nil && *req.WOID != "" {
		wid, err := uuid.Parse(*req.WOID)
		if err == nil {
			woID = &wid
		}
	}
	log, err := h.depth.ConsumeFromBatch(r.Context(), &id, nil, req.Qty, woID, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":                  log.ID.String(),
		"consumable_batch_id": log.ConsumableBatchID.String(),
		"qty_consumed":        log.QtyConsumed,
		"consumed_at":         httpserver.FormatRFC3339(log.ConsumedAt),
	})
}

func (h *Handler) consumeFIFO(w http.ResponseWriter, r *http.Request) {
	itemID, ok := httpserver.ParseUUIDParam(w, r, "item_id", "consumable")
	if !ok {
		return
	}
	var req consumeBatchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	var woID *uuid.UUID
	if req.WOID != nil && *req.WOID != "" {
		wid, err := uuid.Parse(*req.WOID)
		if err == nil {
			woID = &wid
		}
	}
	log, err := h.depth.ConsumeFromBatch(r.Context(), nil, &itemID, req.Qty, woID, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":                  log.ID.String(),
		"consumable_batch_id": log.ConsumableBatchID.String(),
		"qty_consumed":        log.QtyConsumed,
		"consumed_at":         httpserver.FormatRFC3339(log.ConsumedAt),
	})
}

func (h *Handler) listConsumptionForBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "consumable_batch")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("limit"), 50)
	offset := httpserver.ParseIntDefault(q.Get("offset"), 0)
	logs, total, err := h.depth.ListConsumptionForBatch(r.Context(), id, limit, offset)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(logs))
	for _, l := range logs {
		items = append(items, map[string]any{
			"id":                  l.ID.String(),
			"consumable_batch_id": l.ConsumableBatchID.String(),
			"qty_consumed":        l.QtyConsumed,
			"wo_id":               nilOrStr(l.WOID),
			"consumed_at":         httpserver.FormatRFC3339(l.ConsumedAt),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) listExpiringSoon(w http.ResponseWriter, r *http.Request) {
	days := httpserver.ParseIntDefault(r.URL.Query().Get("days"), 30)
	batches, _, err := h.depth.ListExpiringSoon(r.Context(), days)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]consumableBatchDTO, 0, len(batches))
	for _, b := range batches {
		items = append(items, toConsumableBatchDTO(b))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "days_ahead": days})
}

// =====================================================================
// Sub-warehouse handlers
// =====================================================================

type subWarehouseDTO struct {
	ID                string `json:"id"`
	ParentWarehouseID string `json:"parent_warehouse_id"`
	Name              string `json:"name"`
	Code              string `json:"code"`
	OwnerUserID       string `json:"owner_user_id"`
	OwnerRole         string `json:"owner_role"`
	IsMobile          bool   `json:"is_mobile"`
	VehicleID         string `json:"vehicle_id"`
	CanPurchase       bool   `json:"can_purchase"`
	Active            bool   `json:"active"`
}

func toSubWarehouseDTO(s domain.SubWarehouse) subWarehouseDTO {
	return subWarehouseDTO{
		ID:                s.ID.String(),
		ParentWarehouseID: s.ParentWarehouseID.String(),
		Name:              s.Name,
		Code:              s.Code,
		OwnerUserID:       s.OwnerUserID.String(),
		OwnerRole:         string(s.OwnerRole),
		IsMobile:          s.IsMobile,
		VehicleID:         s.VehicleID,
		CanPurchase:       s.CanPurchase,
		Active:            s.Active,
	}
}

type createSubWarehouseRequest struct {
	ParentWarehouseID string `json:"parent_warehouse_id"`
	Name              string `json:"name"`
	Code              string `json:"code"`
	OwnerUserID       string `json:"owner_user_id"`
	OwnerRole         string `json:"owner_role"`
	IsMobile          bool   `json:"is_mobile"`
	VehicleID         string `json:"vehicle_id"`
}

func (h *Handler) createSubWarehouse(w http.ResponseWriter, r *http.Request) {
	var req createSubWarehouseRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	parentID, err := uuid.Parse(req.ParentWarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("sub_warehouse.parent_invalid", "parent_warehouse_id is not a valid uuid"))
		return
	}
	ownerID, err := uuid.Parse(req.OwnerUserID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("sub_warehouse.owner_invalid", "owner_user_id is not a valid uuid"))
		return
	}
	out, err := h.depth.CreateSubWarehouse(r.Context(), port.CreateSubWarehouseInput{
		ParentWarehouseID: parentID,
		Name:              req.Name,
		Code:              req.Code,
		OwnerUserID:       ownerID,
		OwnerRole:         domain.SubWarehouseRole(req.OwnerRole),
		IsMobile:          req.IsMobile,
		VehicleID:         req.VehicleID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSubWarehouseDTO(*out))
}

func (h *Handler) getSubWarehouse(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "sub_warehouse")
	if !ok {
		return
	}
	out, err := h.depth.GetSubWarehouse(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubWarehouseDTO(*out))
}

func (h *Handler) listSubWarehouses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.SubWarehouseListFilter{ActiveOnly: q.Get("active_only") == "true"}
	if v := q.Get("parent_warehouse_id"); v != "" {
		id, _ := uuid.Parse(v)
		if id != uuid.Nil {
			f.ParentWarehouseID = &id
		}
	}
	if v := q.Get("owner_user_id"); v != "" {
		id, _ := uuid.Parse(v)
		if id != uuid.Nil {
			f.OwnerUserID = &id
		}
	}
	subs, err := h.depth.ListSubWarehouses(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]subWarehouseDTO, 0, len(subs))
	for _, s := range subs {
		items = append(items, toSubWarehouseDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) mySubWarehouses(w http.ResponseWriter, r *http.Request) {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	subs, err := h.depth.MySubWarehouses(r.Context(), c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]subWarehouseDTO, 0, len(subs))
	for _, s := range subs {
		items = append(items, toSubWarehouseDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type transferIntoSubRequest struct {
	AssetID string `json:"asset_id"`
	Reason  string `json:"reason"`
}

func (h *Handler) transferIntoSub(w http.ResponseWriter, r *http.Request) {
	subID, ok := httpserver.ParseUUIDParam(w, r, "id", "sub_warehouse")
	if !ok {
		return
	}
	var req transferIntoSubRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	assetID, err := uuid.Parse(req.AssetID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("sub_warehouse.asset_id_invalid", "asset_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.depth.TransferToSub(r.Context(), assetID, subID, c.UserID, req.Reason); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "transferred"})
}

type receiveFromSubRequest struct {
	AssetID           string `json:"asset_id"`
	ParentWarehouseID string `json:"parent_warehouse_id"`
	Reason            string `json:"reason"`
}

func (h *Handler) receiveFromSub(w http.ResponseWriter, r *http.Request) {
	subID, ok := httpserver.ParseUUIDParam(w, r, "id", "sub_warehouse")
	if !ok {
		return
	}
	var req receiveFromSubRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	assetID, err := uuid.Parse(req.AssetID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("sub_warehouse.asset_id_invalid", "asset_id is not a valid uuid"))
		return
	}
	parentID, err := uuid.Parse(req.ParentWarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("sub_warehouse.parent_invalid", "parent_warehouse_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.depth.ReceiveFromSub(r.Context(), assetID, subID, parentID, c.UserID, req.Reason); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "received"})
}

// =====================================================================
// Asset location handlers
// =====================================================================

func (h *Handler) assetLocationHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "asset")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("limit"), 50)
	offset := httpserver.ParseIntDefault(q.Get("offset"), 0)
	moves, total, err := h.depth.LocationHistory(r.Context(), id, limit, offset)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(moves))
	for _, m := range moves {
		items = append(items, map[string]any{
			"id":                    m.ID.String(),
			"asset_id":              m.AssetID.String(),
			"movement_kind":         string(m.MovementKind),
			"from_warehouse_id":     nilOrStr(m.FromWarehouseID),
			"to_warehouse_id":       nilOrStr(m.ToWarehouseID),
			"from_sub_warehouse_id": nilOrStr(m.FromSubWarehouseID),
			"to_sub_warehouse_id":   nilOrStr(m.ToSubWarehouseID),
			"wo_id":                 nilOrStr(m.WOID),
			"customer_id":           nilOrStr(m.CustomerID),
			"reason":                m.Reason,
			"location_label":        m.LocationLabel,
			"moved_at":              httpserver.FormatRFC3339(m.MovedAt),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) assetCurrentLocation(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "asset")
	if !ok {
		return
	}
	loc, ts, err := h.depth.CurrentLocation(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := map[string]any{}
	if loc != nil {
		resp["current_location_id"] = loc.String()
	}
	if ts != nil {
		resp["last_movement_at"] = httpserver.FormatRFC3339(*ts)
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) listInTransitAnomalies(w http.ResponseWriter, r *http.Request) {
	hours := httpserver.ParseIntDefault(r.URL.Query().Get("hours"), 24)
	moves, err := h.depth.ListInTransitAnomalies(r.Context(), timeHours(hours))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(moves))
	for _, m := range moves {
		items = append(items, map[string]any{
			"asset_id":      m.AssetID.String(),
			"movement_kind": string(m.MovementKind),
			"moved_at":      httpserver.FormatRFC3339(m.MovedAt),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "threshold_hours": hours,
	})
}

// =====================================================================
// QR handlers
// =====================================================================

func (h *Handler) getItemQR(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "item")
	if !ok {
		return
	}
	qr, err := h.depth.GenerateQRForItem(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"item_id": id.String(), "qr": qr})
}

func (h *Handler) regenerateItemQR(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "item")
	if !ok {
		return
	}
	qr, err := h.depth.GenerateQRForItem(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"item_id": id.String(), "qr": qr, "regenerated": true})
}

type scanQRRequest struct {
	QR string `json:"qr"`
}

func (h *Handler) scanQR(w http.ResponseWriter, r *http.Request) {
	var req scanQRRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	res, err := h.depth.ScanQR(r.Context(), req.QR)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := map[string]any{
		"item_type": string(res.ItemType),
		"raw":       res.Raw,
	}
	if res.Asset != nil {
		resp["asset_id"] = res.Asset.ID.String()
		resp["serial_number"] = res.Asset.SerialNumber
		resp["status"] = string(res.Asset.Status)
	}
	if res.Item != nil {
		resp["item_id"] = res.Item.ID.String()
		resp["sku"] = res.Item.SKU
		resp["name"] = res.Item.Name
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

// =====================================================================
// Opname tablet handlers
// =====================================================================

type startOpnameTabletRequest struct {
	OpnameSessionID string `json:"opname_session_id"`
	DeviceID        string `json:"device_id"`
}

func (h *Handler) startOpnameTabletSession(w http.ResponseWriter, r *http.Request) {
	var req startOpnameTabletRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	opnameID, err := uuid.Parse(req.OpnameSessionID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("opname_tablet.opname_session_invalid", "opname_session_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	out, err := h.depth.StartOpnameTabletSession(r.Context(), port.CreateOpnameTabletSessionInput{
		OpnameSessionID:  opnameID,
		DeviceID:         req.DeviceID,
		TechnicianUserID: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toOpnameTabletDTO(*out))
}

func (h *Handler) syncOpnameTabletPayload(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname_tablet_session")
	if !ok {
		return
	}
	// Accept either JSON {payload_b64, total_scans} or raw bytes.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("opname_tablet.body_invalid", "could not read body"))
		return
	}
	var totalScans int
	var payload []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			TotalScans int    `json:"total_scans"`
			Payload    string `json:"payload"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			httpserver.WriteError(w, errors.Validation("opname_tablet.json_invalid", "invalid json body"))
			return
		}
		totalScans = req.TotalScans
		payload = []byte(req.Payload)
	} else {
		payload = body
		if v := r.Header.Get("X-Total-Scans"); v != "" {
			totalScans, _ = strconv.Atoi(v)
		}
	}
	out, err := h.depth.SubmitOfflinePayload(r.Context(), id, payload, totalScans)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameTabletDTO(*out))
}

func (h *Handler) reconcileOpnameTablet(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname_tablet_session")
	if !ok {
		return
	}
	out, err := h.depth.ReconcileSession(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameTabletDTO(*out))
}

func (h *Handler) getOpnameTabletSession(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "opname_tablet_session")
	if !ok {
		return
	}
	out, err := h.depth.GetOpnameTabletSession(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOpnameTabletDTO(*out))
}

func toOpnameTabletDTO(s domain.OpnameTabletSession) map[string]any {
	out := map[string]any{
		"id":                 s.ID.String(),
		"opname_session_id":  s.OpnameSessionID.String(),
		"device_id":          s.DeviceID,
		"technician_user_id": s.TechnicianUserID.String(),
		"started_at":         httpserver.FormatRFC3339(s.StartedAt),
		"sync_status":        string(s.SyncStatus),
		"total_scans":        s.TotalScans,
	}
	if s.CompletedAt != nil {
		out["completed_at"] = httpserver.FormatRFC3339(*s.CompletedAt)
	}
	if s.LastSyncedAt != nil {
		out["last_synced_at"] = httpserver.FormatRFC3339(*s.LastSyncedAt)
	}
	if s.OfflinePayloadHash != "" {
		out["offline_payload_hash"] = s.OfflinePayloadHash
	}
	return out
}

// =====================================================================
// Typed dispatch handler
// =====================================================================

type typedDispatchRequest struct {
	WOID        string  `json:"wo_id"`
	WarehouseID string  `json:"warehouse_id"`
	ItemID      string  `json:"item_id"`
	Qty         float64 `json:"qty"`
	AssetID     *string `json:"asset_id,omitempty"`
	CustomerID  *string `json:"customer_id,omitempty"`
}

func (h *Handler) dispatchTyped(w http.ResponseWriter, r *http.Request) {
	var req typedDispatchRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	woID, err := uuid.Parse(req.WOID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("typed_dispatch.wo_id_invalid", "wo_id is not a valid uuid"))
		return
	}
	whID, err := uuid.Parse(req.WarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("typed_dispatch.warehouse_id_invalid", "warehouse_id is not a valid uuid"))
		return
	}
	itemID, err := uuid.Parse(req.ItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("typed_dispatch.item_id_invalid", "item_id is not a valid uuid"))
		return
	}
	in := usecase.TypedDispatchInput{
		WOID:         woID,
		WarehouseID:  whID,
		DispatchedBy: c.UserID,
		ItemID:       itemID,
		Qty:          req.Qty,
	}
	if req.AssetID != nil && *req.AssetID != "" {
		aid, err := uuid.Parse(*req.AssetID)
		if err == nil {
			in.AssetID = &aid
		}
	}
	if req.CustomerID != nil && *req.CustomerID != "" {
		cid, err := uuid.Parse(*req.CustomerID)
		if err == nil {
			in.CustomerID = &cid
		}
	}
	res, err := h.depth.DispatchTyped(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resp := map[string]any{"item_type": string(res.ItemType)}
	if res.CableCutID != nil {
		resp["cable_cut_id"] = res.CableCutID.String()
	}
	if res.BatchConsumption != nil {
		resp["batch_consumption_id"] = res.BatchConsumption.String()
	}
	if res.LocationMovement != nil {
		resp["location_movement_id"] = res.LocationMovement.String()
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

// =====================================================================
// Helpers
// =====================================================================

func nilOrStr(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return u.String()
}
