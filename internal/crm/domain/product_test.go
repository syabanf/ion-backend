package domain

import (
	"testing"

	"github.com/google/uuid"
)

// Wave 77 (TC-PRD-014/016/018/022): tests pinning the schema slot
// contract on Product. The 5 slots must be independently assignable
// (mix-and-match) and the kind→column mapping must be stable.

func TestNewProduct_NoSchemaSlotsByDefault(t *testing.T) {
	p, err := NewProduct("BB-10", "10 Mbps", 10, 100_000, 250_000)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	for _, kind := range SchemaSlots {
		if got := p.SchemaSlotID(kind); got != nil {
			t.Errorf("kind=%s: expected nil slot on fresh product, got %v", kind, got)
		}
	}
}

func TestSetSchemaSlot_AssignsCorrectField(t *testing.T) {
	p, _ := NewProduct("BB-50", "50 Mbps", 50, 300_000, 250_000)
	ids := map[string]uuid.UUID{
		"onboarding": uuid.New(),
		"billing":    uuid.New(),
		"service":    uuid.New(),
		"commission": uuid.New(),
		"suspension": uuid.New(),
	}
	// Wave 77 (TC-PRD-022): each kind sets its own column,
	// independent of the others.
	for kind, id := range ids {
		v := id
		if err := p.SetSchemaSlot(kind, &v); err != nil {
			t.Fatalf("SetSchemaSlot(%s): %v", kind, err)
		}
	}
	for kind, id := range ids {
		got := p.SchemaSlotID(kind)
		if got == nil || *got != id {
			t.Errorf("kind=%s: expected %v, got %v", kind, id, got)
		}
	}
}

func TestSetSchemaSlot_RejectsUnknownKind(t *testing.T) {
	p, _ := NewProduct("BB-50", "50 Mbps", 50, 300_000, 250_000)
	id := uuid.New()
	if err := p.SetSchemaSlot("bogus", &id); err == nil {
		t.Fatalf("expected error for unknown kind, got nil")
	}
}

func TestSetSchemaSlot_NilClears(t *testing.T) {
	p, _ := NewProduct("BB-50", "50 Mbps", 50, 300_000, 250_000)
	id := uuid.New()
	_ = p.SetSchemaSlot("onboarding", &id)
	if p.OnboardingSchemaID == nil {
		t.Fatalf("setup: slot should be set")
	}
	// Wave 77: assigning nil explicitly clears the slot. Tests the
	// PATCH "ClearOnboarding=true" path indirectly via domain.
	_ = p.SetSchemaSlot("onboarding", nil)
	if p.OnboardingSchemaID != nil {
		t.Errorf("expected slot to be cleared, got %v", *p.OnboardingSchemaID)
	}
}

func TestSchemaSlotID_UnknownKindReturnsNil(t *testing.T) {
	p, _ := NewProduct("BB-50", "50 Mbps", 50, 300_000, 250_000)
	if got := p.SchemaSlotID("bogus"); got != nil {
		t.Errorf("expected nil for unknown kind, got %v", got)
	}
}

func TestSchemaSlots_CoversAllFiveKinds(t *testing.T) {
	// Wave 77 (TC-PRD-022): the canonical list must have all 5 kinds
	// in stable order so resolver loops + tests + UI dropdowns agree.
	want := []string{"onboarding", "billing", "service", "commission", "suspension"}
	if len(SchemaSlots) != len(want) {
		t.Fatalf("SchemaSlots has %d entries, want %d", len(SchemaSlots), len(want))
	}
	for i, w := range want {
		if SchemaSlots[i] != w {
			t.Errorf("SchemaSlots[%d] = %q, want %q", i, SchemaSlots[i], w)
		}
	}
}
