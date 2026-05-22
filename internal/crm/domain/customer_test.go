package domain

import (
	"testing"

	"github.com/google/uuid"
)

// Wave 78 (TC-SCH-011/015/023/026, TC-PRD-025): Customer carries a
// schema_version_id per kind so dunning + commission + suspension
// reads are pinned to the version active at conversion time.

func TestNewBroadbandCustomer_NoLocksByDefault(t *testing.T) {
	c, err := NewBroadbandCustomer("Budi", "0811", "Jakarta")
	if err != nil {
		t.Fatalf("NewBroadbandCustomer: %v", err)
	}
	for _, kind := range SchemaSlots {
		if got := c.LockedSchemaVersionID(kind); got != nil {
			t.Errorf("kind=%s: expected nil lock on fresh customer, got %v", kind, got)
		}
	}
}

func TestSetLockedSchemaVersion_AssignsCorrectField(t *testing.T) {
	c, _ := NewBroadbandCustomer("Budi", "0811", "Jakarta")
	ids := map[string]uuid.UUID{
		"onboarding": uuid.New(),
		"billing":    uuid.New(),
		"service":    uuid.New(),
		"commission": uuid.New(),
		"suspension": uuid.New(),
	}
	for kind, id := range ids {
		v := id
		if err := c.SetLockedSchemaVersion(kind, &v); err != nil {
			t.Fatalf("SetLockedSchemaVersion(%s): %v", kind, err)
		}
	}
	// Wave 78 (TC-SCH-026): each kind locks independently.
	for kind, id := range ids {
		got := c.LockedSchemaVersionID(kind)
		if got == nil || *got != id {
			t.Errorf("kind=%s: expected %v, got %v", kind, id, got)
		}
	}
}

func TestSetLockedSchemaVersion_RejectsUnknownKind(t *testing.T) {
	c, _ := NewBroadbandCustomer("Budi", "0811", "Jakarta")
	id := uuid.New()
	if err := c.SetLockedSchemaVersion("bogus", &id); err == nil {
		t.Fatalf("expected error for unknown kind, got nil")
	}
}

func TestLockedSchemaVersionID_UnknownKindReturnsNil(t *testing.T) {
	c, _ := NewBroadbandCustomer("Budi", "0811", "Jakarta")
	if got := c.LockedSchemaVersionID("bogus"); got != nil {
		t.Errorf("expected nil for unknown kind, got %v", got)
	}
}

func TestSetLockedSchemaVersion_NilClears(t *testing.T) {
	c, _ := NewBroadbandCustomer("Budi", "0811", "Jakarta")
	id := uuid.New()
	_ = c.SetLockedSchemaVersion("billing", &id)
	if c.LockedBillingSchemaVersionID == nil {
		t.Fatalf("setup: lock should be set")
	}
	// Bulk-migration tool needs the unlock path: setting nil clears
	// the lock and the resolver falls back to product slot / default.
	_ = c.SetLockedSchemaVersion("billing", nil)
	if c.LockedBillingSchemaVersionID != nil {
		t.Errorf("expected lock cleared, got %v", *c.LockedBillingSchemaVersionID)
	}
}
