package usecase

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
)

// fakeBOMItem is a thin pointer wrapper so the map values aren't copied
// on every read. The exec service treats items by value.
type fakeBOMItem = domain.BulkODPMigrationItem
type fakeBWOItem = domain.BulkWOCreationItem

// fakeBOM — in-memory repo for ODP migration items.
type fakeBOM struct {
	mu    sync.Mutex
	items map[uuid.UUID]*fakeBOMItem
}

func (r *fakeBOM) CreateBatch(_ context.Context, items []domain.BulkODPMigrationItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range items {
		clone := items[i]
		r.items[clone.ID] = &clone
	}
	return nil
}
func (r *fakeBOM) Update(_ context.Context, it *domain.BulkODPMigrationItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *it
	r.items[it.ID] = &clone
	return nil
}
func (r *fakeBOM) FindByID(_ context.Context, id uuid.UUID) (*domain.BulkODPMigrationItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if it, ok := r.items[id]; ok {
		clone := *it
		return &clone, nil
	}
	return nil, nil
}
func (r *fakeBOM) ListByJob(_ context.Context, jobID uuid.UUID, _, _ int) ([]domain.BulkODPMigrationItem, error) {
	return r.match(jobID, nil), nil
}
func (r *fakeBOM) ListUnprocessedForJob(_ context.Context, jobID uuid.UUID, _ int) ([]domain.BulkODPMigrationItem, error) {
	open := map[domain.BulkODPMigrationItemStatus]bool{
		domain.BOMItemQueued:     true,
		domain.BOMItemValidating: true,
		domain.BOMItemValidated:  true,
		domain.BOMItemStaged:     true,
	}
	return r.match(jobID, open), nil
}
func (r *fakeBOM) match(jobID uuid.UUID, statuses map[domain.BulkODPMigrationItemStatus]bool) []domain.BulkODPMigrationItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.BulkODPMigrationItem{}
	for _, it := range r.items {
		if it.BulkJobID != jobID {
			continue
		}
		if statuses != nil && !statuses[it.Status] {
			continue
		}
		out = append(out, *it)
	}
	return out
}

// fakeBWO — in-memory repo for WO creation items.
type fakeBWO struct {
	mu    sync.Mutex
	items map[uuid.UUID]*fakeBWOItem
}

func (r *fakeBWO) CreateBatch(_ context.Context, items []domain.BulkWOCreationItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range items {
		clone := items[i]
		r.items[clone.ID] = &clone
	}
	return nil
}
func (r *fakeBWO) Update(_ context.Context, it *domain.BulkWOCreationItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *it
	r.items[it.ID] = &clone
	return nil
}
func (r *fakeBWO) FindByID(_ context.Context, id uuid.UUID) (*domain.BulkWOCreationItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if it, ok := r.items[id]; ok {
		clone := *it
		return &clone, nil
	}
	return nil, nil
}
func (r *fakeBWO) ListByJob(_ context.Context, jobID uuid.UUID, _, _ int) ([]domain.BulkWOCreationItem, error) {
	return r.match(jobID, nil), nil
}
func (r *fakeBWO) ListUnprocessedForJob(_ context.Context, jobID uuid.UUID, _ int) ([]domain.BulkWOCreationItem, error) {
	open := map[domain.BulkWOCreationItemStatus]bool{
		domain.BWOItemQueued:     true,
		domain.BWOItemValidating: true,
		domain.BWOItemValidated:  true,
	}
	return r.match(jobID, open), nil
}
func (r *fakeBWO) match(jobID uuid.UUID, statuses map[domain.BulkWOCreationItemStatus]bool) []domain.BulkWOCreationItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.BulkWOCreationItem{}
	for _, it := range r.items {
		if it.BulkJobID != jobID {
			continue
		}
		if statuses != nil && !statuses[it.Status] {
			continue
		}
		out = append(out, *it)
	}
	return out
}
