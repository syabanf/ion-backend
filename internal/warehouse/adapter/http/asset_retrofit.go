// Wave 87 (Tier 3) — HTTP surface for asset retrofit.
//
// Routes (mounted in handler.go::Mount):
//
//	POST /assets/{id}/retrofit       — perform retrofit on a source asset
//	GET  /assets/{id}/retrofits      — list retrofits where this asset was the source
//
// The POST is gated under warehouse.asset.manage (a new permission;
// dashboard hides the button when the caller doesn't have it).
package http

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

type retrofitRequest struct {
	NewSerialNumber string `json:"new_serial_number,omitempty"`
	NewQRCode       string `json:"new_qr_code,omitempty"`
	NewWarehouseID  string `json:"new_warehouse_id,omitempty"`
	Reason          string `json:"reason"`
}

type retrofitDTO struct {
	ID                string  `json:"id"`
	SourceAssetID     string  `json:"source_asset_id"`
	ProducedAssetID   string  `json:"produced_asset_id"`
	Reason            string  `json:"reason"`
	PerformedBy       *string `json:"performed_by,omitempty"`
	PerformedAt       string  `json:"performed_at"`
	ConsumeMovementID *string `json:"consume_movement_id,omitempty"`
	ProduceMovementID *string `json:"produce_movement_id,omitempty"`
}

type retrofitResultDTO struct {
	Retrofit      retrofitDTO `json:"retrofit"`
	SourceAsset   assetDTO    `json:"source_asset"`
	ProducedAsset assetDTO    `json:"produced_asset"`
}

func toRetrofitDTO(r domain.AssetRetrofit) retrofitDTO {
	out := retrofitDTO{
		ID:              r.ID.String(),
		SourceAssetID:   r.SourceAssetID.String(),
		ProducedAssetID: r.ProducedAssetID.String(),
		Reason:          r.Reason,
		PerformedAt:     httpserver.FormatRFC3339(r.PerformedAt),
	}
	if r.PerformedBy != nil {
		s := r.PerformedBy.String()
		out.PerformedBy = &s
	}
	if r.ConsumeMovementID != nil {
		s := r.ConsumeMovementID.String()
		out.ConsumeMovementID = &s
	}
	if r.ProduceMovementID != nil {
		s := r.ProduceMovementID.String()
		out.ProduceMovementID = &s
	}
	return out
}

func (h *Handler) retrofitAsset(w http.ResponseWriter, r *http.Request) {
	srcID, ok := httpserver.ParseUUIDParam(w, r, "id", "asset")
	if !ok {
		return
	}
	var req retrofitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var newWh uuid.UUID
	if s := req.NewWarehouseID; s != "" {
		v, err := uuid.Parse(s)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		newWh = v
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var by *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		by = &uid
	}
	res, err := h.uc.RetrofitAsset(r.Context(), port.RetrofitInput{
		SourceAssetID:   srcID,
		NewSerialNumber: req.NewSerialNumber,
		NewQRCode:       req.NewQRCode,
		NewWarehouseID:  newWh,
		Reason:          req.Reason,
		PerformedBy:     by,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, retrofitResultDTO{
		Retrofit:      toRetrofitDTO(res.Retrofit),
		SourceAsset:   toAssetDTO(res.SourceAsset),
		ProducedAsset: toAssetDTO(res.ProducedAsset),
	})
}

func (h *Handler) listRetrofitsForAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "asset")
	if !ok {
		return
	}
	items, err := h.uc.ListRetrofitsForAsset(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]retrofitDTO, 0, len(items))
	for _, x := range items {
		out = append(out, toRetrofitDTO(x))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}
