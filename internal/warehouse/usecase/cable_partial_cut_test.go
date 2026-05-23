// Wave 120 — cable partial-cut edge: refuse a cut exceeding remaining.
//
// Pins TC-IT2-* "if a technician requests a 60m cable cut from a lot
// with 50m remaining, the cut must be refused with a domain validation
// error". The bare domain test
// (cable_lot_test.go::TestCableLot_CutSegment_RefusesOverdraft) covers
// the domain primitive; this test exercises the SAME refusal at the
// usecase boundary so the contract is pinned at both layers.

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
)

func TestCutSegment_RefusesOverRemaining(t *testing.T) {
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
		TotalLengthMeters: 50,
		WarehouseID:       uuid.New(),
	})
	if err != nil {
		t.Fatalf("ReceiveCableLot: %v", err)
	}
	// Attempt to cut 60 from a 50m lot.
	_, err = s.CutSegment(context.Background(), lot.ID, 60, nil, uuid.New())
	if err == nil {
		t.Fatalf("expected refusal cutting 60m from a 50m lot")
	}

	// Verify the lot was not mutated.
	got, _ := s.GetCableLot(context.Background(), lot.ID)
	if got.RemainingLengthMeters != 50 {
		t.Errorf("lot.RemainingLengthMeters = %v, want unchanged 50 after refused cut", got.RemainingLengthMeters)
	}
}

func TestCutSegment_ExactRemaining_OK(t *testing.T) {
	// Boundary: cut exactly the remaining length — should succeed and
	// flip the lot to consumed.
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
		TotalLengthMeters: 75,
		WarehouseID:       uuid.New(),
	})
	if err != nil {
		t.Fatalf("ReceiveCableLot: %v", err)
	}
	_, err = s.CutSegment(context.Background(), lot.ID, 75, nil, uuid.New())
	if err != nil {
		t.Fatalf("exact-length cut should succeed: %v", err)
	}
	got, _ := s.GetCableLot(context.Background(), lot.ID)
	if got.RemainingLengthMeters != 0 {
		t.Errorf("after exact cut, remaining = %v, want 0", got.RemainingLengthMeters)
	}
}
