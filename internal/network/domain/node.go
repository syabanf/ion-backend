package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// NodeStatus mirrors the CHECK on network.nodes.status.
type NodeStatus string

const (
	NodeStatusActive          NodeStatus = "active"
	NodeStatusDegraded        NodeStatus = "degraded"
	NodeStatusDown            NodeStatus = "down"
	NodeStatusMaintenance     NodeStatus = "maintenance"
	NodeStatusFull            NodeStatus = "full"
	NodeStatusDecommissioned  NodeStatus = "decommissioned"
)

func (s NodeStatus) Valid() bool {
	switch s {
	case NodeStatusActive, NodeStatusDegraded, NodeStatusDown,
		NodeStatusMaintenance, NodeStatusFull, NodeStatusDecommissioned:
		return true
	}
	return false
}

// Node is a single topology element — POP, OLT, ODC, ODP, ONT, etc.
//
// Hierarchical via ParentID. The chain is enforced at the application layer
// (we don't have a CHECK constraint for "ODP's parent must be an OLT or ODC")
// because the rule depends on the configurable node_types table.
type Node struct {
	ID              uuid.UUID
	NodeTypeID      uuid.UUID
	Name            string
	Code            string
	ParentID        *uuid.UUID
	UpstreamPortID  *uuid.UUID
	BranchID        *uuid.UUID
	AssetID         *uuid.UUID
	Address         string
	GPSLat          *float64
	GPSLng          *float64
	CoverageRadiusM *int
	TotalPorts      *int
	Status          NodeStatus
	Metadata        map[string]any
	Active          bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewNode constructs a node with required-field validation. The caller is
// responsible for supplying valid foreign keys; the repo verifies referential
// integrity at insert time.
func NewNode(typeID uuid.UUID, name, code string) (*Node, error) {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if typeID == uuid.Nil {
		return nil, errors.Validation("node.type_required", "node_type_id is required")
	}
	if name == "" {
		return nil, errors.Validation("node.name_required", "name is required")
	}
	if code == "" {
		return nil, errors.Validation("node.code_required", "code is required")
	}

	now := time.Now().UTC()
	return &Node{
		ID:         uuid.New(),
		NodeTypeID: typeID,
		Name:       name,
		Code:       code,
		Status:     NodeStatusActive,
		Metadata:   map[string]any{},
		Active:     true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// SetGPS attaches a coordinate pair. Both lat and lng are required together.
func (n *Node) SetGPS(lat, lng float64) error {
	if lat < -90 || lat > 90 {
		return errors.Validation("node.lat_invalid", "lat must be in [-90,90]")
	}
	if lng < -180 || lng > 180 {
		return errors.Validation("node.lng_invalid", "lng must be in [-180,180]")
	}
	n.GPSLat = &lat
	n.GPSLng = &lng
	return nil
}
