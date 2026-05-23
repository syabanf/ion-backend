// Wave 89 (Tier 3) — HTTP surface for product BOM templates.
//
// Routes (mounted in handler.go::Mount):
//
//	POST   /products/{id}/bom-templates              — create new template for product
//	GET    /products/{id}/bom-templates              — list templates (active or all)
//	GET    /products/{id}/bom-templates/active       — get the single active template
//	GET    /bom-templates/{id}                       — full detail (header + lines)
//	POST   /bom-templates/{id}/deactivate            — flip active → false
package http

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

type bomTemplateDTO struct {
	ID          string  `json:"id"`
	ProductID   string  `json:"product_id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Active      bool    `json:"active"`
	CreatedBy   *string `json:"created_by,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type bomItemDTO struct {
	ID              string  `json:"id"`
	StockItemID     string  `json:"stock_item_id"`
	DefaultQuantity float64 `json:"default_quantity"`
	Required        bool    `json:"required"`
	SortOrder       int     `json:"sort_order"`
	Notes           string  `json:"notes,omitempty"`
}

type bomTemplateDetailDTO struct {
	bomTemplateDTO
	Items []bomItemDTO `json:"items"`
}

func toBOMTemplateDTO(t domain.ProductBOMTemplate) bomTemplateDTO {
	out := bomTemplateDTO{
		ID:          t.ID.String(),
		ProductID:   t.ProductID.String(),
		Name:        t.Name,
		Description: t.Description,
		Active:      t.Active,
		CreatedAt:   httpserver.FormatRFC3339(t.CreatedAt),
		UpdatedAt:   httpserver.FormatRFC3339(t.UpdatedAt),
	}
	if t.CreatedBy != nil {
		s := t.CreatedBy.String()
		out.CreatedBy = &s
	}
	return out
}

func toBOMDetailDTO(d port.BOMTemplateDetail) bomTemplateDetailDTO {
	out := bomTemplateDetailDTO{bomTemplateDTO: toBOMTemplateDTO(d.Template)}
	for _, it := range d.Items {
		out.Items = append(out.Items, bomItemDTO{
			ID:              it.ID.String(),
			StockItemID:     it.StockItemID.String(),
			DefaultQuantity: it.DefaultQuantity,
			Required:        it.Required,
			SortOrder:       it.SortOrder,
			Notes:           it.Notes,
		})
	}
	return out
}

type createBOMRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Items       []struct {
		StockItemID     string  `json:"stock_item_id"`
		DefaultQuantity float64 `json:"default_quantity"`
		Required        bool    `json:"required"`
		SortOrder       int     `json:"sort_order,omitempty"`
		Notes           string  `json:"notes,omitempty"`
	} `json:"items"`
}

func (h *Handler) createBOMTemplate(w http.ResponseWriter, r *http.Request) {
	productID, ok := httpserver.ParseUUIDParam(w, r, "id", "product")
	if !ok {
		return
	}
	var req createBOMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]domain.ProductBOMTemplateItemInput, 0, len(req.Items))
	for _, it := range req.Items {
		sid, err := uuid.Parse(it.StockItemID)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		items = append(items, domain.ProductBOMTemplateItemInput{
			StockItemID:     sid,
			DefaultQuantity: it.DefaultQuantity,
			Required:        it.Required,
			SortOrder:       it.SortOrder,
			Notes:           it.Notes,
		})
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		by = &uid
	}
	detail, err := h.uc.CreateBOMTemplate(r.Context(), port.CreateBOMTemplateInput{
		ProductID:   productID,
		Name:        req.Name,
		Description: req.Description,
		Items:       items,
		CreatedBy:   by,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBOMDetailDTO(*detail))
}

func (h *Handler) getBOMTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "bom_template")
	if !ok {
		return
	}
	detail, err := h.uc.GetBOMTemplate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBOMDetailDTO(*detail))
}

func (h *Handler) getActiveBOMForProduct(w http.ResponseWriter, r *http.Request) {
	productID, ok := httpserver.ParseUUIDParam(w, r, "id", "product")
	if !ok {
		return
	}
	detail, err := h.uc.GetActiveBOMTemplateForProduct(r.Context(), productID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBOMDetailDTO(*detail))
}

func (h *Handler) listBOMTemplatesForProduct(w http.ResponseWriter, r *http.Request) {
	productID, ok := httpserver.ParseUUIDParam(w, r, "id", "product")
	if !ok {
		return
	}
	activeOnly := r.URL.Query().Get("active_only") == "true"
	items, err := h.uc.ListBOMTemplatesForProduct(r.Context(), productID, activeOnly)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]bomTemplateDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toBOMTemplateDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

func (h *Handler) deactivateBOMTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "bom_template")
	if !ok {
		return
	}
	if err := h.uc.DeactivateBOMTemplate(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
