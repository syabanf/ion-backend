// Wave 120 — sub-warehouse type restriction edge.
//
// Pins TC-SWH-* "mobile sub-warehouses (e.g. technician truck stock)
// must refuse inbound Type 2 (cable) and Type 4 (network infra) items
// per warehouse PRD §Sub-Warehouse; only Type 1 (serialized devices)
// and Type 3 (consumables) are accepted". The domain rule is in
// sub_warehouse.go::CanReceive; this test extends the existing
// domain-level table test by exercising the predicate explicitly
// for the cable / network-infra cases.

package usecase

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
)

func TestSubWarehouse_RefusesCable(t *testing.T) {
	sw, err := domain.NewSubWarehouse(
		uuid.New(),
		uuid.New(),
		"Tech Truck #1",
		"TT-1",
		domain.SubWarehouseRoleTechnician,
	)
	if err != nil {
		t.Fatalf("NewSubWarehouse: %v", err)
	}
	if sw.CanReceive(domain.ItemTypeCable) {
		t.Errorf("mobile sub-warehouse must refuse Type 2 (cable) inbound")
	}
}

func TestSubWarehouse_RefusesNetworkInfra(t *testing.T) {
	sw, _ := domain.NewSubWarehouse(
		uuid.New(),
		uuid.New(),
		"NOC Stash",
		"NS-1",
		domain.SubWarehouseRoleNOCSupervisor,
	)
	if sw.CanReceive(domain.ItemTypeNetworkInfra) {
		t.Errorf("mobile sub-warehouse must refuse Type 4 (network infra) inbound")
	}
}

func TestSubWarehouse_AcceptsSerializedAndConsumables(t *testing.T) {
	sw, _ := domain.NewSubWarehouse(
		uuid.New(),
		uuid.New(),
		"Field Tech",
		"FT-1",
		domain.SubWarehouseRoleTeamLead,
	)
	if !sw.CanReceive(domain.ItemTypeSerialized) {
		t.Errorf("mobile sub-warehouse must accept Type 1 (serialized) inbound")
	}
	if !sw.CanReceive(domain.ItemTypeConsumable) {
		t.Errorf("mobile sub-warehouse must accept Type 3 (consumable) inbound")
	}
}
