// Package domain holds the invoicesvc bounded context's entities + invariants.
//
// invoicesvc is the read-heavy aggregation + audit-anchor service that
// will eventually extract into a standalone microservice. It intentionally
// does NOT import internal/billing or internal/enterprise — the
// cross-context links (invoice_id, customer_id) are plain UUIDs the
// SQL-only InvoiceReader resolves at query time.
package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// SourceModule indicates which upstream module produced the invoice this
// snapshot anchors. The unified read side may aggregate snapshots across
// both 'billing' (broadband cycles) and 'enterprise' (CPQ termin).
type SourceModule string

const (
	SourceBilling    SourceModule = "billing"
	SourceEnterprise SourceModule = "enterprise"
	SourceManual     SourceModule = "manual"
)

// SnapshotLineItem is the JSON-stable shape of a line captured at the
// moment an invoice was issued. We hand-roll this rather than reusing
// billing.LineItem because the upstream domain can evolve; snapshots
// must not.
type SnapshotLineItem struct {
	Description string  `json:"description"`
	ItemType    string  `json:"item_type"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	Amount      float64 `json:"amount"`
}

// InvoiceSnapshot — IMMUTABLE per-issuance capture.
//
// Invariant: once persisted, no field changes. UpdateSnapshot is not a
// thing; corrections create a NEW snapshot row with a fresh
// snapshotted_at. The UNIQUE (invoice_id, snapshotted_at) constraint at
// the DB level keeps duplicates out.
type InvoiceSnapshot struct {
	ID                uuid.UUID
	InvoiceID         uuid.UUID
	CustomerID        *uuid.UUID
	PlanID            *uuid.UUID
	SchemaSnapshotID  *uuid.UUID
	SnapshottedAt     time.Time
	TotalAmount       float64
	LineItems         []SnapshotLineItem
	StatusAtSnapshot  string
	SourceModule      SourceModule
}

// InvoiceLike is the narrow projection BuildFromInvoice needs from the
// upstream domain. Defined here as an interface so this package stays
// import-free of internal/billing.
type InvoiceLike interface {
	GetID() uuid.UUID
	GetCustomerID() uuid.UUID
	GetStatus() string
	GetTotal() float64
}

// BuildFromInvoice produces a fresh snapshot row given any invoice that
// satisfies InvoiceLike + the rendered line items. schemaSnapshotID is
// the platform schema version that was current at issue (nil-safe).
// The caller is responsible for persistence — this constructor only
// enforces the in-memory invariants.
func BuildFromInvoice(inv InvoiceLike, lines []SnapshotLineItem, schemaSnapshotID *uuid.UUID, source SourceModule) (*InvoiceSnapshot, error) {
	if inv == nil {
		return nil, errors.New("invoicesvc.snapshot.nil_invoice")
	}
	if inv.GetID() == uuid.Nil {
		return nil, errors.New("invoicesvc.snapshot.invoice_id_required")
	}
	if lines == nil {
		lines = []SnapshotLineItem{}
	}
	if source == "" {
		source = SourceBilling
	}
	cid := inv.GetCustomerID()
	var custPtr *uuid.UUID
	if cid != uuid.Nil {
		c := cid
		custPtr = &c
	}
	return &InvoiceSnapshot{
		ID:               uuid.New(),
		InvoiceID:        inv.GetID(),
		CustomerID:       custPtr,
		SchemaSnapshotID: schemaSnapshotID,
		SnapshottedAt:    time.Now().UTC(),
		TotalAmount:      inv.GetTotal(),
		LineItems:        lines,
		StatusAtSnapshot: inv.GetStatus(),
		SourceModule:     source,
	}, nil
}

// LineItemsJSON marshals LineItems to the JSON wire shape used in the
// jsonb column. Stable order means a re-run produces byte-identical
// payloads — important for hash-based audit chains.
func (s *InvoiceSnapshot) LineItemsJSON() ([]byte, error) {
	if s.LineItems == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.LineItems)
}
