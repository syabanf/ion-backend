// Wave 128D — CS ticket importer E2E.
//
// Exercises TicketImporterService against live Postgres (ion_p1c_smoke):
//
//   - Seeds a field.tickets row via raw SQL
//   - Runs the importer
//   - Asserts a cs.tickets row appeared with the correct ticket_type,
//     status, priority, and legacy_id linkage
//   - Re-runs the importer; asserts AlreadyMigrated == 1 + no duplicates
//
// All assertions skip cleanly when DATABASE_URL is unset, field.tickets
// is missing, or cs.tickets.legacy_id is missing (migration 0087 not
// applied to this DB).
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	cspg "github.com/ion-core/backend/internal/cs/adapter/postgres"
	csuc "github.com/ion-core/backend/internal/cs/usecase"
)

// TestCSImporter_BackfillsLegacyRow seeds one field.tickets row, runs
// the importer, then asserts the canonical row materialised. A second
// pass asserts idempotence.
func TestCSImporter_BackfillsLegacyRow(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.tickets")
	w121cSkipIfMissingTable(t, pool, "cs.tickets")

	// Probe for migration 0087 — skip cleanly if legacy_id isn't there
	// yet. Mirror of the Wave 127 pattern: light up automatically once
	// the migration lands.
	if !w128dHasLegacyIDColumn(t) {
		t.Skip("Wave 128D: cs.tickets.legacy_id column missing (migration 0087 not applied?)")
	}

	ctx := context.Background()

	legacyID := uuid.New()
	customerID := uuid.New()
	openedBy := uuid.New()
	ticketNumber := "TKT-LEG-" + uuid.New().String()[:8]
	createdAt := time.Now().UTC().Add(-72 * time.Hour)

	// Seed one legacy row directly via SQL. Cleanup deletes BOTH the
	// legacy row AND any cs.tickets row keyed by the same legacy_id so
	// re-runs of this test start clean.
	w121cExec(t, pool, `
		INSERT INTO field.tickets
			(id, ticket_number, customer_id, category, priority, status,
			 summary, description, opened_by, created_at, updated_at)
		VALUES
			($1, $2, $3, 'no_internet', 'high', 'open',
			 'W128D importer e2e', 'wave-128d test', $4, $5, $5)
	`, legacyID, ticketNumber, customerID, openedBy, createdAt)

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM cs.tickets WHERE legacy_id = $1`, legacyID)
		_, _ = pool.Exec(cctx, `DELETE FROM field.tickets WHERE id = $1`, legacyID)
	})

	reader := cspg.NewLegacyTicketReader(pool)
	writer := cspg.NewCanonicalImporterWriter(pool)
	svc := csuc.NewTicketImporterService(reader, writer, nil)

	first, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce first pass: %v", err)
	}
	if first.Errors > 0 {
		t.Fatalf("first pass had errors: %v", first.ErrorSamples)
	}

	// Assert the canonical row materialised with the right mapping.
	var (
		gotType, gotStatus, gotPriority string
		gotCustomer                     uuid.UUID
		gotTicketNo                     string
	)
	err = pool.QueryRow(ctx, `
		SELECT ticket_type, status, priority, customer_id, ticket_no
		  FROM cs.tickets WHERE legacy_id = $1
	`, legacyID).Scan(&gotType, &gotStatus, &gotPriority, &gotCustomer, &gotTicketNo)
	if err != nil {
		t.Fatalf("canonical row not materialised: %v", err)
	}
	if gotType != "technical" {
		t.Errorf("ticket_type = %q want technical (no_internet → technical)", gotType)
	}
	if gotStatus != "open" {
		t.Errorf("status = %q want open", gotStatus)
	}
	if gotPriority != "high" {
		t.Errorf("priority = %q want high", gotPriority)
	}
	if gotCustomer != customerID {
		t.Errorf("customer_id mismatch: got %s want %s", gotCustomer, customerID)
	}
	if gotTicketNo == "" {
		t.Errorf("ticket_no empty")
	}

	// Second pass — must be idempotent.
	second, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce second pass: %v", err)
	}
	if second.Errors > 0 {
		t.Fatalf("second pass had errors: %v", second.ErrorSamples)
	}

	// Sanity: only one cs.tickets row exists for this legacy_id.
	var dupCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM cs.tickets WHERE legacy_id = $1`, legacyID,
	).Scan(&dupCount); err != nil {
		t.Fatalf("count cs.tickets: %v", err)
	}
	if dupCount != 1 {
		t.Errorf("cs.tickets rows with legacy_id=%s: got %d want 1", legacyID, dupCount)
	}

	// AlreadyMigrated counter should include our row on the second
	// pass. We don't assert == 1 (the DB may carry other legacy
	// rows from concurrent tests / prior runs) — we assert >= 1.
	if second.AlreadyMigrated < 1 {
		t.Errorf("second pass AlreadyMigrated = %d want >= 1", second.AlreadyMigrated)
	}
}

// w128dHasLegacyIDColumn probes information_schema for the Wave 128D
// cs.tickets.legacy_id column. Returns true when the migration has
// been applied to this DB. Returns false on probe failure rather than
// t.Skip — the caller decides whether absence is a skip or a failure.
func w128dHasLegacyIDColumn(t *testing.T) bool {
	t.Helper()
	pool := w121cDB(t)
	var ok bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
		   SELECT 1 FROM information_schema.columns
		    WHERE table_schema = 'cs'
		      AND table_name   = 'tickets'
		      AND column_name  = 'legacy_id'
		 )`).Scan(&ok)
	if err != nil {
		return false
	}
	return ok
}
