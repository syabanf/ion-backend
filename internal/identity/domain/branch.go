package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Branch is a node in the 3-level geographic hierarchy:
// Regional → Area → Sub Area. Sub Area is mandatory for every Area.
// See PRD Core Reference: Branch & Territory Hierarchy.
type Branch struct {
	ID         uuid.UUID
	Name       string
	Code       string
	Level      BranchLevel
	ParentID   *uuid.UUID
	Active     bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// GeoPolygon and per-branch config (odp_strategy, cable_distance,
	// wo_auto_assign) are stored as jsonb in the repo layer; not modeled
	// in the domain entity until a use case needs to enforce rules over them.
}

// NewBranch constructs a branch with hierarchy rules enforced:
//   - regional: parent must be nil
//   - area:     parent must be regional
//   - sub_area: parent must be area
//
// The repo verifies the parent's level matches before insert.
func NewBranch(name, code string, level BranchLevel, parentID *uuid.UUID) (*Branch, error) {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if name == "" {
		return nil, errors.Validation("branch.name_required", "branch name is required")
	}
	if code == "" {
		return nil, errors.Validation("branch.code_required", "branch code is required")
	}
	if !level.Valid() {
		return nil, errors.Validation("branch.level_invalid", "invalid branch level")
	}
	if level == BranchLevelRegional && parentID != nil {
		return nil, errors.Validation("branch.regional_no_parent", "regional branches have no parent")
	}
	if level != BranchLevelRegional && parentID == nil {
		return nil, errors.Validation("branch.parent_required", "parent is required for non-regional branches")
	}

	now := time.Now().UTC()
	return &Branch{
		ID:        uuid.New(),
		Name:      name,
		Code:      code,
		Level:     level,
		ParentID:  parentID,
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
