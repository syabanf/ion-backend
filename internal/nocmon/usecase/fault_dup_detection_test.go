// Wave 120 — fault dedup edge.
//
// Pins TC-FAM-* / TC-NSM-* "if the NOC opens a second fault with the
// same source_id within a 5-minute window, it must be flagged as a
// duplicate of the still-open original — not opened as a fresh
// incident". The dedup decision happens in the cron probe tick (and
// in MarkDuplicate at the domain layer); this test exercises the
// domain transition and asserts the back-ref to the original fault
// lives in the root_cause field for forensic linkage.

package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
)

func TestFaultService_DuplicateDetection_StampsBackRef(t *testing.T) {
	ctx := context.Background()
	probeID := uuid.New()

	faults := newMemFaultRepo()
	impacts := newMemImpactRepo()
	svc := NewFaultService(faults, impacts, nil)

	// First fault — opened against the probe.
	first, err := svc.OpenFault(ctx, port.OpenFaultInput{
		Kind:       domain.FaultKindProbeCritical,
		Severity:   domain.FaultSeverityHigh,
		SourceID:   &probeID,
		SourceKind: "probe",
	})
	if err != nil {
		t.Fatalf("OpenFault: %v", err)
	}
	if first.Status != domain.FaultStatusOpen {
		t.Fatalf("first.Status = %s, want open", first.Status)
	}

	// Second fault — same source_id, opened a few minutes later (within
	// the dedup window). The NOC operator marks it as duplicate of the
	// first.
	second, err := svc.OpenFault(ctx, port.OpenFaultInput{
		Kind:       domain.FaultKindProbeCritical,
		Severity:   domain.FaultSeverityHigh,
		SourceID:   &probeID,
		SourceKind: "probe",
	})
	if err != nil {
		t.Fatalf("OpenFault(second): %v", err)
	}

	// Now flag second as a duplicate of first.
	dedupAt := time.Now().UTC()
	if err := second.MarkDuplicate(first.ID, dedupAt); err != nil {
		t.Fatalf("MarkDuplicate: %v", err)
	}
	if err := faults.UpdateStatus(ctx, second); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Verify second is now duplicate + carries the back-ref string.
	got, _ := faults.FindByID(ctx, second.ID)
	if got.Status != domain.FaultStatusDuplicate {
		t.Errorf("second.Status = %s, want duplicate", got.Status)
	}
	if !strings.Contains(got.RootCause, first.ID.String()) {
		t.Errorf("root_cause = %q, expected back-ref to %s", got.RootCause, first.ID)
	}

	// First fault is still open and not affected.
	gotFirst, _ := faults.FindByID(ctx, first.ID)
	if gotFirst.Status != domain.FaultStatusOpen {
		t.Errorf("first.Status = %s, want still open", gotFirst.Status)
	}
}

func TestFaultEvent_MarkDuplicateFromNonOpen_Conflicts(t *testing.T) {
	probeID := uuid.New()
	f, err := domain.NewFaultEvent(domain.FaultKindProbeCritical, domain.FaultSeverityHigh, &probeID, "probe")
	if err != nil {
		t.Fatalf("NewFaultEvent: %v", err)
	}
	// Push to acknowledged via the SM.
	if err := f.Acknowledge(uuid.New(), time.Now()); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if err := f.MarkDuplicate(uuid.New(), time.Now()); err == nil {
		t.Fatalf("expected MarkDuplicate from acknowledged to be refused")
	}
}
