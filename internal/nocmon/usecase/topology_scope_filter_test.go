// Wave 120 — topology scope filter edge.
//
// Pins TC-NTV-* "RebuildSnapshot(sub_area, X) must return a graph
// scoped to sub-area X only; sibling sub-areas in the same Area must
// NOT appear in the snapshot node count". The filter actually happens
// at the TopologyBuilder boundary (network bounded context); this test
// validates that the usecase faithfully threads the scope + scope_id
// down to the builder without mutation.

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
)

func TestTopologyService_RebuildSnapshot_ScopeFilterThreadedToBuilder(t *testing.T) {
	subAreaID := uuid.New()

	snapshots := newMemSnapshotRepo()
	builder := &stubTopologyBuilder{nodeCount: 12, edgeCount: 18}

	svc := NewTopologyService(snapshots, builder)
	snap, err := svc.RebuildSnapshot(context.Background(), domain.TopologyScopeSubArea, &subAreaID)
	if err != nil {
		t.Fatalf("RebuildSnapshot: %v", err)
	}
	if snap.Scope != domain.TopologyScopeSubArea {
		t.Errorf("snap.Scope = %s, want sub_area", snap.Scope)
	}
	if snap.ScopeID == nil || *snap.ScopeID != subAreaID {
		t.Errorf("snap.ScopeID = %v, want %s", snap.ScopeID, subAreaID)
	}

	// Builder must have been called exactly once with the same scope + id.
	if len(builder.calls) != 1 {
		t.Fatalf("builder calls = %d, want 1", len(builder.calls))
	}
	gotCall := builder.calls[0]
	if gotCall.scope != domain.TopologyScopeSubArea {
		t.Errorf("builder.scope = %s, want sub_area", gotCall.scope)
	}
	if gotCall.scopeID == nil || *gotCall.scopeID != subAreaID {
		t.Errorf("builder.scopeID = %v, want %s", gotCall.scopeID, subAreaID)
	}
}

func TestTopologyService_RebuildSnapshot_RegionalScopeAllowsNilID(t *testing.T) {
	// Regional scope is the root scope — scope_id may be nil because
	// there's exactly one regional snapshot. This test pins that nil
	// passes through without rejection.
	snapshots := newMemSnapshotRepo()
	builder := &stubTopologyBuilder{nodeCount: 200, edgeCount: 350}

	svc := NewTopologyService(snapshots, builder)
	snap, err := svc.RebuildSnapshot(context.Background(), domain.TopologyScopeRegional, nil)
	if err != nil {
		t.Fatalf("RebuildSnapshot regional: %v", err)
	}
	if snap.ScopeID != nil {
		t.Errorf("regional snap.ScopeID = %v, want nil", snap.ScopeID)
	}
	if len(builder.calls) != 1 || builder.calls[0].scopeID != nil {
		t.Errorf("builder must be called with nil scope_id; got %+v", builder.calls)
	}
}

func TestTopologyService_GetLatest_FiltersSubAreaSibling(t *testing.T) {
	// Build snapshots for two different sub-areas; GetLatest with one
	// of their IDs must NOT pick up the sibling's snapshot.
	subA := uuid.New()
	subB := uuid.New()
	snapshots := newMemSnapshotRepo()
	builder := &stubTopologyBuilder{nodeCount: 1, edgeCount: 0}

	svc := NewTopologyService(snapshots, builder)
	if _, err := svc.RebuildSnapshot(context.Background(), domain.TopologyScopeSubArea, &subA); err != nil {
		t.Fatalf("RebuildSnapshot subA: %v", err)
	}
	if _, err := svc.RebuildSnapshot(context.Background(), domain.TopologyScopeSubArea, &subB); err != nil {
		t.Fatalf("RebuildSnapshot subB: %v", err)
	}

	gotA, err := svc.GetLatest(context.Background(), domain.TopologyScopeSubArea, &subA)
	if err != nil {
		t.Fatalf("GetLatest subA: %v", err)
	}
	if gotA.ScopeID == nil || *gotA.ScopeID != subA {
		t.Errorf("got subA snapshot for wrong ID: %v", gotA.ScopeID)
	}
	gotB, err := svc.GetLatest(context.Background(), domain.TopologyScopeSubArea, &subB)
	if err != nil {
		t.Fatalf("GetLatest subB: %v", err)
	}
	if gotB.ScopeID == nil || *gotB.ScopeID != subB {
		t.Errorf("got subB snapshot for wrong ID: %v", gotB.ScopeID)
	}
}
