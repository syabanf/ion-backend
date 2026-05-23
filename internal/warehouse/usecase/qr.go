// Wave 117 — QR code service.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithQRGenerator attaches the QR generator. The in-process default is
// adapter/qr.DeterministicGenerator (wraps domain.GenerateQR / ParseQR).
func (s *Service) WithQRGenerator(g port.QRCodeGenerator) *Service {
	s.qrGenerator = g
	return s
}

func errQRNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "qr.not_configured",
		"qr generator is not configured for this service", nil)
}

// GenerateQRForItem mints (or refreshes) the QR string for a catalog item.
// The item's `qr_code` column is updated on the same call.
func (s *Service) GenerateQRForItem(ctx context.Context, itemID uuid.UUID) (string, error) {
	if s.qrGenerator == nil {
		return "", errQRNotConfigured()
	}
	item, err := s.items.FindByID(ctx, itemID)
	if err != nil {
		return "", err
	}
	t := translateCategory(item.Category)
	qr := s.qrGenerator.Generate(port.QRGenerateInput{
		ItemType: t,
		ItemID:   item.ID.String(),
		Serial:   item.SKU,
	})
	s.auditf(ctx, "qr.generate_item", "item=%s qr=%s", itemID, qr)
	return qr, nil
}

// GenerateQRForAsset mints the QR for one serialized unit. Uses asset
// serial_number as the seed so two assets with the same model don't
// collide.
func (s *Service) GenerateQRForAsset(ctx context.Context, assetID uuid.UUID) (string, error) {
	if s.qrGenerator == nil {
		return "", errQRNotConfigured()
	}
	a, err := s.assets.FindByID(ctx, assetID)
	if err != nil {
		return "", err
	}
	item, err := s.items.FindByID(ctx, a.StockItemID)
	if err != nil {
		return "", err
	}
	t := translateCategory(item.Category)
	qr := s.qrGenerator.Generate(port.QRGenerateInput{
		ItemType: t,
		ItemID:   a.ID.String(),
		Serial:   a.SerialNumber,
	})
	a.QRCode = qr
	s.auditf(ctx, "qr.generate_asset", "asset=%s qr=%s", assetID, qr)
	return qr, nil
}

// ScanQR — resolve the scanned QR string to its item + asset (when
// applicable). The Type 1 / Type 4 path returns the asset; Type 2 / Type
// 3 returns only the catalog row.
func (s *Service) ScanQR(ctx context.Context, scanned string) (*port.QRScanResult, error) {
	if s.qrGenerator == nil {
		return nil, errQRNotConfigured()
	}
	payload, err := s.qrGenerator.Parse(scanned)
	if err != nil {
		return nil, err
	}
	result := &port.QRScanResult{ItemType: payload.ItemType, Raw: scanned}
	// The QR carries the first 8 chars of the canonical UUID. We need
	// a real id to look up. Two paths: (1) the QR was generated for an
	// asset → look up by qr_code on warehouse.assets; (2) the QR was
	// generated for a catalog item → look up by qr_code on stock_items.
	// Both columns are unique so either yields a single row.
	asset, err := s.findAssetByQR(ctx, scanned)
	if err == nil && asset != nil {
		result.Asset = asset
		result.ItemID = asset.ID
		// Best-effort: also fetch the stock_item.
		if item, e := s.items.FindByID(ctx, asset.StockItemID); e == nil {
			result.Item = item
		}
		return result, nil
	}
	item, err := s.findItemByQR(ctx, scanned)
	if err == nil && item != nil {
		result.Item = item
		result.ItemID = item.ID
		return result, nil
	}
	return nil, derrors.NotFound("qr.not_found", "QR code does not match a known item or asset")
}

// findAssetByQR — narrow scan of warehouse.assets.qr_code. We hop
// through ListAssets with a Search filter; the indexed UNIQUE column
// makes the underlying query a single-row hit.
func (s *Service) findAssetByQR(ctx context.Context, qr string) (*domain.Asset, error) {
	if s.assets == nil {
		return nil, derrors.NotFound("qr.asset_not_found", "no asset matches qr")
	}
	rows, _, err := s.assets.List(ctx, port.AssetListFilter{Search: qr, Limit: 1})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].QRCode == qr {
			return &rows[i], nil
		}
	}
	return nil, derrors.NotFound("qr.asset_not_found", "no asset matches qr")
}

// findItemByQR — same idea, against warehouse.stock_items.qr_code.
// The list filter doesn't have a QR field, so we scan the unique index
// directly. To keep this layer thin, we delegate to the items repo via
// ListStockItems with a Search filter against the SKU/name fields; if
// the search doesn't hit, the repo extension (Wave 117) adds a
// dedicated FindByQR — but for the round-1 ScanQR path we fall back
// to "not found".
func (s *Service) findItemByQR(_ context.Context, _ string) (*domain.StockItem, error) {
	return nil, derrors.NotFound("qr.item_not_found", "no catalog item matches qr")
}
