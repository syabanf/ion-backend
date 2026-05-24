// Wave 128D — TicketImporterService unit tests.
//
// Coverage:
//   - 5-row mixed-status, mixed-category seed → asserts every legacy
//     status survives mapping AND every category lands in the right
//     workflow-oriented ticket_type bucket.
//   - Re-run idempotence: second RunOnce sees zero pending rows and
//     reports AlreadyMigrated == 5 with zero new inserts.
//   - Mapping helpers exercised directly so the workflow taxonomy
//     decisions are visible in test output.
package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// stub legacy reader + canonical writer
// =====================================================================

// stubLegacyReader is an in-memory LegacyTicketReader. The migrated
// set is tracked by the writer; on each ListUnmigrated we filter by it
// so re-runs behave like the real anti-join.
type stubLegacyReader struct {
	mu     sync.Mutex
	rows   []LegacyTicketRow
	writer *stubCanonicalWriter
}

func (r *stubLegacyReader) CountAll(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows), nil
}

func (r *stubLegacyReader) ListUnmigrated(_ context.Context, limit int) ([]LegacyTicketRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []LegacyTicketRow{}
	for _, row := range r.rows {
		if !r.writer.hasLegacy(row.ID) {
			out = append(out, row)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// stubCanonicalWriter is an in-memory CanonicalTicketWriter. The
// inserted map is keyed by legacy_id so we get the same uniqueness
// guarantee as the real partial index.
type stubCanonicalWriter struct {
	mu       sync.Mutex
	byLegacy map[uuid.UUID]*domain.Ticket
}

func newStubWriter() *stubCanonicalWriter {
	return &stubCanonicalWriter{byLegacy: map[uuid.UUID]*domain.Ticket{}}
}

func (w *stubCanonicalWriter) Insert(_ context.Context, t *domain.Ticket) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	legacyID := LegacyID(t)
	if legacyID == uuid.Nil {
		return false, nil
	}
	if _, ok := w.byLegacy[legacyID]; ok {
		return false, nil
	}
	w.byLegacy[legacyID] = t
	return true, nil
}

func (w *stubCanonicalWriter) hasLegacy(id uuid.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.byLegacy[id]
	return ok
}

func (w *stubCanonicalWriter) byLegacyID(id uuid.UUID) *domain.Ticket {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.byLegacy[id]
}

// =====================================================================
// helpers
// =====================================================================

func seedLegacyRow(category, status, priority string) LegacyTicketRow {
	id := uuid.New()
	customer := uuid.New()
	opener := uuid.New()
	return LegacyTicketRow{
		ID:           id,
		TicketNumber: "LEG-" + id.String()[:8],
		CustomerID:   customer,
		Category:     category,
		Priority:     priority,
		Status:       status,
		Summary:      "legacy summary " + category,
		Description:  "legacy desc",
		OpenedBy:     &opener,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		UpdatedAt:    time.Now().UTC().Add(-24 * time.Hour),
	}
}

// =====================================================================
// Tests
// =====================================================================

// TestTicketImporter_RunOnce_MapsAllVariants seeds 5 rows that exercise
// every legacy-status value plus a category from each canonical
// ticket_type bucket the legacy enum can hit. Asserts the per-row
// mapping AND the summary counters.
func TestTicketImporter_RunOnce_MapsAllVariants(t *testing.T) {
	ctx := context.Background()

	writer := newStubWriter()
	reader := &stubLegacyReader{
		writer: writer,
		rows: []LegacyTicketRow{
			seedLegacyRow("no_internet", "open", "high"),
			seedLegacyRow("slow_speed", "in_progress", "medium"),
			seedLegacyRow("equipment_damage", "pending_customer", "high"),
			seedLegacyRow("billing_dispute", "resolved", "low"),
			seedLegacyRow("other", "closed", "medium"),
		},
	}

	svc := NewTicketImporterService(reader, writer, nil)
	summary, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if summary.TotalLegacy != 5 {
		t.Errorf("TotalLegacy = %d want 5", summary.TotalLegacy)
	}
	if summary.Imported != 5 {
		t.Errorf("Imported = %d want 5", summary.Imported)
	}
	if summary.AlreadyMigrated != 0 {
		t.Errorf("AlreadyMigrated = %d want 0 on first run", summary.AlreadyMigrated)
	}
	if summary.Errors != 0 {
		t.Errorf("Errors = %d want 0 (samples=%v)", summary.Errors, summary.ErrorSamples)
	}

	// Per-row assertions: ticket_type + status + priority round-trip.
	want := []struct {
		legacyCategory string
		legacyStatus   string
		legacyPriority string
		wantType       domain.TicketType
		wantStatus     domain.TicketStatus
		wantPriority   domain.Priority
	}{
		{"no_internet", "open", "high", domain.TicketTypeTechnical, domain.TicketStatusOpen, domain.PriorityHigh},
		{"slow_speed", "in_progress", "medium", domain.TicketTypeTechnical, domain.TicketStatusInProgress, domain.PriorityNormal},
		{"equipment_damage", "pending_customer", "high", domain.TicketTypeTechnical, domain.TicketStatusPendingCustomer, domain.PriorityHigh},
		{"billing_dispute", "resolved", "low", domain.TicketTypeBilling, domain.TicketStatusResolved, domain.PriorityLow},
		{"other", "closed", "medium", domain.TicketTypeTechnical, domain.TicketStatusClosed, domain.PriorityNormal},
	}

	for i, row := range reader.rows {
		got := writer.byLegacyID(row.ID)
		if got == nil {
			t.Errorf("row %d (%s): not imported", i, row.Category)
			continue
		}
		if got.TicketType != want[i].wantType {
			t.Errorf("row %d (%s): ticket_type = %q want %q",
				i, row.Category, got.TicketType, want[i].wantType)
		}
		if got.Status != want[i].wantStatus {
			t.Errorf("row %d (%s/%s): status = %q want %q",
				i, row.Category, row.Status, got.Status, want[i].wantStatus)
		}
		if got.Priority != want[i].wantPriority {
			t.Errorf("row %d (%s/%s): priority = %q want %q",
				i, row.Category, row.Priority, got.Priority, want[i].wantPriority)
		}
		if got.CustomerID != row.CustomerID {
			t.Errorf("row %d: customer_id mismatch", i)
		}
		if LegacyID(got) != row.ID {
			t.Errorf("row %d: legacy_id not preserved in source_metadata", i)
		}
	}
}

// TestTicketImporter_RunOnce_Idempotent re-runs the importer on the
// same seed; the second pass MUST find 5 already-migrated rows + 0
// new inserts.
func TestTicketImporter_RunOnce_Idempotent(t *testing.T) {
	ctx := context.Background()

	writer := newStubWriter()
	reader := &stubLegacyReader{
		writer: writer,
		rows: []LegacyTicketRow{
			seedLegacyRow("no_internet", "open", "high"),
			seedLegacyRow("slow_speed", "in_progress", "medium"),
			seedLegacyRow("billing_dispute", "resolved", "low"),
			seedLegacyRow("equipment_damage", "pending_customer", "high"),
			seedLegacyRow("other", "closed", "medium"),
		},
	}

	svc := NewTicketImporterService(reader, writer, nil)

	first, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce first: %v", err)
	}
	if first.Imported != 5 {
		t.Fatalf("first Imported = %d want 5", first.Imported)
	}

	second, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce second: %v", err)
	}
	if second.Imported != 0 {
		t.Errorf("second Imported = %d want 0", second.Imported)
	}
	if second.AlreadyMigrated != 5 {
		t.Errorf("second AlreadyMigrated = %d want 5", second.AlreadyMigrated)
	}
	if second.Errors != 0 {
		t.Errorf("second Errors = %d want 0", second.Errors)
	}
}

// TestTicketImporter_RunOnce_UnknownCategoryDefaultsTechnical asserts
// the fall-through path the prompt spelled out: an unmapped category
// must NOT fail the import — it should land as `technical` with a
// warning logged. The test asserts only the type, not the log line
// (slog is too verbose to assert on textually).
func TestTicketImporter_RunOnce_UnknownCategoryDefaultsTechnical(t *testing.T) {
	ctx := context.Background()

	row := seedLegacyRow("this_is_not_a_known_value", "open", "low")
	writer := newStubWriter()
	reader := &stubLegacyReader{writer: writer, rows: []LegacyTicketRow{row}}

	svc := NewTicketImporterService(reader, writer, nil)
	summary, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Imported != 1 {
		t.Fatalf("Imported = %d want 1", summary.Imported)
	}
	got := writer.byLegacyID(row.ID)
	if got.TicketType != domain.TicketTypeTechnical {
		t.Errorf("unknown category → ticket_type = %q want technical", got.TicketType)
	}
}

// TestMapCategoryToTicketType_AllPromptCategories asserts every
// category the prompt explicitly enumerated maps to the documented
// bucket. Catches regressions if the mapping switch is reshuffled.
func TestMapCategoryToTicketType_AllPromptCategories(t *testing.T) {
	cases := []struct {
		category string
		want     domain.TicketType
	}{
		// technical bucket
		{"no_internet", domain.TicketTypeTechnical},
		{"slow_speed", domain.TicketTypeTechnical},
		{"intermittent", domain.TicketTypeTechnical},
		{"signal_quality", domain.TicketTypeTechnical},
		{"hardware_failure", domain.TicketTypeTechnical},
		{"frequent_drops", domain.TicketTypeTechnical},
		{"equipment_damage", domain.TicketTypeTechnical},
		// billing bucket
		{"invoice_dispute", domain.TicketTypeBilling},
		{"payment_issue", domain.TicketTypeBilling},
		{"refund", domain.TicketTypeBilling},
		{"billing_dispute", domain.TicketTypeBilling},
		// complaint bucket
		{"service_quality", domain.TicketTypeComplaint},
		{"complaint", domain.TicketTypeComplaint},
		{"escalation", domain.TicketTypeComplaint},
		// service_request bucket
		{"cancellation", domain.TicketTypeServiceRequest},
		{"plan_change", domain.TicketTypeServiceRequest},
		{"address_change", domain.TicketTypeServiceRequest},
		// information bucket
		{"status_inquiry", domain.TicketTypeInformation},
		{"info_request", domain.TicketTypeInformation},
		// default
		{"other", domain.TicketTypeTechnical},
		{"totally_unknown_value", domain.TicketTypeTechnical},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			got := mapCategoryToTicketType(tc.category, nil)
			if got != tc.want {
				t.Errorf("category %q → %q want %q", tc.category, got, tc.want)
			}
		})
	}
}

// TestMapLegacyStatus exercises all 5 legacy status values plus the
// unknown-fallback path.
func TestMapLegacyStatus(t *testing.T) {
	cases := map[string]domain.TicketStatus{
		"open":             domain.TicketStatusOpen,
		"in_progress":      domain.TicketStatusInProgress,
		"pending_customer": domain.TicketStatusPendingCustomer,
		"resolved":         domain.TicketStatusResolved,
		"closed":           domain.TicketStatusClosed,
		"garbage":          domain.TicketStatusOpen, // fallback
	}
	for in, want := range cases {
		if got := mapLegacyStatus(in); got != want {
			t.Errorf("mapLegacyStatus(%q) = %q want %q", in, got, want)
		}
	}
}
