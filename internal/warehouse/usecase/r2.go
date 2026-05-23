// M3 round-2 service methods: thresholds, alerts, opname workflow.
//
// Each method first checks the corresponding repo isn't nil so cmd
// wirings that haven't called WithR2 fail gracefully instead of
// panicking. In practice cmd/warehouse-svc/main.go always wires r2.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Thresholds
// =====================================================================

func (s *Service) SetThreshold(ctx context.Context, in port.SetThresholdInput) error {
	if s.thresholds == nil {
		return derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"threshold repo not configured")
	}
	if in.WarehouseID == uuid.Nil || in.StockItemID == uuid.Nil {
		return derrors.Validation("threshold.ids_required",
			"warehouse_id and stock_item_id are required")
	}
	if in.MinThreshold != nil && *in.MinThreshold < 0 {
		return derrors.Validation("threshold.invalid",
			"min_threshold must be ≥ 0")
	}
	return s.thresholds.Set(ctx, in.WarehouseID, in.StockItemID, in.MinThreshold)
}

// =====================================================================
// Alerts
// =====================================================================

func (s *Service) ListStockAlerts(ctx context.Context, f port.AlertFilter) ([]domain.StockAlert, error) {
	if s.alerts == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"alerts repo not configured")
	}
	return s.alerts.ListBelowThreshold(ctx, f.BranchID)
}

// RunAlertCascadeTick is the cron-callable Wave 88 tick. Runs the
// two-step state sync (open new states, close recovered ones) then
// bumps escalation levels for any open state past its budget.
func (s *Service) RunAlertCascadeTick(
	ctx context.Context, subToArea, areaToRegional time.Duration,
) (opened, closed, escalated int, err error) {
	if s.alerts == nil {
		return 0, 0, 0, derrors.New(derrors.KindInternal,
			"alert.not_configured", "alert repo not wired")
	}
	opened, closed, err = s.alerts.SyncAlertStates(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	escalated, err = s.alerts.CascadeEscalations(ctx, subToArea, areaToRegional)
	if err != nil {
		return opened, closed, 0, err
	}
	return opened, closed, escalated, nil
}

// =====================================================================
// Opname workflow
// =====================================================================

// StartOpname creates a new open session for the warehouse. The
// partial-unique index in the DB enforces "at most one open per
// warehouse" — duplicate starts surface as a Conflict.
func (s *Service) StartOpname(ctx context.Context, in port.StartOpnameInput) (*port.OpnameView, error) {
	if s.opnames == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	if in.WarehouseID == uuid.Nil {
		return nil, derrors.Validation("opname.warehouse_required",
			"warehouse_id is required")
	}
	// Sanity: warehouse must exist.
	if _, err := s.warehouses.FindByID(ctx, in.WarehouseID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := &domain.OpnameSession{
		ID:            uuid.New(),
		SessionNumber: domain.GenerateOpnameNumber(now),
		WarehouseID:   in.WarehouseID,
		Status:        domain.OpnameStatusOpen,
		StartedBy:     &in.StartedBy,
		StartedAt:     now,
		Notes:         in.Notes,
	}
	if err := s.opnames.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	return s.opnames.FindSession(ctx, sess.ID)
}

func (s *Service) GetOpname(ctx context.Context, id uuid.UUID) (*port.OpnameView, error) {
	if s.opnames == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	return s.opnames.FindSession(ctx, id)
}

func (s *Service) ListOpnameSessions(ctx context.Context, warehouseID *uuid.UUID, status string, limit, offset int) ([]port.OpnameView, int, error) {
	if s.opnames == nil {
		return nil, 0, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	return s.opnames.ListSessions(ctx, warehouseID, status, limit, offset)
}

// UpsertOpnameCount records one count line. On first upsert we capture
// the system's current count as `expected_qty` so the audit row shows
// what the system thought was there at count time. The source of that
// "current count" depends on the item:
//
//   - non-serialized (cable / consumable): stock_levels.quantity
//   - serialized (devices / infrastructure): COUNT(assets in_stock)
//
// Subsequent upserts only adjust counted_qty / decision / notes —
// expected stays frozen at first count to keep variance honest across
// edits.
func (s *Service) UpsertOpnameCount(ctx context.Context, in port.UpsertOpnameCountInput) (*domain.OpnameCount, error) {
	if s.opnames == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	if in.CountedQty < 0 {
		return nil, derrors.Validation("opname.counted_invalid",
			"counted_qty must be ≥ 0")
	}
	view, err := s.opnames.FindSession(ctx, in.SessionID)
	if err != nil {
		return nil, err
	}
	if err := view.Session.AssertCanCount(); err != nil {
		return nil, err
	}

	// Resolve the item so we know how to compute its expected quantity.
	item, err := s.items.FindByID(ctx, in.StockItemID)
	if err != nil {
		return nil, err
	}

	expected := 0.0
	if item.Serialized {
		// Count in_stock assets for (warehouse, item).
		whID := view.Session.WarehouseID
		_, total, err := s.assets.List(ctx, port.AssetListFilter{
			WarehouseID: &whID,
			StockItemID: &in.StockItemID,
			Status:      string(domain.AssetStatusInStock),
			Limit:       1, // we only need total
		})
		if err != nil {
			return nil, err
		}
		expected = float64(total)
	} else if lvl, _ := s.levels.Get(ctx, view.Session.WarehouseID, in.StockItemID); lvl != nil {
		expected = lvl.Quantity
	}
	c := &domain.OpnameCount{
		ID:                   uuid.New(),
		SessionID:            in.SessionID,
		StockItemID:          in.StockItemID,
		ExpectedQty:          expected,
		CountedQty:           in.CountedQty,
		Variance:             in.CountedQty - expected,
		CableRemnantDecision: in.CableRemnantDecision,
		Notes:                in.Notes,
		CountedBy:            &in.CountedBy,
		CountedAt:            time.Now().UTC(),
	}
	return s.opnames.UpsertCount(ctx, c)
}

// CommitOpname applies every count's variance to live stock_levels and
// writes an opname_adjustment movement per line. Special cable handling:
// when cable_remnant_decision='scrap' we set the target to 0 (not the
// counted remnant) so the write-off is recorded as the actual loss.
//
// We don't wrap this in a single tx — same trade-off as Intake. A
// failure mid-loop leaves some movements applied; the session stays
// 'open' so the operator can re-run / cancel.
func (s *Service) CommitOpname(ctx context.Context, id, performedBy uuid.UUID) (*port.OpnameView, error) {
	if s.opnames == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	view, err := s.opnames.FindSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := view.Session.AssertCanCommit(); err != nil {
		return nil, err
	}
	if len(view.Counts) == 0 {
		return nil, derrors.Validation("opname.empty",
			"cannot commit an opname session with no count lines")
	}

	now := time.Now().UTC()
	whID := view.Session.WarehouseID

	for _, cv := range view.Counts {
		// Decide the target live quantity.
		target := cv.Count.CountedQty
		if cv.IsCable && cv.Count.CableRemnantDecision != nil &&
			*cv.Count.CableRemnantDecision == domain.CableRemnantScrap {
			target = 0
		}
		delta := target - cv.Count.ExpectedQty
		if delta == 0 {
			continue
		}

		// Resolve item to know if it's serialized — serialized items
		// have no stock_levels row; their truth lives in the assets
		// table. We record the variance as an audit movement but do
		// NOT touch stock_levels. The operator must reconcile missing
		// or surplus serials manually via the asset registry.
		item, err := s.items.FindByID(ctx, cv.Count.StockItemID)
		if err != nil {
			return nil, err
		}

		reason := "opname adjustment"
		if cv.IsCable && cv.Count.CableRemnantDecision != nil {
			reason += " (cable: " + string(*cv.Count.CableRemnantDecision) + ")"
		}
		if item.Serialized {
			reason += " (serialized — asset registry update required)"
		} else {
			if _, err := s.levels.UpsertDelta(ctx, whID, cv.Count.StockItemID, delta); err != nil {
				return nil, err
			}
		}

		sessRef := view.Session.ID
		mv := &domain.StockMovement{
			ID:            uuid.New(),
			WarehouseID:   whID,
			StockItemID:   cv.Count.StockItemID,
			MovementType:  domain.MovementOpnameAdjustment,
			Quantity:      delta,
			ReferenceType: "opname_session",
			ReferenceID:   &sessRef,
			PerformedBy:   &performedBy,
			PerformedAt:   now,
			Reason:        reason,
		}
		if err := s.movements.Record(ctx, mv); err != nil {
			return nil, err
		}
	}

	if err := s.opnames.UpdateSessionStatus(ctx, id, domain.OpnameStatusCommitted, now); err != nil {
		return nil, err
	}
	return s.opnames.FindSession(ctx, id)
}

func (s *Service) CancelOpname(ctx context.Context, id, performedBy uuid.UUID) (*port.OpnameView, error) {
	if s.opnames == nil {
		return nil, derrors.New(derrors.KindInternal, "warehouse.r2_not_wired",
			"opname repo not configured")
	}
	view, err := s.opnames.FindSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := view.Session.AssertCanCancel(); err != nil {
		return nil, err
	}
	_ = performedBy // round 1: not recorded on the session row; reserved
	if err := s.opnames.UpdateSessionStatus(ctx, id, domain.OpnameStatusCancelled, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.opnames.FindSession(ctx, id)
}
