// Wave 117 — Consumable batch management (Type 3) with FIFO consumption.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithConsumables(b port.ConsumableBatchRepository, l port.BatchConsumptionLogRepository) *Service {
	s.consumableBatches = b
	s.consumptionLogs = l
	return s
}

func errConsumablesNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "consumable.not_configured",
		"consumable batch repository is not configured for this service", nil)
}

func (s *Service) ReceiveConsumableBatch(ctx context.Context, in port.ReceiveConsumableBatchInput) (*domain.ConsumableBatch, error) {
	if s.consumableBatches == nil {
		return nil, errConsumablesNotConfigured()
	}
	b, err := domain.NewConsumableBatch(in.ItemID, in.BatchNo, in.TotalQty)
	if err != nil {
		return nil, err
	}
	b.ExpiryDate = in.ExpiryDate
	b.SupplierID = in.SupplierID
	b.CurrentWarehouseID = &in.WarehouseID
	b.UnitCost = in.UnitCost
	b.Notes = in.Notes
	if err := s.consumableBatches.Create(ctx, b); err != nil {
		return nil, err
	}
	s.auditf(ctx, "consumable.receive", "batch=%s item=%s qty=%d", b.ID, b.ItemID, b.TotalQty)
	return b, nil
}

// ConsumeFromBatch — when batchID is non-nil, decrement that specific
// batch. When batchID is nil, FIFO-pick the oldest in-stock batch for
// the item.
func (s *Service) ConsumeFromBatch(ctx context.Context, batchID *uuid.UUID, itemID *uuid.UUID, qty int, woID *uuid.UUID, byUserID uuid.UUID) (*domain.BatchConsumptionLog, error) {
	if s.consumableBatches == nil {
		return nil, errConsumablesNotConfigured()
	}
	var b *domain.ConsumableBatch
	var err error
	if batchID != nil {
		b, err = s.consumableBatches.FindByID(ctx, *batchID)
	} else {
		if itemID == nil {
			return nil, derrors.Validation("consumable.target_required",
				"either batch_id or item_id is required")
		}
		b, err = s.consumableBatches.FindOldestInStock(ctx, *itemID)
	}
	if err != nil {
		return nil, err
	}
	log, err := b.Consume(qty, woID, &byUserID)
	if err != nil {
		return nil, err
	}
	if err := s.consumableBatches.PersistConsumption(ctx, b, log); err != nil {
		return nil, err
	}
	s.auditf(ctx, "consumable.consume",
		"batch=%s qty=%d wo=%v remaining=%d", b.ID, qty, woID, b.RemainingQty)
	return log, nil
}

func (s *Service) ListConsumableBatches(ctx context.Context, f port.ConsumableBatchListFilter) ([]domain.ConsumableBatch, int, error) {
	if s.consumableBatches == nil {
		return nil, 0, errConsumablesNotConfigured()
	}
	return s.consumableBatches.List(ctx, f)
}

func (s *Service) GetConsumableBatch(ctx context.Context, id uuid.UUID) (*domain.ConsumableBatch, error) {
	if s.consumableBatches == nil {
		return nil, errConsumablesNotConfigured()
	}
	return s.consumableBatches.FindByID(ctx, id)
}

func (s *Service) ListConsumptionForBatch(ctx context.Context, batchID uuid.UUID, limit, offset int) ([]domain.BatchConsumptionLog, int, error) {
	if s.consumptionLogs == nil {
		return nil, 0, errConsumablesNotConfigured()
	}
	return s.consumptionLogs.ListForBatch(ctx, batchID, limit, offset)
}

func (s *Service) ListConsumptionForWO(ctx context.Context, woID uuid.UUID) ([]domain.BatchConsumptionLog, error) {
	if s.consumptionLogs == nil {
		return nil, errConsumablesNotConfigured()
	}
	return s.consumptionLogs.ListForWO(ctx, woID)
}

// ListExpiringSoon — daily housekeeping query.
func (s *Service) ListExpiringSoon(ctx context.Context, daysAhead int) ([]domain.ConsumableBatch, int, error) {
	if s.consumableBatches == nil {
		return nil, 0, errConsumablesNotConfigured()
	}
	return s.consumableBatches.List(ctx, port.ConsumableBatchListFilter{
		ExpiringWithinDays: &daysAhead,
		Status:             string(domain.ConsumableBatchStatusInStock),
	})
}

// MarkBatchExpired — admin/cron action.
func (s *Service) MarkBatchExpired(ctx context.Context, id uuid.UUID) (*domain.ConsumableBatch, error) {
	if s.consumableBatches == nil {
		return nil, errConsumablesNotConfigured()
	}
	b, err := s.consumableBatches.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := b.MarkExpired(); err != nil {
		return nil, err
	}
	if err := s.consumableBatches.UpdateStatus(ctx, b); err != nil {
		return nil, err
	}
	s.auditf(ctx, "consumable.mark_expired", "batch=%s", id)
	return b, nil
}

// SweepExpiredBatches is the cron pass that flips status to expired for
// all batches whose expiry_date is in the past. Returns the count moved.
func (s *Service) SweepExpiredBatches(ctx context.Context) (int, error) {
	if s.consumableBatches == nil {
		return 0, errConsumablesNotConfigured()
	}
	// Day 0 — we list all in_stock batches with expiry_date in the past
	// via ExpiringWithinDays=0. The DB filter is "expiry <= now + 0 days".
	zero := 0
	batches, _, err := s.consumableBatches.List(ctx, port.ConsumableBatchListFilter{
		ExpiringWithinDays: &zero,
		Status:             string(domain.ConsumableBatchStatusInStock),
	})
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	moved := 0
	for i := range batches {
		b := batches[i]
		if !b.IsExpired(now) {
			continue
		}
		if err := b.MarkExpired(); err != nil {
			continue
		}
		if err := s.consumableBatches.UpdateStatus(ctx, &b); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}
