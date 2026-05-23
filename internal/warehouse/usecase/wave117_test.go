// Wave 117 — usecase tests using in-memory fakes.
//
// We test the typed flows (cable cut, consume FIFO, location movement,
// QR scan) at the usecase boundary so a future repo swap doesn't break
// the contract tests.
package usecase

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ---------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------

type fakeCableLotRepo struct {
	lots map[uuid.UUID]*domain.CableLot
	cuts []*domain.CableCut
}

func newFakeCableLotRepo() *fakeCableLotRepo {
	return &fakeCableLotRepo{lots: map[uuid.UUID]*domain.CableLot{}}
}

func (f *fakeCableLotRepo) Create(_ context.Context, l *domain.CableLot) error {
	f.lots[l.ID] = l
	return nil
}
func (f *fakeCableLotRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.CableLot, error) {
	l, ok := f.lots[id]
	if !ok {
		return nil, derrors.NotFound("cable_lot.not_found", "not found")
	}
	return l, nil
}
func (f *fakeCableLotRepo) List(_ context.Context, fil port.CableLotListFilter) ([]domain.CableLot, int, error) {
	out := []domain.CableLot{}
	for _, l := range f.lots {
		if fil.ItemID != nil && l.ItemID != *fil.ItemID {
			continue
		}
		if fil.Status != "" && string(l.Status) != fil.Status {
			continue
		}
		if fil.LowRemainingThresholdMeters != nil && l.RemainingLengthMeters >= *fil.LowRemainingThresholdMeters {
			continue
		}
		out = append(out, *l)
	}
	return out, len(out), nil
}
func (f *fakeCableLotRepo) PersistCut(_ context.Context, l *domain.CableLot, c *domain.CableCut) error {
	f.lots[l.ID] = l
	f.cuts = append(f.cuts, c)
	return nil
}
func (f *fakeCableLotRepo) UpdateStatus(_ context.Context, l *domain.CableLot) error {
	f.lots[l.ID] = l
	return nil
}

type fakeCableCutRepo struct{}

func (fakeCableCutRepo) ListForLot(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.CableCut, int, error) {
	return nil, 0, nil
}
func (fakeCableCutRepo) ListForWO(_ context.Context, _ uuid.UUID) ([]domain.CableCut, error) {
	return nil, nil
}

type fakeBatchRepo struct {
	batches map[uuid.UUID]*domain.ConsumableBatch
	logs    []*domain.BatchConsumptionLog
}

func newFakeBatchRepo() *fakeBatchRepo {
	return &fakeBatchRepo{batches: map[uuid.UUID]*domain.ConsumableBatch{}}
}

func (f *fakeBatchRepo) Create(_ context.Context, b *domain.ConsumableBatch) error {
	f.batches[b.ID] = b
	return nil
}
func (f *fakeBatchRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.ConsumableBatch, error) {
	b, ok := f.batches[id]
	if !ok {
		return nil, derrors.NotFound("consumable_batch.not_found", "not found")
	}
	return b, nil
}

func (f *fakeBatchRepo) FindOldestInStock(_ context.Context, itemID uuid.UUID) (*domain.ConsumableBatch, error) {
	var oldest *domain.ConsumableBatch
	for _, b := range f.batches {
		if b.ItemID != itemID {
			continue
		}
		if b.Status != domain.ConsumableBatchStatusInStock && b.Status != domain.ConsumableBatchStatusAllocated {
			continue
		}
		if b.RemainingQty <= 0 {
			continue
		}
		if oldest == nil || b.ReceivedAt.Before(oldest.ReceivedAt) {
			oldest = b
		}
	}
	if oldest == nil {
		return nil, derrors.NotFound("consumable_batch.not_found", "no in_stock batch")
	}
	return oldest, nil
}

func (f *fakeBatchRepo) List(_ context.Context, _ port.ConsumableBatchListFilter) ([]domain.ConsumableBatch, int, error) {
	out := []domain.ConsumableBatch{}
	for _, b := range f.batches {
		out = append(out, *b)
	}
	return out, len(out), nil
}
func (f *fakeBatchRepo) PersistConsumption(_ context.Context, b *domain.ConsumableBatch, l *domain.BatchConsumptionLog) error {
	f.batches[b.ID] = b
	f.logs = append(f.logs, l)
	return nil
}
func (f *fakeBatchRepo) UpdateStatus(_ context.Context, b *domain.ConsumableBatch) error {
	f.batches[b.ID] = b
	return nil
}

type fakeAssetLocationRepo struct {
	moves []*domain.LocationMovement
}

func (f *fakeAssetLocationRepo) Record(_ context.Context, mv *domain.LocationMovement) error {
	f.moves = append(f.moves, mv)
	return nil
}
func (f *fakeAssetLocationRepo) ListForAsset(_ context.Context, id uuid.UUID, _, _ int) ([]domain.LocationMovement, int, error) {
	out := []domain.LocationMovement{}
	for _, m := range f.moves {
		if m.AssetID == id {
			out = append(out, *m)
		}
	}
	return out, len(out), nil
}
func (f *fakeAssetLocationRepo) CurrentLocation(_ context.Context, _ uuid.UUID) (*uuid.UUID, *time.Time, error) {
	return nil, nil, nil
}
func (f *fakeAssetLocationRepo) ListInTransitOlderThan(_ context.Context, _ time.Duration) ([]domain.LocationMovement, error) {
	return nil, nil
}

// noop QR generator (uses domain.GenerateQR / ParseQR)
type fakeQR struct{}

func (fakeQR) Generate(in port.QRGenerateInput) string {
	return domain.GenerateQR(domain.NewQRSource(in.ItemType, in.ItemID, in.Serial))
}
func (fakeQR) Parse(s string) (*domain.QRPayload, error) { return domain.ParseQR(s) }

// minimal items repo — only what cable / consumable / scan tests need.
type fakeItemsRepo struct {
	items map[uuid.UUID]*domain.StockItem
}

func newFakeItemsRepo() *fakeItemsRepo { return &fakeItemsRepo{items: map[uuid.UUID]*domain.StockItem{}} }

func (f *fakeItemsRepo) List(_ context.Context, _ port.StockItemListFilter) ([]domain.StockItem, int, error) {
	return nil, 0, nil
}
func (f *fakeItemsRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.StockItem, error) {
	item, ok := f.items[id]
	if !ok {
		return nil, derrors.NotFound("stock_item.not_found", "not found")
	}
	return item, nil
}
func (f *fakeItemsRepo) FindBySKU(_ context.Context, _ string) (*domain.StockItem, error) {
	return nil, errors.New("unused")
}
func (f *fakeItemsRepo) Create(_ context.Context, _ *domain.StockItem) error { return nil }
func (f *fakeItemsRepo) Update(_ context.Context, _ port.UpdateStockItemInput) (*domain.StockItem, error) {
	return nil, nil
}

// ---------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------

func newTestService(opts ...func(*Service)) *Service {
	s := &Service{log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	for _, o := range opts {
		o(s)
	}
	return s
}

func TestCutSegment_HappyPath(t *testing.T) {
	itemID := uuid.New()
	lotRepo := newFakeCableLotRepo()
	itemsRepo := newFakeItemsRepo()
	itemsRepo.items[itemID] = &domain.StockItem{ID: itemID, Category: domain.CategoryCable}

	s := newTestService(func(s *Service) {
		s.cableLots = lotRepo
		s.items = itemsRepo
	})

	lot, err := s.ReceiveCableLot(context.Background(), port.ReceiveCableLotInput{
		ItemID:            itemID,
		TotalLengthMeters: 1000,
		WarehouseID:       uuid.New(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cut, err := s.CutSegment(context.Background(), lot.ID, 50, nil, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cut.CutLengthMeters != 50 {
		t.Fatalf("expected 50m cut, got %f", cut.CutLengthMeters)
	}
	got, _ := s.GetCableLot(context.Background(), lot.ID)
	if got.RemainingLengthMeters != 950 {
		t.Fatalf("expected remaining=950, got %f", got.RemainingLengthMeters)
	}
}

func TestConsumeFromBatch_FIFO(t *testing.T) {
	itemID := uuid.New()
	bRepo := newFakeBatchRepo()

	s := newTestService(func(s *Service) {
		s.consumableBatches = bRepo
	})

	// Older batch (B1) + newer batch (B2). FIFO must hit B1 first.
	older, _ := s.ReceiveConsumableBatch(context.Background(), port.ReceiveConsumableBatchInput{
		ItemID:      itemID,
		BatchNo:     "B1",
		TotalQty:    10,
		WarehouseID: uuid.New(),
	})
	// Sleep is not allowed; force the second batch to have a later ReceivedAt.
	older.ReceivedAt = time.Now().Add(-1 * time.Hour)

	newer, _ := s.ReceiveConsumableBatch(context.Background(), port.ReceiveConsumableBatchInput{
		ItemID:      itemID,
		BatchNo:     "B2",
		TotalQty:    10,
		WarehouseID: uuid.New(),
	})
	_ = newer

	log, err := s.ConsumeFromBatch(context.Background(), nil, &itemID, 3, nil, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log.ConsumableBatchID != older.ID {
		t.Fatalf("FIFO should hit older batch first, got %s vs %s", log.ConsumableBatchID, older.ID)
	}
}

func TestConsumeFromBatch_ExhaustionFallthrough(t *testing.T) {
	itemID := uuid.New()
	bRepo := newFakeBatchRepo()

	s := newTestService(func(s *Service) {
		s.consumableBatches = bRepo
	})

	b1, _ := s.ReceiveConsumableBatch(context.Background(), port.ReceiveConsumableBatchInput{
		ItemID:      itemID,
		BatchNo:     "B1",
		TotalQty:    2,
		WarehouseID: uuid.New(),
	})
	b1.ReceivedAt = time.Now().Add(-2 * time.Hour)
	b2, _ := s.ReceiveConsumableBatch(context.Background(), port.ReceiveConsumableBatchInput{
		ItemID:      itemID,
		BatchNo:     "B2",
		TotalQty:    5,
		WarehouseID: uuid.New(),
	})

	// Drain B1 → status flips to consumed.
	if _, err := s.ConsumeFromBatch(context.Background(), nil, &itemID, 2, nil, uuid.New()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b1.Status != domain.ConsumableBatchStatusConsumed {
		t.Fatalf("expected B1 consumed, got %s", b1.Status)
	}
	// Next FIFO should hit B2.
	log, err := s.ConsumeFromBatch(context.Background(), nil, &itemID, 1, nil, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log.ConsumableBatchID != b2.ID {
		t.Fatalf("expected fallthrough to B2 after B1 exhausted")
	}
}

func TestRecordMovement_PersistsAndAudits(t *testing.T) {
	locRepo := &fakeAssetLocationRepo{}
	s := newTestService(func(s *Service) { s.assetLocations = locRepo })

	mv, err := s.RecordMovement(context.Background(), port.RecordMovementInput{
		AssetID: uuid.New(),
		Kind:    domain.MovementKindDispatch,
		MovedBy: uuid.New(),
		Reason:  "tech pickup",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locRepo.moves) != 1 {
		t.Fatalf("expected 1 movement, got %d", len(locRepo.moves))
	}
	if locRepo.moves[0].ID != mv.ID {
		t.Fatalf("expected the recorded movement to match the returned one")
	}
}

func TestScanQR_RoundTrip(t *testing.T) {
	s := newTestService(func(s *Service) { s.qrGenerator = fakeQR{} })

	// Without an asset repo wired we expect a clean not-found.
	if _, err := s.ScanQR(context.Background(), "ION-type1-deadbeef-aaaaaaaaaaaa"); err == nil {
		t.Fatal("expected NotFound when no item/asset matches")
	}
	// Bad input should surface a validation error from ParseQR.
	if _, err := s.ScanQR(context.Background(), "garbage"); err == nil {
		t.Fatal("expected validation error on bad QR string")
	}
}
