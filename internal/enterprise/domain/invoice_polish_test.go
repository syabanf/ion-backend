package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Wave 107 — invoice polish field tests (PPh23 + ReminderSentAt).
//
// These exercise the small accessor methods added in invoice.go so a
// regression on the JSON/SQL fan-out gets caught at the domain boundary.

func newTestInvoice(t *testing.T) *Invoice {
	t.Helper()
	inv, err := NewInvoice(
		uuid.New(), uuid.New(), uuid.New(),
		"INV-TEST",
		1000.0, 900.0, 11.0,
		"IDR",
		time.Now().Add(7*24*time.Hour),
	)
	if err != nil {
		t.Fatalf("NewInvoice: %v", err)
	}
	return inv
}

// TestInvoice_SetPPh23_Applicable — applies the flag + amount.
func TestInvoice_SetPPh23_Applicable(t *testing.T) {
	inv := newTestInvoice(t)
	inv.SetPPh23(true, 20.0)
	if !inv.IsPPh23Applicable {
		t.Error("IsPPh23Applicable should be true")
	}
	if inv.PPh23WithheldAmount != 20.0 {
		t.Errorf("withheld = %v, want 20", inv.PPh23WithheldAmount)
	}
}

// TestInvoice_SetPPh23_Disable — clearing the flag zeroes the amount.
func TestInvoice_SetPPh23_Disable(t *testing.T) {
	inv := newTestInvoice(t)
	inv.SetPPh23(true, 20.0)
	inv.SetPPh23(false, 0)
	if inv.IsPPh23Applicable {
		t.Error("IsPPh23Applicable should be false")
	}
	if inv.PPh23WithheldAmount != 0 {
		t.Errorf("withheld = %v, want 0", inv.PPh23WithheldAmount)
	}
}

// TestInvoice_SetPPh23_NegativeClamped — negative amount clamps to 0.
func TestInvoice_SetPPh23_NegativeClamped(t *testing.T) {
	inv := newTestInvoice(t)
	inv.SetPPh23(true, -50.0)
	if inv.PPh23WithheldAmount != 0 {
		t.Errorf("clamped withheld = %v, want 0", inv.PPh23WithheldAmount)
	}
}

// TestInvoice_NetReceived_NoPPh23 — equals TotalAmount when flag off.
func TestInvoice_NetReceived_NoPPh23(t *testing.T) {
	inv := newTestInvoice(t)
	if inv.NetReceived() != inv.TotalAmount {
		t.Errorf("NetReceived = %v, want %v", inv.NetReceived(), inv.TotalAmount)
	}
}

// TestInvoice_NetReceived_WithPPh23 — subtracts the withheld amount.
func TestInvoice_NetReceived_WithPPh23(t *testing.T) {
	inv := newTestInvoice(t)
	inv.SetPPh23(true, 20.0)
	if inv.NetReceived() != inv.TotalAmount-20.0 {
		t.Errorf("NetReceived = %v, want %v", inv.NetReceived(), inv.TotalAmount-20.0)
	}
}

// TestInvoice_MarkReminderSent — stamps timestamp + updated_at.
func TestInvoice_MarkReminderSent(t *testing.T) {
	inv := newTestInvoice(t)
	before := inv.UpdatedAt
	time.Sleep(1 * time.Millisecond)
	inv.MarkReminderSent()
	if inv.ReminderSentAt == nil {
		t.Fatal("ReminderSentAt nil after MarkReminderSent")
	}
	if !inv.UpdatedAt.After(before) {
		t.Error("UpdatedAt not advanced")
	}
}
