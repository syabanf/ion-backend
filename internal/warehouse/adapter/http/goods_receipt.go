// Wave 86 (Tier 3) — HTTP surface for goods receipts.
//
// Routes (mounted in handler.go::Mount):
//
//	GET    /purchase-orders/{id}/receipts  — list receipts for a PO
//	POST   /purchase-orders/{id}/receipts  — create a receipt (atomic)
//	GET    /goods-receipts/{id}            — receipt detail (header + lines)
//
// The receipt-by-id GET sits on /goods-receipts/{id} rather than
// nesting under the PO because the dashboard often deep-links a
// receipt without context.
package http

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

type grDTO struct {
	ID              string  `json:"id"`
	ReceiptNumber   string  `json:"receipt_number"`
	PurchaseOrderID string  `json:"purchase_order_id"`
	WarehouseID     string  `json:"warehouse_id"`
	ReceivedAt      string  `json:"received_at"`
	ReceivedBy      *string `json:"received_by,omitempty"`
	CarrierRef      string  `json:"carrier_ref,omitempty"`
	Notes           string  `json:"notes,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

type grLineDTO struct {
	ID                  string  `json:"id"`
	PurchaseOrderLineID string  `json:"purchase_order_line_id"`
	QuantityReceived    float64 `json:"quantity_received"`
	UnitCost            float64 `json:"unit_cost"`
	AssetID             *string `json:"asset_id,omitempty"`
	Notes               string  `json:"notes,omitempty"`
}

type grDetailDTO struct {
	grDTO
	Lines []grLineDTO `json:"lines"`
}

func toGRDTO(g domain.GoodsReceipt) grDTO {
	out := grDTO{
		ID:              g.ID.String(),
		ReceiptNumber:   g.ReceiptNumber,
		PurchaseOrderID: g.PurchaseOrderID.String(),
		WarehouseID:     g.WarehouseID.String(),
		ReceivedAt:      httpserver.FormatRFC3339(g.ReceivedAt),
		CarrierRef:      g.CarrierRef,
		Notes:           g.Notes,
		CreatedAt:       httpserver.FormatRFC3339(g.CreatedAt),
	}
	if g.ReceivedBy != nil {
		s := g.ReceivedBy.String()
		out.ReceivedBy = &s
	}
	return out
}

func toGRDetailDTO(d port.GoodsReceiptDetail) grDetailDTO {
	out := grDetailDTO{grDTO: toGRDTO(d.Receipt)}
	for _, l := range d.Lines {
		dto := grLineDTO{
			ID:                  l.ID.String(),
			PurchaseOrderLineID: l.PurchaseOrderLineID.String(),
			QuantityReceived:    l.QuantityReceived,
			UnitCost:            l.UnitCost,
			Notes:               l.Notes,
		}
		if l.AssetID != nil {
			s := l.AssetID.String()
			dto.AssetID = &s
		}
		out.Lines = append(out.Lines, dto)
	}
	return out
}

// =====================================================================
// Approve PO (Wave 86 added; complements Submit + Cancel)
// =====================================================================

func (h *Handler) approvePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by uuid.UUID
	if claims != nil {
		by = claims.UserID
	}
	detail, err := h.uc.ApprovePurchaseOrder(r.Context(), id, by)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPODetailDTO(*detail))
}

// =====================================================================
// Receipts
// =====================================================================

type createGRRequest struct {
	WarehouseID string `json:"warehouse_id,omitempty"` // optional; defaults to PO's
	CarrierRef  string `json:"carrier_ref,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Lines       []struct {
		PurchaseOrderLineID string  `json:"purchase_order_line_id"`
		QuantityReceived    float64 `json:"quantity_received"`
		UnitCost            float64 `json:"unit_cost,omitempty"`
		Notes               string  `json:"notes,omitempty"`
		Serials             []struct {
			SerialNumber string `json:"serial_number"`
			QRCode       string `json:"qr_code,omitempty"`
			MACAddress   string `json:"mac_address,omitempty"`
			Condition    string `json:"condition,omitempty"`
			Ownership    string `json:"ownership,omitempty"`
		} `json:"serials,omitempty"`
	} `json:"lines"`
}

func (h *Handler) createGoodsReceipt(w http.ResponseWriter, r *http.Request) {
	poID, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	var req createGRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var warehouseID uuid.UUID
	if s := req.WarehouseID; s != "" {
		wh, err := uuid.Parse(s)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		warehouseID = wh
	}
	lines := make([]port.ReceiptLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		poLineID, err := uuid.Parse(l.PurchaseOrderLineID)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		serials := make([]port.ReceiptSerialEntry, 0, len(l.Serials))
		for _, sn := range l.Serials {
			serials = append(serials, port.ReceiptSerialEntry{
				SerialNumber: sn.SerialNumber,
				QRCode:       sn.QRCode,
				MACAddress:   sn.MACAddress,
				Condition:    sn.Condition,
				Ownership:    sn.Ownership,
			})
		}
		lines = append(lines, port.ReceiptLineInput{
			PurchaseOrderLineID: poLineID,
			QuantityReceived:    l.QuantityReceived,
			UnitCost:            l.UnitCost,
			Serials:             serials,
			Notes:               l.Notes,
		})
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		by = &uid
	}
	detail, err := h.uc.CreateGoodsReceipt(r.Context(), port.CreateGoodsReceiptInput{
		PurchaseOrderID: poID,
		WarehouseID:     warehouseID,
		ReceivedBy:      by,
		CarrierRef:      req.CarrierRef,
		Notes:           req.Notes,
		Lines:           lines,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toGRDetailDTO(*detail))
}

func (h *Handler) getGoodsReceipt(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "goods_receipt")
	if !ok {
		return
	}
	detail, err := h.uc.GetGoodsReceipt(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toGRDetailDTO(*detail))
}

func (h *Handler) listReceiptsForPO(w http.ResponseWriter, r *http.Request) {
	poID, ok := httpserver.ParseUUIDParam(w, r, "id", "purchase_order")
	if !ok {
		return
	}
	items, err := h.uc.ListGoodsReceiptsForPO(r.Context(), poID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]grDetailDTO, 0, len(items))
	for _, d := range items {
		out = append(out, toGRDetailDTO(d))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}
