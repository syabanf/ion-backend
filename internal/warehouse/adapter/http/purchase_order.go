// Wave 85 (Tier 3 starter) — HTTP surface for warehouse purchase orders.
//
// Routes (mounted in handler.go::Mount):
//
//	GET    /purchase-orders                — list (filter + paginate)
//	GET    /purchase-orders/{id}           — full detail (header + lines)
//	POST   /purchase-orders                — create draft (body: po + lines)
//	POST   /purchase-orders/{id}/submit    — draft → submitted
//	POST   /purchase-orders/{id}/cancel    — non-terminal → cancelled
//
// The dashboard exercises this surface; goods-receipt (Wave 86) will
// extend it with `/purchase-orders/{id}/receipts` POST + GET.
package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

// poDTO is the header projection. Lines come back separately via the
// detail endpoint to keep the list response small.
type poDTO struct {
	ID                   string  `json:"id"`
	PONumber             string  `json:"po_number"`
	SupplierID           string  `json:"supplier_id"`
	BranchID             string  `json:"branch_id"`
	ReceivingWarehouseID string  `json:"receiving_warehouse_id"`
	Status               string  `json:"status"`
	Subtotal             float64 `json:"subtotal"`
	PPNRate              float64 `json:"ppn_rate"`
	Total                float64 `json:"total"`
	ExpectedAt           *string `json:"expected_at,omitempty"`
	Notes                string  `json:"notes,omitempty"`
	CreatedBy            *string `json:"created_by,omitempty"`
	SubmittedAt          *string `json:"submitted_at,omitempty"`
	ApprovedAt           *string `json:"approved_at,omitempty"`
	ClosedAt             *string `json:"closed_at,omitempty"`
	CancelledAt          *string `json:"cancelled_at,omitempty"`
	CancelledReason      string  `json:"cancelled_reason,omitempty"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

type poLineDTO struct {
	ID               string  `json:"id"`
	LineNo           int     `json:"line_no"`
	StockItemID      string  `json:"stock_item_id"`
	QuantityOrdered  float64 `json:"quantity_ordered"`
	QuantityReceived float64 `json:"quantity_received"`
	UnitCost         float64 `json:"unit_cost"`
	LineSubtotal     float64 `json:"line_subtotal"`
	Notes            string  `json:"notes,omitempty"`
}

type poDetailDTO struct {
	poDTO
	Lines []poLineDTO `json:"lines"`
}

func toPODTO(p domain.PurchaseOrder) poDTO {
	out := poDTO{
		ID:                   p.ID.String(),
		PONumber:             p.PONumber,
		SupplierID:           p.SupplierID.String(),
		BranchID:             p.BranchID.String(),
		ReceivingWarehouseID: p.ReceivingWarehouseID.String(),
		Status:               string(p.Status),
		Subtotal:             p.Subtotal,
		PPNRate:              p.PPNRate,
		Total:                p.Total,
		Notes:                p.Notes,
		CancelledReason:      p.CancelledReason,
		CreatedAt:            httpserver.FormatRFC3339(p.CreatedAt),
		UpdatedAt:            httpserver.FormatRFC3339(p.UpdatedAt),
	}
	if p.ExpectedAt != nil {
		s := p.ExpectedAt.Format("2006-01-02")
		out.ExpectedAt = &s
	}
	if p.CreatedBy != nil {
		s := p.CreatedBy.String()
		out.CreatedBy = &s
	}
	if p.SubmittedAt != nil {
		s := httpserver.FormatRFC3339(*p.SubmittedAt)
		out.SubmittedAt = &s
	}
	if p.ApprovedAt != nil {
		s := httpserver.FormatRFC3339(*p.ApprovedAt)
		out.ApprovedAt = &s
	}
	if p.ClosedAt != nil {
		s := httpserver.FormatRFC3339(*p.ClosedAt)
		out.ClosedAt = &s
	}
	if p.CancelledAt != nil {
		s := httpserver.FormatRFC3339(*p.CancelledAt)
		out.CancelledAt = &s
	}
	return out
}

func toPODetailDTO(d port.PurchaseOrderDetail) poDetailDTO {
	out := poDetailDTO{poDTO: toPODTO(d.PO)}
	for _, l := range d.Lines {
		out.Lines = append(out.Lines, poLineDTO{
			ID:               l.ID.String(),
			LineNo:           l.LineNo,
			StockItemID:      l.StockItemID.String(),
			QuantityOrdered:  l.QuantityOrdered,
			QuantityReceived: l.QuantityReceived,
			UnitCost:         l.UnitCost,
			LineSubtotal:     l.LineSubtotal,
			Notes:            l.Notes,
		})
	}
	return out
}

// =====================================================================
// Handlers
// =====================================================================

type createPORequest struct {
	SupplierID           string  `json:"supplier_id"`
	BranchID             string  `json:"branch_id"`
	ReceivingWarehouseID string  `json:"receiving_warehouse_id"`
	PPNRate              float64 `json:"ppn_rate"`
	ExpectedAt           string  `json:"expected_at,omitempty"` // YYYY-MM-DD
	Notes                string  `json:"notes,omitempty"`
	Lines                []struct {
		StockItemID     string  `json:"stock_item_id"`
		QuantityOrdered float64 `json:"quantity_ordered"`
		UnitCost        float64 `json:"unit_cost"`
		Notes           string  `json:"notes,omitempty"`
	} `json:"lines"`
}

func (h *Handler) createPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	var req createPORequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	supplierID, err := uuid.Parse(req.SupplierID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	branchID, err := uuid.Parse(req.BranchID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	warehouseID, err := uuid.Parse(req.ReceivingWarehouseID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	lines := make([]domain.PurchaseOrderLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		sid, perr := uuid.Parse(l.StockItemID)
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		lines = append(lines, domain.PurchaseOrderLineInput{
			StockItemID:     sid,
			QuantityOrdered: l.QuantityOrdered,
			UnitCost:        l.UnitCost,
			Notes:           l.Notes,
		})
	}
	var expected *time.Time
	if s := req.ExpectedAt; s != "" {
		t, perr := time.Parse("2006-01-02", s)
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		expected = &t
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		by = &uid
	}
	detail, err := h.uc.CreatePurchaseOrder(r.Context(), port.CreatePurchaseOrderInput{
		SupplierID:           supplierID,
		BranchID:             branchID,
		ReceivingWarehouseID: warehouseID,
		Lines:                lines,
		PPNRate:              req.PPNRate,
		ExpectedAt:           expected,
		Notes:                req.Notes,
		CreatedBy:            by,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toPODetailDTO(*detail))
}

func (h *Handler) getPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	detail, err := h.uc.GetPurchaseOrder(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPODetailDTO(*detail))
}

func (h *Handler) listPurchaseOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.PurchaseOrderListFilter{
		Status: q.Get("status"),
		Limit:  parsePositiveInt(q.Get("limit"), 50),
		Offset: parsePositiveInt(q.Get("offset"), 0),
	}
	if s := q.Get("branch_id"); s != "" {
		bid, err := uuid.Parse(s)
		if err == nil {
			f.BranchID = &bid
		}
	}
	if s := q.Get("supplier_id"); s != "" {
		sid, err := uuid.Parse(s)
		if err == nil {
			f.SupplierID = &sid
		}
	}
	items, total, err := h.uc.ListPurchaseOrders(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]poDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toPODTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *Handler) submitPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by uuid.UUID
	if claims != nil {
		by = claims.UserID
	}
	detail, err := h.uc.SubmitPurchaseOrder(r.Context(), id, by)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPODetailDTO(*detail))
}

type cancelPORequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) cancelPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	var req cancelPORequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by uuid.UUID
	if claims != nil {
		by = claims.UserID
	}
	detail, err := h.uc.CancelPurchaseOrder(r.Context(), id, by, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPODetailDTO(*detail))
}

// parsePositiveInt parses a query string integer with a default,
// clamping negatives to the default so callers can't pass -1 to
// disable pagination.
func parsePositiveInt(s string, dflt int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return dflt
	}
	return n
}
