// Wave 117 — Cable lot management (Type 2).
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithCable(lots port.CableLotRepository, cuts port.CableCutRepository) *Service {
	s.cableLots = lots
	s.cableCuts = cuts
	return s
}

func errCableNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "cable.not_configured",
		"cable lot repository is not configured for this service", nil)
}

func (s *Service) ReceiveCableLot(ctx context.Context, in port.ReceiveCableLotInput) (*domain.CableLot, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	lot, err := domain.NewCableLot(in.ItemID, in.TotalLengthMeters)
	if err != nil {
		return nil, err
	}
	lot.LotNumber = in.LotNumber
	lot.DrumSerial = in.DrumSerial
	lot.SupplierID = in.SupplierID
	lot.CurrentWarehouseID = &in.WarehouseID
	lot.UnitCostPerMeter = in.UnitCostPerMeter
	lot.Notes = in.Notes
	if err := s.cableLots.Create(ctx, lot); err != nil {
		return nil, err
	}
	s.auditf(ctx, "cable.receive", "lot=%s item=%s total_m=%.2f", lot.ID, lot.ItemID, lot.TotalLengthMeters)
	return lot, nil
}

// CutSegment is the atomic operation. The repo's PersistCut wraps the
// lot update + cut insert in a single pgx tx.
func (s *Service) CutSegment(ctx context.Context, lotID uuid.UUID, length float64, woID *uuid.UUID, byUserID uuid.UUID) (*domain.CableCut, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	lot, err := s.cableLots.FindByID(ctx, lotID)
	if err != nil {
		return nil, err
	}
	cut, err := lot.CutSegment(length, woID, &byUserID)
	if err != nil {
		return nil, err
	}
	if err := s.cableLots.PersistCut(ctx, lot, cut); err != nil {
		return nil, err
	}
	s.auditf(ctx, "cable.cut", "lot=%s cut=%.2f m wo=%v remaining=%.2f",
		lot.ID, cut.CutLengthMeters, woID, lot.RemainingLengthMeters)
	return cut, nil
}

func (s *Service) ListCableLots(ctx context.Context, f port.CableLotListFilter) ([]domain.CableLot, int, error) {
	if s.cableLots == nil {
		return nil, 0, errCableNotConfigured()
	}
	return s.cableLots.List(ctx, f)
}

func (s *Service) GetCableLot(ctx context.Context, id uuid.UUID) (*domain.CableLot, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	return s.cableLots.FindByID(ctx, id)
}

func (s *Service) ListCableCuts(ctx context.Context, lotID uuid.UUID, limit, offset int) ([]domain.CableCut, int, error) {
	if s.cableCuts == nil {
		return nil, 0, errCableNotConfigured()
	}
	return s.cableCuts.ListForLot(ctx, lotID, limit, offset)
}

// ListLowRemaining returns lots with remaining meters below the
// threshold. The downstream alert flow + dashboard view consume this.
func (s *Service) ListLowRemainingCableLots(ctx context.Context, thresholdMeters float64) ([]domain.CableLot, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	f := port.CableLotListFilter{LowRemainingThresholdMeters: &thresholdMeters, Status: string(domain.CableLotStatusInStock)}
	lots, _, err := s.cableLots.List(ctx, f)
	return lots, err
}

// DisposeCableLot — admin action. Caller passes a reason.
func (s *Service) DisposeCableLot(ctx context.Context, id uuid.UUID, reason string) (*domain.CableLot, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	lot, err := s.cableLots.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := lot.Dispose(reason); err != nil {
		return nil, err
	}
	if err := s.cableLots.UpdateStatus(ctx, lot); err != nil {
		return nil, err
	}
	s.auditf(ctx, "cable.dispose", "lot=%s reason=%s", id, reason)
	return lot, nil
}

// Helper for usecase tests + callers needing a hint at "today".
var nowFn = func() time.Time { return time.Now().UTC() }
