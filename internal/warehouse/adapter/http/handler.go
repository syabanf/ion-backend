// Package http is the driving adapter for the warehouse bounded context.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	uc         port.UseCase
	woDispatch port.WODispatchUseCase
	verifier   *auth.Verifier
}

func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// Mount — route map:
//
//	Warehouses
//	  GET   /warehouses                         [warehouse.warehouse.read]
//	  GET   /warehouses/{id}                    [warehouse.warehouse.read]
//	  POST  /warehouses                         [warehouse.warehouse.manage]
//	  PATCH /warehouses/{id}                    [warehouse.warehouse.manage]
//
//	Catalog
//	  GET   /catalog/items                      [warehouse.catalog.read]
//	  GET   /catalog/items/{id}                 [warehouse.catalog.read]
//	  POST  /catalog/items                      [warehouse.catalog.manage]
//	  PATCH /catalog/items/{id}                 [warehouse.catalog.manage]
//
//	Inventory + intake
//	  GET   /warehouses/{id}/inventory          [warehouse.stock.read]
//	  GET   /warehouses/{id}/movements          [warehouse.stock.read]
//	  POST  /warehouses/{id}/intake             [warehouse.stock.intake]
//
//	Assets
//	  GET   /assets                             [warehouse.stock.read]
//	  GET   /assets/{id}                        [warehouse.stock.read]
//
//	Transfers
//	  GET   /transfers                          [warehouse.transfer.manage]
//	  GET   /transfers/{id}                     [warehouse.transfer.manage]
//	  POST  /transfers                          [warehouse.transfer.manage]
//	  POST  /transfers/{id}/dispatch            [warehouse.transfer.manage]
//	  POST  /transfers/{id}/receive             [warehouse.transfer.manage]
//	  POST  /transfers/{id}/cancel              [warehouse.transfer.manage]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Warehouses
		r.With(httpserver.RequirePermission("warehouse.warehouse.read")).
			Get("/warehouses", h.listWarehouses)
		r.With(httpserver.RequirePermission("warehouse.warehouse.read")).
			Get("/warehouses/{id}", h.getWarehouse)
		r.With(httpserver.RequirePermission("warehouse.warehouse.manage")).
			Post("/warehouses", h.createWarehouse)
		r.With(httpserver.RequirePermission("warehouse.warehouse.manage")).
			Patch("/warehouses/{id}", h.updateWarehouse)

		// Catalog
		r.With(httpserver.RequirePermission("warehouse.catalog.read")).
			Get("/catalog/items", h.listItems)
		r.With(httpserver.RequirePermission("warehouse.catalog.read")).
			Get("/catalog/items/{id}", h.getItem)
		r.With(httpserver.RequirePermission("warehouse.catalog.manage")).
			Post("/catalog/items", h.createItem)
		r.With(httpserver.RequirePermission("warehouse.catalog.manage")).
			Patch("/catalog/items/{id}", h.updateItem)

		// Inventory + intake + movements
		r.With(httpserver.RequirePermission("warehouse.stock.read")).
			Get("/warehouses/{id}/inventory", h.inventory)
		r.With(httpserver.RequirePermission("warehouse.stock.read")).
			Get("/warehouses/{id}/movements", h.movements)
		r.With(httpserver.RequirePermission("warehouse.stock.intake")).
			Post("/warehouses/{id}/intake", h.intake)

		// Assets
		r.With(httpserver.RequirePermission("warehouse.stock.read")).
			Get("/assets", h.listAssets)
		r.With(httpserver.RequirePermission("warehouse.stock.read")).
			Get("/assets/{id}", h.getAsset)

		// Transfers
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Get("/transfers", h.listTransfers)
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Get("/transfers/{id}", h.getTransfer)
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Post("/transfers", h.createTransfer)
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Post("/transfers/{id}/dispatch", h.dispatchTransfer)
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Post("/transfers/{id}/receive", h.receiveTransfer)
		r.With(httpserver.RequirePermission("warehouse.transfer.manage")).
			Post("/transfers/{id}/cancel", h.cancelTransfer)

		// Suppliers (master data — CRM-Sales-Enterprise PRD §5.1)
		r.With(httpserver.RequirePermission("warehouse.supplier.read")).
			Get("/suppliers", h.listSuppliers)
		r.With(httpserver.RequirePermission("warehouse.supplier.read")).
			Get("/suppliers/{id}", h.getSupplier)
		r.With(httpserver.RequirePermission("warehouse.supplier.manage")).
			Post("/suppliers", h.createSupplier)
		r.With(httpserver.RequirePermission("warehouse.supplier.manage")).
			Patch("/suppliers/{id}", h.updateSupplier)

		// --- M3 r2 — Thresholds (warehouse.threshold.manage)
		r.With(httpserver.RequirePermission("warehouse.threshold.manage")).
			Put("/warehouses/{id}/items/{itemId}/threshold", h.setThreshold)

		// --- M3 r2 — Alerts (warehouse.alerts.read)
		r.With(httpserver.RequirePermission("warehouse.alerts.read")).
			Get("/alerts", h.listAlerts)

		// --- M3 r2 — Opname (warehouse.opname.execute)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Get("/opname/sessions", h.listOpnameSessions)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Get("/opname/sessions/{id}", h.getOpnameSession)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Post("/opname/sessions", h.startOpname)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Put("/opname/sessions/{id}/counts", h.upsertOpnameCount)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Post("/opname/sessions/{id}/commit", h.commitOpname)
		r.With(httpserver.RequirePermission("warehouse.opname.execute")).
			Post("/opname/sessions/{id}/cancel", h.cancelOpname)
	})

	// WO dispatch — opt-in surface. Self-skips when WithWODispatch
	// hasn't been called, so test wirings that don't need it stay clean.
	h.MountWODispatch(r)
}

// DTOs (warehouseDTO, stockItemDTO, assetDTO, transferDTO, …) live in dto.go.

// =====================================================================
// Handlers — Warehouses
// =====================================================================

func (h *Handler) listWarehouses(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") == "true"
	out, err := h.uc.ListWarehouses(r.Context(), activeOnly)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]warehouseDTO, 0, len(out))
	for _, it := range out {
		items = append(items, toWarehouseDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getWarehouse(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	it, err := h.uc.GetWarehouse(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWarehouseDTO(*it))
}

func (h *Handler) createWarehouse(w http.ResponseWriter, r *http.Request) {
	var req createWarehouseRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateWarehouseInput{Name: req.Name, Code: req.Code, Address: req.Address, Notes: req.Notes}
	if req.BranchID != nil {
		id, err := uuid.Parse(*req.BranchID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("warehouse.branch_id_invalid", "branch_id is not a valid uuid"))
			return
		}
		in.BranchID = &id
	}
	out, err := h.uc.CreateWarehouse(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Re-fetch via list-shape so the response carries joined branch info,
	// keeping create + get identical (same approach as network.createNode).
	full, err := h.uc.GetWarehouse(r.Context(), out.ID)
	if err != nil {
		// Created OK but couldn't re-read — fall back to the bare projection
		// so the caller still gets the id.
		httpserver.WriteJSON(w, http.StatusCreated, warehouseDTO{
			ID: out.ID.String(), Name: out.Name, Code: out.Code,
			Address: out.Address, Notes: out.Notes,
			Active: out.Active, CreatedAt: httpserver.FormatRFC3339(out.CreatedAt),
		})
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toWarehouseDTO(*full))
}

func (h *Handler) updateWarehouse(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	var req updateWarehouseRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateWarehouseInput{
		ID: id, Name: req.Name, Address: req.Address, Notes: req.Notes, Active: req.Active,
		ClearBranch: req.ClearBranch,
	}
	if req.BranchID != nil {
		bid, err := uuid.Parse(*req.BranchID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("warehouse.branch_id_invalid", "branch_id is not a valid uuid"))
			return
		}
		in.BranchID = &bid
	}
	out, err := h.uc.UpdateWarehouse(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	full, err := h.uc.GetWarehouse(r.Context(), out.ID)
	if err != nil {
		httpserver.WriteJSON(w, http.StatusOK, warehouseDTO{
			ID: out.ID.String(), Name: out.Name, Code: out.Code,
			Address: out.Address, Notes: out.Notes,
			Active: out.Active, CreatedAt: httpserver.FormatRFC3339(out.CreatedAt),
		})
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWarehouseDTO(*full))
}

// =====================================================================
// Handlers — Catalog
// =====================================================================

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.StockItemListFilter{
		Search:   q.Get("q"),
		Category: q.Get("category"),
		Limit:    httpserver.ParseIntDefault(q.Get("page_size"), 50),
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	f.Offset = (page - 1) * f.Limit

	if v := q.Get("active"); v != "" {
		b := v == "true" || v == "1"
		f.Active = &b
	}

	items, total, err := h.uc.ListStockItems(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]stockItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toStockItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "page": page, "page_size": f.Limit,
	})
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "stock_item")
	if !ok {
		return
	}
	it, err := h.uc.GetStockItem(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toStockItemDTO(*it))
}

func (h *Handler) createItem(w http.ResponseWriter, r *http.Request) {
	var req createItemRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	it, err := h.uc.CreateStockItem(r.Context(), port.CreateStockItemInput{
		SKU: req.SKU, Name: req.Name, Category: domain.ItemCategory(req.Category),
		Brand: req.Brand, Model: req.Model, Spec: req.Spec,
		Unit:            domain.Unit(req.Unit),
		DefaultUnitCost: req.DefaultUnitCost,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toStockItemDTO(*it))
}

func (h *Handler) updateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "stock_item")
	if !ok {
		return
	}
	var req updateItemRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	it, err := h.uc.UpdateStockItem(r.Context(), port.UpdateStockItemInput{
		ID: id, Name: req.Name, Brand: req.Brand, Model: req.Model, Spec: req.Spec,
		DefaultUnitCost: req.DefaultUnitCost, Active: req.Active,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toStockItemDTO(*it))
}

// =====================================================================
// Handlers — Inventory + intake + movements
// =====================================================================

func (h *Handler) inventory(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	q := r.URL.Query()
	f := port.InventoryFilter{
		WarehouseID: id, Category: q.Get("category"), Search: q.Get("q"),
		BelowOnly: q.Get("below_only") == "true",
		OrderBy:   q.Get("order_by"),
	}
	rows, total, err := h.uc.Inventory(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]inventoryRowDTO, 0, len(rows))
	for _, x := range rows {
		out = append(out, toInventoryRowDTO(x))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

func (h *Handler) intake(w http.ResponseWriter, r *http.Request) {
	whID, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	var req intakeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemID, err := uuid.Parse(req.StockItemID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("intake.stock_item_invalid", "stock_item_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}

	in := port.IntakeInput{
		WarehouseID:      whID,
		StockItemID:      itemID,
		Quantity:         req.Quantity,
		UnitCost:         req.UnitCost,
		Distributor:      req.Distributor,
		PurchaseOrderRef: req.PurchaseOrderRef,
		Reason:           req.Reason,
	}
	for _, se := range req.Serials {
		in.Serials = append(in.Serials, port.SerialEntry{
			SerialNumber: se.SerialNumber,
			QRCode:       se.QRCode,
			MACAddress:   se.MACAddress,
			Condition:    domain.Condition(se.Condition),
			Ownership:    domain.Ownership(se.Ownership),
		})
	}
	if req.ReceivedAt != nil {
		if t, err := time.Parse(time.RFC3339, *req.ReceivedAt); err == nil {
			in.ReceivedAt = t
		}
	}
	if req.PurchaseDate != nil {
		if t, err := time.Parse("2006-01-02", *req.PurchaseDate); err == nil {
			in.PurchaseDate = &t
		}
	}
	if req.WarrantyExpiry != nil {
		if t, err := time.Parse("2006-01-02", *req.WarrantyExpiry); err == nil {
			in.WarrantyExpiry = &t
		}
	}

	out, err := h.uc.Intake(r.Context(), in, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	resp := map[string]any{
		"created_assets": []string{},
	}
	if out.StockLevel != nil {
		resp["stock_level"] = map[string]any{
			"warehouse_id":  out.StockLevel.WarehouseID.String(),
			"stock_item_id": out.StockLevel.StockItemID.String(),
			"quantity":      out.StockLevel.Quantity,
		}
	}
	ids := make([]string, 0, len(out.CreatedAssets))
	for _, id := range out.CreatedAssets {
		ids = append(ids, id.String())
	}
	resp["created_assets"] = ids
	httpserver.WriteJSON(w, http.StatusCreated, resp)
}

func (h *Handler) movements(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "warehouse")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	offset := (page - 1) * limit

	rows, total, err := h.uc.ListMovements(r.Context(), id, limit, offset)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]movementDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, toMovementDTO(m))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "page": page, "page_size": limit,
	})
}

// =====================================================================
// Handlers — Assets
// =====================================================================

func (h *Handler) listAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.AssetListFilter{
		Status:  q.Get("status"),
		Search:  q.Get("q"),
		OrderBy: q.Get("order_by"),
		Limit:   httpserver.ParseIntDefault(q.Get("page_size"), 50),
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	f.Offset = (page - 1) * f.Limit
	if v := q.Get("warehouse_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.WarehouseID = &id
		}
	}
	if v := q.Get("stock_item_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.StockItemID = &id
		}
	}
	out, total, err := h.uc.ListAssets(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]assetDTO, 0, len(out))
	for _, a := range out {
		items = append(items, toAssetDTO(a))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": f.Limit,
	})
}

func (h *Handler) getAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "asset")
	if !ok {
		return
	}
	a, err := h.uc.GetAsset(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAssetDTO(*a))
}

// =====================================================================
// Handlers — Transfers
// =====================================================================

func (h *Handler) listTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	out, total, err := h.uc.ListTransfers(r.Context(), q.Get("status"), limit, (page-1)*limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]transferDTO, 0, len(out))
	for _, t := range out {
		items = append(items, toTransferDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getTransfer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "transfer")
	if !ok {
		return
	}
	t, err := h.uc.GetTransfer(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTransferDTO(*t))
}

func (h *Handler) createTransfer(w http.ResponseWriter, r *http.Request) {
	var req createTransferRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	src, err := uuid.Parse(req.SourceWarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("transfer.src_invalid", "source_warehouse_id is not a valid uuid"))
		return
	}
	dst, err := uuid.Parse(req.DestinationWarehouseID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("transfer.dst_invalid", "destination_warehouse_id is not a valid uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}

	in := port.CreateTransferInput{
		SourceWarehouseID: src, DestinationWarehouseID: dst,
		Notes: req.Notes, CreatedBy: c.UserID,
	}
	for _, it := range req.Items {
		sid, err := uuid.Parse(it.StockItemID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("transfer.item_invalid", "stock_item_id is not a valid uuid"))
			return
		}
		ii := port.TransferItemInput{StockItemID: sid, Quantity: it.Quantity}
		if it.AssetID != nil {
			aid, err := uuid.Parse(*it.AssetID)
			if err != nil {
				httpserver.WriteError(w, errors.Validation("transfer.asset_invalid", "asset_id is not a valid uuid"))
				return
			}
			ii.AssetID = &aid
		}
		in.Items = append(in.Items, ii)
	}

	t, err := h.uc.CreateTransfer(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toTransferDTO(*t))
}

func (h *Handler) dispatchTransfer(w http.ResponseWriter, r *http.Request) {
	h.transferTransition(w, r, h.uc.DispatchTransfer)
}
func (h *Handler) receiveTransfer(w http.ResponseWriter, r *http.Request) {
	h.transferTransition(w, r, h.uc.ReceiveTransfer)
}
func (h *Handler) cancelTransfer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "transfer")
	if !ok {
		return
	}
	if err := h.uc.CancelTransfer(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) transferTransition(
	w http.ResponseWriter, r *http.Request,
	fn func(ctx context.Context, id, performedBy uuid.UUID) (*domain.Transfer, error),
) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "transfer")
	if !ok {
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	t, err := fn(r.Context(), id, c.UserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTransferDTO(*t))
}

// =====================================================================
// helpers
// =====================================================================
