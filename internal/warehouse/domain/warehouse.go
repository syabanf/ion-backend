// Package domain holds the warehouse context's entities and value objects.
// Same rules as identity / network: no framework imports, invariants
// enforced by constructors, errors via pkg/errors.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Warehouse is a physical storage location, belonging to a branch.
type Warehouse struct {
	ID        uuid.UUID
	Name      string
	Code      string
	BranchID  *uuid.UUID
	Address   string
	Notes     string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewWarehouse(name, code string) (*Warehouse, error) {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if name == "" {
		return nil, errors.Validation("warehouse.name_required", "name is required")
	}
	if code == "" {
		return nil, errors.Validation("warehouse.code_required", "code is required")
	}
	now := time.Now().UTC()
	return &Warehouse{
		ID:        uuid.New(),
		Name:      name,
		Code:      code,
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
