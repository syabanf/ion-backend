package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewAddOnPurchase_DigitalGoesActive(t *testing.T) {
	p, err := NewAddOnPurchase(uuid.New(), "speed_boost_50", "Speed Boost 50M",
		AddOnCategoryDigital, 1, 50000)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != AddOnStatusActive {
		t.Errorf("expected active, got %s", p.Status)
	}
	if p.Total != 50000 {
		t.Errorf("expected total 50000, got %f", p.Total)
	}
}

func TestNewAddOnPurchase_PhysicalPendingInstall(t *testing.T) {
	p, err := NewAddOnPurchase(uuid.New(), "wifi_extender", "WiFi Mesh",
		AddOnCategoryPhysical, 1, 250000)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != AddOnStatusPendingInstall {
		t.Errorf("expected pending_install, got %s", p.Status)
	}
}

func TestNewAddOnPurchase_QuantityMultiplies(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "static_ip", "Static IP",
		AddOnCategoryDigital, 3, 10000)
	if p.Total != 30000 {
		t.Errorf("expected total 30000, got %f", p.Total)
	}
}

func TestNewAddOnPurchase_RequiresCustomerID(t *testing.T) {
	if _, err := NewAddOnPurchase(uuid.Nil, "x", "n", AddOnCategoryDigital, 1, 1); err == nil {
		t.Fatal("expected error for nil customer")
	}
}

func TestNewAddOnPurchase_RequiresSKU(t *testing.T) {
	if _, err := NewAddOnPurchase(uuid.New(), "", "n", AddOnCategoryDigital, 1, 1); err == nil {
		t.Fatal("expected error for empty sku")
	}
}

func TestNewAddOnPurchase_RejectsNegativePrice(t *testing.T) {
	if _, err := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, -1); err == nil {
		t.Fatal("expected error for negative price")
	}
}

func TestAddOnPurchase_Activate_FromPendingOnly(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "router", "Router",
		AddOnCategoryPhysical, 1, 500000)
	if err := p.Activate(); err != nil {
		t.Fatal(err)
	}
	if p.Status != AddOnStatusActive {
		t.Errorf("expected active, got %s", p.Status)
	}
	// Re-activate must conflict.
	if err := p.Activate(); err == nil {
		t.Error("expected conflict on re-activate")
	}
}

func TestAddOnPurchase_Cancel_RequiresReason(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	if err := p.Cancel(""); err == nil {
		t.Fatal("expected validation error for empty reason")
	}
}

func TestAddOnPurchase_Cancel_NoRefundMidCycle(t *testing.T) {
	// TC-AOB-005: mid-cycle cancel does NOT refund — there's no refund
	// state, just a status flip to 'cancelled'. The domain models
	// exactly that.
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	if err := p.Cancel("customer changed mind"); err != nil {
		t.Fatal(err)
	}
	if p.Status != AddOnStatusCancelled {
		t.Errorf("expected cancelled, got %s", p.Status)
	}
	if p.CancelledAt == nil {
		t.Error("cancelled_at should be set")
	}
	if p.IsBillable() {
		t.Error("cancelled add-ons must not be billable")
	}
}

func TestAddOnPurchase_Cancel_ConflictOnRecancel(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	_ = p.Cancel("r1")
	if err := p.Cancel("r2"); err == nil {
		t.Error("expected conflict on re-cancel")
	}
}

func TestAddOnPurchase_Expire_Idempotent(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	if err := p.Expire(); err != nil {
		t.Fatal(err)
	}
	if p.Status != AddOnStatusExpired {
		t.Errorf("expected expired, got %s", p.Status)
	}
	// idempotent
	if err := p.Expire(); err != nil {
		t.Error("re-expire should be idempotent")
	}
}

func TestAddOnPurchase_Expire_RejectsCancelled(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	_ = p.Cancel("r")
	if err := p.Expire(); err == nil {
		t.Error("expected conflict expiring cancelled add-on")
	}
}

func TestAddOnPurchase_IsBillable_OnlyActive(t *testing.T) {
	p, _ := NewAddOnPurchase(uuid.New(), "x", "n", AddOnCategoryDigital, 1, 100)
	if !p.IsBillable() {
		t.Error("active add-ons should be billable")
	}
	p2, _ := NewAddOnPurchase(uuid.New(), "y", "n2", AddOnCategoryPhysical, 1, 100)
	if p2.IsBillable() {
		t.Error("pending_install add-ons should NOT be billable")
	}
}
