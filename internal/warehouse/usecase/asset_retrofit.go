// Wave 87 (Tier 3) — Asset retrofit usecase.
//
// PRD §8A: an end-of-life serialized asset gets cannibalized for
// salvageable parts; those parts become a new asset with
// is_retrofit=true. The workflow is one atomic tx in the repo —
// this layer composes the payload after validating preconditions.
package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithAssetRetrofits wires the retrofit repo. Optional; the surface
// returns errRetrofitNotConfigured cleanly when nil.
func (s *Service) WithAssetRetrofits(r port.AssetRetrofitRepository) *Service {
	s.assetRetrofits = r
	return s
}

func errRetrofitNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "retrofit.not_configured",
		"asset retrofit is not configured for this service", nil)
}

// RetrofitAsset cannibalizes the source asset, mints a produced
// asset, and records the audit pair. The repo runs everything in
// one tx; we just shape the payload here.
func (s *Service) RetrofitAsset(
	ctx context.Context, in port.RetrofitInput,
) (*port.RetrofitResult, error) {
	if s.assetRetrofits == nil {
		return nil, errRetrofitNotConfigured()
	}
	if in.Reason = strings.TrimSpace(in.Reason); in.Reason == "" {
		return nil, derrors.Validation("retrofit.reason_required",
			"reason is required when retrofitting an asset")
	}
	// Load + validate source asset.
	src, err := s.assets.FindByID(ctx, in.SourceAssetID)
	if err != nil {
		return nil, err
	}
	if src.Status == domain.AssetStatusCannibalized {
		return nil, derrors.Conflict("retrofit.already_cannibalized",
			"source asset is already cannibalized")
	}
	if src.Status == domain.AssetStatusDecommissioned {
		return nil, derrors.Conflict("retrofit.source_decommissioned",
			"decommissioned assets can't be retrofitted")
	}
	// Default the produced asset's warehouse to the source's when
	// the caller didn't specify (typical: retrofit happens in-place).
	whID := in.NewWarehouseID
	if whID == uuid.Nil {
		if src.WarehouseID == nil {
			return nil, derrors.Validation("retrofit.warehouse_required",
				"source asset has no warehouse; specify new_warehouse_id")
		}
		whID = *src.WarehouseID
	}

	now := time.Now().UTC()
	// Build produced asset row. Inherits stock_item from source so the
	// retrofit stays within the same catalog category. Serial / QR
	// optional — PRD §8A allows un-labeled retrofits.
	produced, err := domain.NewAsset(src.StockItemID, whID,
		strings.TrimSpace(in.NewSerialNumber), now)
	if err != nil {
		return nil, err
	}
	produced.QRCode = strings.TrimSpace(in.NewQRCode)
	produced.IsRetrofit = true
	produced.Condition = domain.Condition("refurbished")
	// Track cost inheritance: the retrofit doesn't change the on-the-
	// books cost. Carry the source's purchase_cost forward.
	if src.PurchaseCost != nil {
		c := *src.PurchaseCost
		produced.PurchaseCost = &c
	}

	rec := domain.AssetRetrofit{
		ID:              uuid.New(),
		SourceAssetID:   in.SourceAssetID,
		ProducedAssetID: produced.ID,
		Reason:          in.Reason,
		PerformedBy:     in.PerformedBy,
		PerformedAt:     now,
	}

	consumeMov := domain.StockMovement{
		WarehouseID:   whID,
		StockItemID:   src.StockItemID,
		AssetID:       &src.ID,
		MovementType:  domain.MovementRetrofitConsume,
		Quantity:      -1,
		Reason:        in.Reason,
		ReferenceType: "asset_retrofit",
		ReferenceID:   &rec.ID,
		PerformedBy:   in.PerformedBy,
		PerformedAt:   now,
	}
	produceMov := domain.StockMovement{
		WarehouseID:   whID,
		StockItemID:   src.StockItemID,
		AssetID:       &produced.ID,
		MovementType:  domain.MovementRetrofitProduce,
		Quantity:      1,
		Reason:        in.Reason,
		ReferenceType: "asset_retrofit",
		ReferenceID:   &rec.ID,
		PerformedBy:   in.PerformedBy,
		PerformedAt:   now,
	}

	result, err := s.assetRetrofits.RecordRetrofit(ctx,
		port.RecordRetrofitPersist{
			Retrofit:        rec,
			ProducedAsset:   *produced,
			ConsumeMovement: consumeMov,
			ProduceMovement: produceMov,
		})
	if err != nil {
		return nil, err
	}
	// SourceAsset on result wasn't populated by the repo (it didn't
	// need the row); attach what we already loaded so the caller can
	// render both sides without a refetch.
	result.SourceAsset = *src
	return result, nil
}

func (s *Service) ListRetrofitsForAsset(
	ctx context.Context, sourceAssetID uuid.UUID,
) ([]domain.AssetRetrofit, error) {
	if s.assetRetrofits == nil {
		return nil, errRetrofitNotConfigured()
	}
	return s.assetRetrofits.ListForSource(ctx, sourceAssetID)
}
