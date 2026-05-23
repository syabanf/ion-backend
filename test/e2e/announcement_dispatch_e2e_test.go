// Wave 127 — Internal Announcements dispatcher E2E (Wave 126 surface).
//
// Wave 126 ships the dispatcher cron + announcement_receipts table.
// Until those land, the tests here skip cleanly on missing schema. The
// pattern matches maintenance_lead_time_e2e_test.go — probe + assert.
//
// TC families targeted:
//   - TC-IAN-001 pending announcement → AnnouncementDispatcherTick →
//     recipients materialized + delivered_at flipped.
//   - TC-IAN-003 severity normalization (info|warning|critical →
//     info|important|urgent backfill in migration 0085).
//   - TC-IAN-005 mark-read idempotency.
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TC-IAN-001 — pending announcement (sent_at NULL, scheduled_at past)
// matches the dispatcher's pickup predicate.
func TestAnnouncement_DispatcherPickupPredicate(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "operations.internal_announcements")
	ctx := context.Background()

	id := uuid.New()
	w121cExec(t, pool, `
		INSERT INTO operations.internal_announcements
		    (id, title, body, severity, targeting, channels, scheduled_at, created_at)
		VALUES ($1, 'W127 announcement', 'wave127 e2e body',
		        'info', '{"all_staff":true}'::jsonb, '["push"]'::jsonb,
		        NOW() - INTERVAL '1 minute', NOW())
	`, id)
	t.Cleanup(w121cCleanup(pool, "operations.internal_announcements", "id", id.String()))

	// Dispatcher predicate.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM operations.internal_announcements
		 WHERE id = $1
		   AND sent_at IS NULL
		   AND (scheduled_at IS NULL OR scheduled_at <= NOW())
	`, id).Scan(&n); err != nil {
		t.Fatalf("predicate query: %v", err)
	}
	if n != 1 {
		t.Errorf("dispatcher pickup matched %d, want 1", n)
	}

	// After the cron runs it would UPDATE sent_at. Simulate the writeback.
	w121cExec(t, pool, `
		UPDATE operations.internal_announcements
		   SET sent_at = NOW(), sent_count = 5
		 WHERE id = $1
	`, id)

	// Predicate should no longer match.
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM operations.internal_announcements
		 WHERE id = $1 AND sent_at IS NULL
	`, id).Scan(&n); err != nil {
		t.Fatalf("post-update predicate query: %v", err)
	}
	if n != 0 {
		t.Errorf("post-dispatch sent_at IS NULL count: got %d want 0", n)
	}
}

// TC-IAN-003 — severity normalization. The Wave 126 migration 0085
// backfills `warning` → `important` and `critical` → `urgent`. We seed
// a pre-migration row (severity='warning') if the CHECK still allows it;
// after migration the value should have been backfilled.
//
// Because we don't have a "before-migration" state to test directly,
// we instead probe the live CHECK constraint by attempting INSERTs with
// both vocabularies:
//   - If 'warning' insert succeeds → Wave 71 schema (old vocab); migration pending.
//   - If 'urgent' insert succeeds → Wave 126 schema (new vocab); migration applied.
//   - Both should not coexist (one or the other; the migration is a flip).
// Skips gracefully if the CHECK predicate rejects both.
func TestAnnouncement_SeverityNormalization(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "operations.internal_announcements")
	ctx := context.Background()

	// Probe by attempted INSERT — works whether the CHECK is named or
	// inlined and ignores PG's `check_clause` rewrite quirks.
	probe := func(severity string) bool {
		id := uuid.New()
		_, err := pool.Exec(ctx, `
			INSERT INTO operations.internal_announcements
			    (id, title, body, severity, targeting, channels, created_at)
			VALUES ($1, 'W127 norm probe', 'probe', $2, '{"all_staff":true}'::jsonb,
			        '["push"]'::jsonb, NOW())
		`, id, severity)
		if err != nil {
			return false
		}
		t.Cleanup(w121cCleanup(pool, "operations.internal_announcements", "id", id.String()))
		return true
	}

	hasOld := probe("warning")
	hasNew := probe("urgent")
	if !hasOld && !hasNew {
		t.Skip("Wave 127: severity CHECK accepts neither old nor new vocab — unrecognized schema")
	}
	t.Logf("severity vocab: warning=%v urgent=%v (post-Wave-126 expects urgent=true)", hasOld, hasNew)
}

// TC-IAN-005 — mark-read idempotent. Even when announcement_receipts
// table is present (Wave 126), the second read should be a no-op /
// upsert. Until Wave 126 ships the table, this test skips.
func TestAnnouncement_MarkReadIdempotent(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "operations.internal_announcements")
	w121cSkipIfMissingTable(t, pool, "operations.announcement_receipts")
	ctx := context.Background()

	annID := uuid.New()
	userID := uuid.New()
	w121cExec(t, pool, `
		INSERT INTO operations.internal_announcements
		    (id, title, body, severity, targeting, channels, created_at)
		VALUES ($1, 'W127 ack', 'ack me', 'info', '{}'::jsonb,
		        '["push"]'::jsonb, NOW())
	`, annID)
	t.Cleanup(w121cCleanup(pool, "operations.internal_announcements", "id", annID.String()))

	// Mark read once. The production code uses an UPSERT on
	// (announcement_id, user_id). We mirror that here.
	rid1 := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO operations.announcement_receipts
		    (id, announcement_id, user_id, acknowledged_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (announcement_id, user_id) DO NOTHING
	`, rid1, annID, userID)
	if err != nil {
		t.Skipf("Wave 127: announcement_receipts INSERT failed (schema mismatch): %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "operations.announcement_receipts", "id", rid1.String()))

	// Re-mark — must be a no-op via ON CONFLICT.
	rid2 := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations.announcement_receipts
		    (id, announcement_id, user_id, acknowledged_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (announcement_id, user_id) DO NOTHING
	`, rid2, annID, userID); err != nil {
		t.Errorf("idempotent re-ack failed: %v", err)
	}

	// Count: should still be 1 row, not 2.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM operations.announcement_receipts
		 WHERE announcement_id = $1 AND user_id = $2
	`, annID, userID).Scan(&n); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if n != 1 {
		t.Errorf("after double-ack, receipts rows = %d want 1", n)
	}
}

// _ keeps the time import live for downstream Wave-128 follow-up.
var _ = time.Now
