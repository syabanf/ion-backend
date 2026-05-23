package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewSubWarehouse_Defaults(t *testing.T) {
	parent := uuid.New()
	owner := uuid.New()
	sw, err := NewSubWarehouse(parent, owner, "Ciracas Mobile", "SW-CRC-01", "")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if sw.OwnerRole != SubWarehouseRoleTeamLead {
		t.Fatalf("expected default role team_lead, got %s", sw.OwnerRole)
	}
	if !sw.IsMobile {
		t.Fatal("expected default is_mobile=true")
	}
	if sw.CanPurchase {
		t.Fatal("expected CanPurchase=false (hardcoded) at intake")
	}
}

func TestNewSubWarehouse_Validates(t *testing.T) {
	if _, err := NewSubWarehouse(uuid.Nil, uuid.New(), "n", "c", ""); err == nil {
		t.Fatal("expected error on nil parent")
	}
	if _, err := NewSubWarehouse(uuid.New(), uuid.Nil, "n", "c", ""); err == nil {
		t.Fatal("expected error on nil owner")
	}
	if _, err := NewSubWarehouse(uuid.New(), uuid.New(), "", "c", ""); err == nil {
		t.Fatal("expected error on empty name")
	}
	if _, err := NewSubWarehouse(uuid.New(), uuid.New(), "n", "", ""); err == nil {
		t.Fatal("expected error on empty code")
	}
}

func TestSubWarehouse_IsOwnedBy(t *testing.T) {
	owner := uuid.New()
	sw, _ := NewSubWarehouse(uuid.New(), owner, "n", "c", "")
	if !sw.IsOwnedBy(owner) {
		t.Fatal("expected owner match")
	}
	if sw.IsOwnedBy(uuid.New()) {
		t.Fatal("expected stranger non-match")
	}
}

func TestSubWarehouse_CanReceive_MobileLimits(t *testing.T) {
	sw, _ := NewSubWarehouse(uuid.New(), uuid.New(), "n", "c", "")
	if !sw.CanReceive(ItemTypeSerialized) {
		t.Fatal("mobile sub-WH should accept Type 1")
	}
	if !sw.CanReceive(ItemTypeConsumable) {
		t.Fatal("mobile sub-WH should accept Type 3")
	}
	if sw.CanReceive(ItemTypeCable) {
		t.Fatal("mobile sub-WH should refuse Type 2")
	}
	if sw.CanReceive(ItemTypeNetworkInfra) {
		t.Fatal("mobile sub-WH should refuse Type 4")
	}
}

func TestSubWarehouse_CanReceive_StaticAcceptsAll(t *testing.T) {
	sw, _ := NewSubWarehouse(uuid.New(), uuid.New(), "n", "c", "")
	sw.IsMobile = false
	for _, tt := range []ItemType{ItemTypeSerialized, ItemTypeCable, ItemTypeConsumable, ItemTypeNetworkInfra} {
		if !sw.CanReceive(tt) {
			t.Fatalf("static sub-WH should accept %s", tt)
		}
	}
}
