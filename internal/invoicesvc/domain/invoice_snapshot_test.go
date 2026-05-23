package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// fakeInvoice — local InvoiceLike for testing BuildFromInvoice.
type fakeInvoice struct {
	id       uuid.UUID
	customer uuid.UUID
	status   string
	total    float64
}

func (f fakeInvoice) GetID() uuid.UUID         { return f.id }
func (f fakeInvoice) GetCustomerID() uuid.UUID { return f.customer }
func (f fakeInvoice) GetStatus() string        { return f.status }
func (f fakeInvoice) GetTotal() float64        { return f.total }

func TestBuildFromInvoice_HappyPath(t *testing.T) {
	inv := fakeInvoice{
		id:       uuid.New(),
		customer: uuid.New(),
		status:   "issued",
		total:    1234.56,
	}
	lines := []SnapshotLineItem{{
		Description: "Monthly recurring",
		ItemType:    "mrc",
		Quantity:    1,
		UnitPrice:   1234.56,
		Amount:      1234.56,
	}}
	schemaID := uuid.New()
	snap, err := BuildFromInvoice(inv, lines, &schemaID, SourceBilling)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if snap.InvoiceID != inv.id {
		t.Errorf("invoice id mismatch: got %s want %s", snap.InvoiceID, inv.id)
	}
	if snap.CustomerID == nil || *snap.CustomerID != inv.customer {
		t.Errorf("customer id mismatch")
	}
	if snap.TotalAmount != 1234.56 {
		t.Errorf("total mismatch: got %f", snap.TotalAmount)
	}
	if snap.SchemaSnapshotID == nil || *snap.SchemaSnapshotID != schemaID {
		t.Error("schema snapshot id not preserved")
	}
	if len(snap.LineItems) != 1 || snap.LineItems[0].Amount != 1234.56 {
		t.Error("line items not preserved")
	}
}

func TestBuildFromInvoice_RejectsNilInvoiceID(t *testing.T) {
	inv := fakeInvoice{} // zero values
	_, err := BuildFromInvoice(inv, nil, nil, SourceBilling)
	if err == nil {
		t.Fatal("expected error for zero invoice id")
	}
}

func TestBuildFromInvoice_DefaultsSource(t *testing.T) {
	inv := fakeInvoice{id: uuid.New(), customer: uuid.New(), status: "issued"}
	snap, err := BuildFromInvoice(inv, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.SourceModule != SourceBilling {
		t.Errorf("expected default source 'billing', got %s", snap.SourceModule)
	}
}

func TestSnapshotImmutability_LineItemsJSONStable(t *testing.T) {
	inv := fakeInvoice{id: uuid.New(), customer: uuid.New(), status: "issued"}
	lines := []SnapshotLineItem{
		{Description: "a", Amount: 1.0},
		{Description: "b", Amount: 2.0},
	}
	snap, err := BuildFromInvoice(inv, lines, nil, SourceBilling)
	if err != nil {
		t.Fatal(err)
	}
	// Two consecutive marshals should produce identical bytes — the
	// snapshot is meant to be a stable hash anchor.
	b1, err := snap.LineItemsJSON()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := snap.LineItemsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Errorf("line items JSON not stable: %s vs %s", b1, b2)
	}
	// Sanity: the JSON parses back to the same line shape.
	var parsed []SnapshotLineItem
	if err := json.Unmarshal(b1, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 lines, got %d", len(parsed))
	}
}

func TestSnapshotImmutability_EmptyLinesEmitsEmptyArray(t *testing.T) {
	inv := fakeInvoice{id: uuid.New()}
	snap, err := BuildFromInvoice(inv, nil, nil, SourceBilling)
	if err != nil {
		t.Fatal(err)
	}
	b, err := snap.LineItemsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Errorf("expected '[]' for empty lines, got %s", b)
	}
}
