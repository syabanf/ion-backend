// Wave 120 — in-memory fakes for the nocmon usecase tests.
//
// These fakes implement the FaultEventRepository, FaultImpactRepository,
// ServiceProbeRepository, HealthSampleRepository, TopologySnapshotRepository,
// and TopologyBuilder ports. Each is sync.Mutex-guarded so the
// usecase tests can exercise concurrent ticks without race detector
// false positives.

package usecase

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// FaultEventRepository
// =====================================================================

type memFaultRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.FaultEvent
}

func newMemFaultRepo() *memFaultRepo {
	return &memFaultRepo{rows: map[uuid.UUID]*domain.FaultEvent{}}
}

func (m *memFaultRepo) Create(_ context.Context, f *domain.FaultEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *f
	m.rows[f.ID] = &cp
	return nil
}

func (m *memFaultRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.FaultEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, derrors.NotFound("fault.not_found", "not found")
	}
	cp := *r
	return &cp, nil
}

func (m *memFaultRepo) List(_ context.Context, f port.FaultListFilter) ([]domain.FaultEvent, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.FaultEvent{}
	for _, r := range m.rows {
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		if f.Severity != "" && r.Severity != f.Severity {
			continue
		}
		if f.Kind != "" && r.Kind != f.Kind {
			continue
		}
		out = append(out, *r)
	}
	return out, len(out), nil
}

func (m *memFaultRepo) UpdateStatus(_ context.Context, f *domain.FaultEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[f.ID]; !ok {
		return derrors.NotFound("fault.not_found", "not found")
	}
	cp := *f
	m.rows[f.ID] = &cp
	return nil
}

func (m *memFaultRepo) ListOpenUnacked(_ context.Context, olderThan time.Time, limit int) ([]domain.FaultEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.FaultEvent{}
	for _, r := range m.rows {
		if r.Status != domain.FaultStatusOpen {
			continue
		}
		if r.AcknowledgedAt != nil {
			continue
		}
		if r.CreatedAt.After(olderThan) {
			continue
		}
		out = append(out, *r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// =====================================================================
// FaultImpactRepository
// =====================================================================

type memImpactRepo struct {
	mu   sync.Mutex
	rows []domain.FaultImpact
}

func newMemImpactRepo() *memImpactRepo { return &memImpactRepo{} }

func (m *memImpactRepo) Upsert(_ context.Context, i *domain.FaultImpact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for idx, r := range m.rows {
		if r.FaultEventID == i.FaultEventID && r.CustomerID == i.CustomerID {
			m.rows[idx] = *i
			return nil
		}
	}
	m.rows = append(m.rows, *i)
	return nil
}

func (m *memImpactRepo) ListForFault(_ context.Context, faultID uuid.UUID) ([]domain.FaultImpact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.FaultImpact{}
	for _, r := range m.rows {
		if r.FaultEventID == faultID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memImpactRepo) CountForFault(_ context.Context, faultID uuid.UUID) (int, error) {
	rows, _ := m.ListForFault(context.Background(), faultID)
	return len(rows), nil
}

// =====================================================================
// ServiceProbeRepository
// =====================================================================

type memProbeRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.ServiceProbe
}

func newMemProbeRepo() *memProbeRepo {
	return &memProbeRepo{rows: map[uuid.UUID]*domain.ServiceProbe{}}
}

func (m *memProbeRepo) Create(_ context.Context, p *domain.ServiceProbe) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	m.rows[p.ID] = &cp
	return nil
}

func (m *memProbeRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.ServiceProbe, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, derrors.NotFound("probe.not_found", "not found")
	}
	cp := *r
	return &cp, nil
}

func (m *memProbeRepo) List(_ context.Context, _ port.ProbeListFilter) ([]domain.ServiceProbe, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.ServiceProbe{}
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, len(out), nil
}

func (m *memProbeRepo) ListDue(_ context.Context, _ time.Time, limit int) ([]domain.ServiceProbe, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.ServiceProbe{}
	for _, r := range m.rows {
		out = append(out, *r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memProbeRepo) ListUnhealthy(_ context.Context, _ int) ([]domain.ServiceProbe, error) {
	return nil, nil
}

func (m *memProbeRepo) Update(_ context.Context, p *domain.ServiceProbe) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	m.rows[p.ID] = &cp
	return nil
}

func (m *memProbeRepo) UpdateLastSample(_ context.Context, id uuid.UUID, status domain.SampleStatus, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return derrors.NotFound("probe.not_found", "not found")
	}
	r.LastStatus = status
	r.LastProbedAt = &at
	return nil
}

func (m *memProbeRepo) UpdateActive(_ context.Context, p *domain.ServiceProbe) error {
	return m.Update(context.Background(), p)
}

// =====================================================================
// HealthSampleRepository
// =====================================================================

type memSampleRepo struct {
	mu                  sync.Mutex
	rows                []domain.HealthSample
	consecutiveOverride map[uuid.UUID]int // probeID → forced streak count
}

func newMemSampleRepo() *memSampleRepo {
	return &memSampleRepo{consecutiveOverride: map[uuid.UUID]int{}}
}

func (m *memSampleRepo) Insert(_ context.Context, s *domain.HealthSample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// idempotent on (probe_id, sampled_at)
	for _, r := range m.rows {
		if r.ProbeID == s.ProbeID && r.SampledAt.Equal(s.SampledAt) {
			return nil
		}
	}
	m.rows = append(m.rows, *s)
	return nil
}

func (m *memSampleRepo) ListForProbe(_ context.Context, f port.SampleListFilter) ([]domain.HealthSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.HealthSample{}
	for _, r := range m.rows {
		if r.ProbeID != f.ProbeID {
			continue
		}
		if f.From != nil && r.SampledAt.Before(*f.From) {
			continue
		}
		if f.To != nil && r.SampledAt.After(*f.To) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *memSampleRepo) CountConsecutive(_ context.Context, probeID uuid.UUID, status domain.SampleStatus, lookback int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.consecutiveOverride[probeID]; ok {
		return v, nil
	}
	// Walk newest-first up to lookback, count consecutive matches.
	cnt := 0
	// Sort by SampledAt desc inline.
	idxs := make([]int, len(m.rows))
	for i := range m.rows {
		idxs[i] = i
	}
	// Simple selection sort by SampledAt desc.
	for i := 0; i < len(idxs); i++ {
		for j := i + 1; j < len(idxs); j++ {
			if m.rows[idxs[j]].SampledAt.After(m.rows[idxs[i]].SampledAt) {
				idxs[i], idxs[j] = idxs[j], idxs[i]
			}
		}
	}
	checked := 0
	for _, i := range idxs {
		if m.rows[i].ProbeID != probeID {
			continue
		}
		if m.rows[i].Status != status {
			break
		}
		cnt++
		checked++
		if checked >= lookback {
			break
		}
	}
	return cnt, nil
}

// =====================================================================
// TopologySnapshotRepository
// =====================================================================

type memSnapshotRepo struct {
	mu   sync.Mutex
	rows []domain.TopologySnapshot
}

func newMemSnapshotRepo() *memSnapshotRepo { return &memSnapshotRepo{} }

func (m *memSnapshotRepo) Create(_ context.Context, s *domain.TopologySnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, *s)
	return nil
}

func (m *memSnapshotRepo) FindLatest(_ context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *domain.TopologySnapshot
	for i := range m.rows {
		r := m.rows[i]
		if r.Scope != scope {
			continue
		}
		if scopeID == nil && r.ScopeID != nil {
			continue
		}
		if scopeID != nil && (r.ScopeID == nil || *r.ScopeID != *scopeID) {
			continue
		}
		if latest == nil || r.SnapshotAt.After(latest.SnapshotAt) {
			cp := r
			latest = &cp
		}
	}
	if latest == nil {
		return nil, derrors.NotFound("snapshot.not_found", "no snapshot")
	}
	return latest, nil
}

// =====================================================================
// TopologyBuilder
// =====================================================================

type stubTopologyBuilder struct {
	mu        sync.Mutex
	calls     []stubBuilderCall
	wantScope domain.TopologyScope
	nodeCount int
	edgeCount int
}

type stubBuilderCall struct {
	scope   domain.TopologyScope
	scopeID *uuid.UUID
}

func (b *stubTopologyBuilder) BuildSnapshot(_ context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, stubBuilderCall{scope: scope, scopeID: scopeID})
	snap, err := domain.NewTopologySnapshot(scope, scopeID, []byte("{}"), b.nodeCount, b.edgeCount)
	if err != nil {
		return nil, err
	}
	return snap, nil
}
