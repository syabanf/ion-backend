// Wave 120 — snapshot immutability edge.
//
// Pins TC-ISV-* / TC-IMC-* "an invoice_snapshot row is immutable after
// creation — there's no Update method on the repo, and the
// SnapshottedAt timestamp is set once at build time and never mutated".
//
// Real immutability is enforced at the DB level via a trigger that
// refuses UPDATE on invoice_snapshots. The repo port doesn't even
// expose an Update method. This test pins the no-update contract.
//
// A second test exercises the "create two snapshots for the same
// invoice" path — this is allowed (each snapshot has a fresh id +
// snapshotted_at) so the contract is "each row is immutable" not
// "only one snapshot per invoice".

package usecase

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
)

func TestInvoiceSnapshotRepository_Port_HasNoUpdateMethod(t *testing.T) {
	// The repository port deliberately does NOT expose an Update or
	// Delete method. This test pins that interface surface by
	// reflecting on the type. If a future change adds an Update method,
	// this test will fail — forcing the author to acknowledge they're
	// breaking the immutability invariant.
	typ := reflect.TypeOf((*port.InvoiceSnapshotRepository)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		switch name {
		case "Update", "Delete", "DeleteByID", "MarkDeleted":
			t.Errorf("InvoiceSnapshotRepository must not expose %q — snapshots are immutable", name)
		}
	}
	// Quick sanity check: the read-side methods we DO expect exist.
	for _, expected := range []string{"Create", "FindByID", "ListByInvoice"} {
		if _, ok := typ.MethodByName(expected); !ok {
			t.Errorf("InvoiceSnapshotRepository missing expected method %q", expected)
		}
	}
}

func TestSnapshotService_TwoCreates_ProduceDistinctRows(t *testing.T) {
	// Multiple snapshots for the same invoice are PERMITTED — each
	// represents a fresh point-in-time read. The invariant is
	// per-row, not per-invoice.
	ctx := context.Background()
	reader := newStubReader()
	repo := &stubSnapshotRepo{}
	invID := uuid.New()
	custID := uuid.New()
	reader.byID[invID] = port.InvoiceProjection{
		ID:           invID,
		CustomerID:   custID,
		Status:       "issued",
		Total:        500.00,
		SourceModule: "billing",
	}
	svc := NewSnapshotService(repo, reader)

	snap1, err := svc.CreateSnapshot(ctx, invID, nil, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot 1: %v", err)
	}
	snap2, err := svc.CreateSnapshot(ctx, invID, nil, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot 2: %v", err)
	}
	if snap1.ID == snap2.ID {
		t.Errorf("two snapshots share ID %s — must be distinct", snap1.ID)
	}
	if snap1.InvoiceID != snap2.InvoiceID {
		t.Errorf("invoice id differs across snapshots: %s vs %s", snap1.InvoiceID, snap2.InvoiceID)
	}
	// Two rows must be in the repo now.
	rows, _ := repo.ListByInvoice(ctx, invID)
	if len(rows) != 2 {
		t.Errorf("repo rows = %d, want 2", len(rows))
	}
}

func TestSnapshot_SnapshottedAtIsStampedAtBuildTime(t *testing.T) {
	// BuildFromInvoice stamps SnapshottedAt once. Asserting the
	// field is non-zero on a fresh snapshot pins that the stamp
	// actually happens (and isn't deferred to the repo layer).
	inv := stubInvoiceLike{
		id:       uuid.New(),
		customer: uuid.New(),
		status:   "issued",
		total:    1000.00,
	}
	snap, err := domain.BuildFromInvoice(inv, nil, nil, domain.SourceBilling)
	if err != nil {
		t.Fatalf("BuildFromInvoice: %v", err)
	}
	if snap.SnapshottedAt.IsZero() {
		t.Errorf("SnapshottedAt is zero — must be stamped at build time")
	}
}

// stubInvoiceLike mirrors invoiceProjectionAdapter for tests in this
// package that need a domain.InvoiceLike directly.
type stubInvoiceLike struct {
	id       uuid.UUID
	customer uuid.UUID
	status   string
	total    float64
}

func (s stubInvoiceLike) GetID() uuid.UUID         { return s.id }
func (s stubInvoiceLike) GetCustomerID() uuid.UUID { return s.customer }
func (s stubInvoiceLike) GetStatus() string        { return s.status }
func (s stubInvoiceLike) GetTotal() float64        { return s.total }
