// Package domain holds the network context's entities and value objects.
//
// Rules (same as identity/domain):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't get an invalid value.
//   - Errors are pkg/errors typed values.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// NodeType is a configurable category for topology nodes (PRD §5.2).
//
// Seeded at deployment (POP, OLT, ODC, ODP, ONT, MikroTik, …); admins can
// add new types via the Administration UI without a code deployment. The
// FE renders icons via the icon_* slot names — purely presentation; the
// backend doesn't interpret them.
type NodeType struct {
	ID          uuid.UUID
	TypeKey     string // slug used by code: "olt", "odp", …
	Label       string
	Description string
	IconOnline  string
	IconOffline string
	IconTrouble string
	SortOrder   int
	Active      bool
	// HasCoverageArea — whether this type carries a service-area
	// polygon. Drives the FE's polygon drawer + KMZ importer pickers
	// and the topology map's polygon fan-out. PRD §5.2 designs node
	// types as admin-configurable, so this lives next to the type
	// itself instead of being a hardcoded list in the FE.
	HasCoverageArea bool
	CreatedAt       time.Time
}

// NewNodeType constructs a node type with key normalization (lowercase,
// dashes/underscores allowed). Returns Validation on bad input.
func NewNodeType(typeKey, label string) (*NodeType, error) {
	typeKey = strings.ToLower(strings.TrimSpace(typeKey))
	label = strings.TrimSpace(label)
	if typeKey == "" {
		return nil, errors.Validation("node_type.key_required", "type_key is required")
	}
	if label == "" {
		return nil, errors.Validation("node_type.label_required", "label is required")
	}
	// Permit a-z, 0-9, underscore — keep keys grep-friendly.
	for _, r := range typeKey {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return nil, errors.Validation("node_type.key_invalid",
				"type_key may contain only a-z, 0-9, and underscore")
		}
	}
	return &NodeType{
		ID:        uuid.New(),
		TypeKey:   typeKey,
		Label:     label,
		Active:    true,
		CreatedAt: time.Now().UTC(),
	}, nil
}
