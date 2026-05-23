package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// TopologyScope mirrors the DB CHECK enum on topology_snapshots.scope.
type TopologyScope string

const (
	TopologyScopeRegional TopologyScope = "regional"
	TopologyScopeBranch   TopologyScope = "branch"
	TopologyScopeSubArea  TopologyScope = "sub_area"
	TopologyScopeOLT      TopologyScope = "olt"
)

func (s TopologyScope) Valid() bool {
	switch s {
	case TopologyScopeRegional, TopologyScopeBranch, TopologyScopeSubArea, TopologyScopeOLT:
		return true
	}
	return false
}

// TopologySnapshot is one materialized topology blob. Payload is raw
// JSON (the BuilderPort owns the schema — the typical shape is a
// node-list + edge-list, but kept opaque here so the consumer can
// evolve without a migration).
//
// node_count + edge_count are denormalized at write time for cheap
// "dashboard header" reads — saves a JSON parse on every list call.
type TopologySnapshot struct {
	ID          uuid.UUID
	Scope       TopologyScope
	ScopeID     *uuid.UUID
	SnapshotAt  time.Time
	Payload     []byte
	NodeCount   int
	EdgeCount   int
	GeneratedBy string
}

// NewTopologySnapshot validates inputs and returns a ready-to-persist
// row. Scope is required; scope_id may be nil for the "regional"
// (entire network) shape.
func NewTopologySnapshot(scope TopologyScope, scopeID *uuid.UUID, payload []byte, nodeCount, edgeCount int) (*TopologySnapshot, error) {
	if !scope.Valid() {
		return nil, errors.Validation("topology.scope_invalid", "scope must be regional/branch/sub_area/olt")
	}
	if len(payload) == 0 {
		return nil, errors.Validation("topology.payload_required", "payload is required")
	}
	return &TopologySnapshot{
		ID:          uuid.New(),
		Scope:       scope,
		ScopeID:     scopeID,
		SnapshotAt:  time.Now().UTC(),
		Payload:     payload,
		NodeCount:   nodeCount,
		EdgeCount:   edgeCount,
		GeneratedBy: "system",
	}, nil
}
