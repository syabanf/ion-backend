// Wave 117 — Sub-warehouse (NOC + TL stockholder model).
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SubWarehouseRole — who's holding the stock at this sub-warehouse.
// Drives the liability + threshold-alert routing (PRD §Sub-Warehouse).
type SubWarehouseRole string

const (
	SubWarehouseRoleTeamLead       SubWarehouseRole = "team_lead"
	SubWarehouseRoleNOCSupervisor  SubWarehouseRole = "noc_supervisor"
	SubWarehouseRoleTechnician     SubWarehouseRole = "technician"
	SubWarehouseRoleWarehouseStaff SubWarehouseRole = "warehouse_staff"
)

func (r SubWarehouseRole) Valid() bool {
	switch r {
	case SubWarehouseRoleTeamLead, SubWarehouseRoleNOCSupervisor,
		SubWarehouseRoleTechnician, SubWarehouseRoleWarehouseStaff:
		return true
	}
	return false
}

// SubWarehouse is a child of a parent Warehouse, owned by a TL / NOC
// supervisor who carries the liability for variance.
type SubWarehouse struct {
	ID                uuid.UUID
	ParentWarehouseID uuid.UUID
	Name              string
	Code              string
	OwnerUserID       uuid.UUID
	OwnerRole         SubWarehouseRole
	IsMobile          bool
	VehicleID         string
	CanPurchase       bool
	Active            bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewSubWarehouse — enforces the per-PRD invariant that sub-warehouses
// cannot self-purchase (CanPurchase = always false at creation; only the
// admin endpoint flips it, and the app-layer refuses to flip a Sub-WH
// per TC-MPE-003).
func NewSubWarehouse(parentID, ownerID uuid.UUID, name, code string, role SubWarehouseRole) (*SubWarehouse, error) {
	if parentID == uuid.Nil {
		return nil, errors.Validation("sub_warehouse.parent_required", "parent_warehouse_id is required")
	}
	if ownerID == uuid.Nil {
		return nil, errors.Validation("sub_warehouse.owner_required", "owner_user_id is required")
	}
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if name == "" {
		return nil, errors.Validation("sub_warehouse.name_required", "name is required")
	}
	if code == "" {
		return nil, errors.Validation("sub_warehouse.code_required", "code is required")
	}
	if role == "" {
		role = SubWarehouseRoleTeamLead
	}
	if !role.Valid() {
		return nil, errors.Validation("sub_warehouse.role_invalid", "owner_role invalid")
	}
	now := time.Now().UTC()
	return &SubWarehouse{
		ID:                uuid.New(),
		ParentWarehouseID: parentID,
		Name:              name,
		Code:              code,
		OwnerUserID:       ownerID,
		OwnerRole:         role,
		IsMobile:          true,
		CanPurchase:       false, // hardcoded at intake; see TC-MPE-003
		Active:            true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// IsOwnedBy answers the "is this technician picking up from their own
// vehicle's sub-stock?" question on the mobile dispatch path.
func (s *SubWarehouse) IsOwnedBy(userID uuid.UUID) bool {
	return s.OwnerUserID == userID
}

// CanReceive — per PRD §Sub-Warehouse, mobile sub-warehouses are limited
// to Type 1 (serialized) + Type 3 (consumable). Cable drums + infra (Type
// 2 + Type 4) stay at the parent. Static sub-warehouses (e.g. permanent
// NOC stockroom) can hold any type.
func (s *SubWarehouse) CanReceive(itemType ItemType) bool {
	if !s.IsMobile {
		return true
	}
	switch itemType {
	case ItemTypeSerialized, ItemTypeConsumable:
		return true
	}
	return false
}
