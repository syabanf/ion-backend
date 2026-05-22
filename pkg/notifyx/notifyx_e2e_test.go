//go:build e2e

// Integration tests for the notifyx Dispatcher's outbox writes. The
// stub provider is used; we just verify that every Send call produces
// the right shape of row in platform.push_outbox.
//
// Run with:
//   go test -tags=e2e ./pkg/notifyx -v

package notifyx

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://syabanf@localhost:5432/ion_core?sslmode=disable"
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func newDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	pool := openPool(t)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(pool, log)
}

// Sends to a user that has no registered device tokens. Should
// still produce an outbox row, but marked with `delivery_error =
// 'no_device_tokens_registered'`.
func TestDispatcher_NoTokens_RecordsError(t *testing.T) {
	d := newDispatcher(t)
	ctx := context.Background()
	uid := uuid.New() // fresh user — definitely no tokens

	d.Send(ctx, Target{UserID: uid}, Message{
		Title:    "test no-tokens",
		Body:     "should land in outbox with error",
		Topic:    "test_no_tokens",
		DeepLink: "/test",
		Data:     map[string]string{"k": "v"},
	})

	var (
		kind         string
		gotUserID    *uuid.UUID
		title        string
		provider     string
		deliveredAt  *string
		deliveryErr  *string
	)
	if err := d.pool.QueryRow(ctx, `
		SELECT target_kind, user_id, title, provider,
		       delivered_at::text, delivery_error
		FROM platform.push_outbox
		WHERE topic = 'test_no_tokens'
		ORDER BY queued_at DESC LIMIT 1
	`).Scan(&kind, &gotUserID, &title, &provider, &deliveredAt, &deliveryErr); err != nil {
		t.Fatalf("read outbox: %v", err)
	}

	if kind != "user" {
		t.Errorf("target_kind = %q, want user", kind)
	}
	if gotUserID == nil || *gotUserID != uid {
		t.Errorf("user_id mismatch")
	}
	if title != "test no-tokens" {
		t.Errorf("title not preserved")
	}
	if provider != "stub" {
		t.Errorf("provider = %q, want stub", provider)
	}
	if deliveredAt != nil {
		t.Errorf("delivered_at should be NULL on no-tokens, got %v", *deliveredAt)
	}
	if deliveryErr == nil || *deliveryErr != "no_device_tokens_registered" {
		t.Errorf("delivery_error = %v, want 'no_device_tokens_registered'", deliveryErr)
	}

	// Cleanup so this test is re-runnable in the same DB.
	_, _ = d.pool.Exec(ctx, `DELETE FROM platform.push_outbox WHERE topic = 'test_no_tokens'`)
}

// Sends to a user that DOES have a registered device token. The stub
// provider always succeeds, so delivered_at should be set.
func TestDispatcher_WithToken_MarksDelivered(t *testing.T) {
	d := newDispatcher(t)
	ctx := context.Background()
	uid := uuid.New()

	// Seed a token. The dispatcher's tokensFor filters by
	// last_seen_at > NOW() - 60d, so explicit NOW() keeps it fresh.
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO platform.device_tokens (user_id, token, platform, app, last_seen_at)
		VALUES ($1, $2, 'ios', 'test', NOW())
	`, uid, "tok_"+uuid.New().String()); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	defer d.pool.Exec(ctx, `DELETE FROM platform.device_tokens WHERE user_id = $1`, uid)

	d.Send(ctx, Target{UserID: uid}, Message{
		Title: "test with-token",
		Body:  "should mark delivered",
		Topic: "test_with_token",
	})

	var deliveredAt *string
	var deliveryErr *string
	if err := d.pool.QueryRow(ctx, `
		SELECT delivered_at::text, delivery_error
		FROM platform.push_outbox
		WHERE topic = 'test_with_token'
		ORDER BY queued_at DESC LIMIT 1
	`).Scan(&deliveredAt, &deliveryErr); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if deliveredAt == nil {
		t.Error("delivered_at should be set when stub provider succeeded")
	}
	if deliveryErr != nil {
		t.Errorf("delivery_error should be nil on success, got %v", *deliveryErr)
	}

	_, _ = d.pool.Exec(ctx, `DELETE FROM platform.push_outbox WHERE topic = 'test_with_token'`)
}

// Bulk fan-out (Target.UserIDs) records `target_kind = 'users'` and
// stores the full id set in recipient_ids.
func TestDispatcher_BulkFanout_RecordsRecipients(t *testing.T) {
	d := newDispatcher(t)
	ctx := context.Background()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	d.Send(ctx, Target{UserIDs: ids}, Message{
		Title: "test bulk",
		Body:  "fan-out",
		Topic: "test_bulk",
	})

	var (
		kind    string
		got     []uuid.UUID
	)
	if err := d.pool.QueryRow(ctx, `
		SELECT target_kind, recipient_ids
		FROM platform.push_outbox
		WHERE topic = 'test_bulk'
		ORDER BY queued_at DESC LIMIT 1
	`).Scan(&kind, &got); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if kind != "users" {
		t.Errorf("target_kind = %q, want users", kind)
	}
	if len(got) != len(ids) {
		t.Errorf("recipient_ids len = %d, want %d", len(got), len(ids))
	}

	_, _ = d.pool.Exec(ctx, `DELETE FROM platform.push_outbox WHERE topic = 'test_bulk'`)
}
