// Package notifyx is the push-notification dispatcher.
//
// It deliberately ships with a stub provider that just logs the
// payload — wiring an actual FCM/APNS client takes credentials we
// don't carry in this build. The interface is set up so a follow-up
// PR drops in a real adapter (firebase-admin / apns2 / OneSignal /
// WhatsApp Business) by implementing `Push.Send` and registering it
// from each service's main.go.
//
// Why a separate package: every service (crm, field, billing,
// enterprise) needs to fan a notification out to the right device
// tokens. Putting the dispatcher in one place keeps the token-lookup
// query + per-platform routing rules out of every handler file.
package notifyx

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Push is the dispatcher interface. Implementations:
//   - StubPush     — logs only (default; ships in this build)
//   - FCMPush      — real Firebase Cloud Messaging (TODO follow-up)
//   - WhatsAppPush — WhatsApp Business API (TODO follow-up)
type Push interface {
	Send(ctx context.Context, target Target, msg Message) error
	// Name identifies the provider in the push_outbox audit log.
	// Examples: "stub", "fcm", "apns", "whatsapp".
	Name() string
}

// Target is either a single user, a single customer, or a broadcast
// to a set of user-ids. Exactly one of the three should be non-zero.
type Target struct {
	UserID     uuid.UUID
	CustomerID uuid.UUID
	UserIDs    []uuid.UUID // bulk to e.g. all techs in a branch
}

// Message is the platform-agnostic payload. Adapters translate it to
// FCM/APNS/etc.-specific shapes.
type Message struct {
	Title    string            // visible push title
	Body     string            // visible push body
	DeepLink string            // app://… or https://… to open on tap
	Data     map[string]string // free-form payload for the app to read
	Topic    string            // category tag (sla_breach, payment, …)
}

// Dispatcher fans a Message out to every device token belonging to
// the target. The provider field decides what happens after the
// token lookup — by default it's StubPush.
type Dispatcher struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	provider Push
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Dispatcher {
	return &Dispatcher{pool: pool, log: log, provider: NewStubPush(log)}
}

// WithProvider swaps the underlying push provider. Call from main.go
// at startup to wire FCM / APNS once those adapters land.
func (d *Dispatcher) WithProvider(p Push) *Dispatcher {
	d.provider = p
	return d
}

// Send resolves the target → device tokens → provider.Send fan-out.
// Errors are logged but not aggregated — a single failed device
// shouldn't fail the whole batch.
//
// Persistence: every Send writes one row into platform.push_outbox
// before resolving tokens. The row is updated with delivered_at on
// success or delivery_error on failure. This means even pushes that
// can't be delivered (no tokens registered, provider down, etc.)
// have a forensic audit trail.
func (d *Dispatcher) Send(ctx context.Context, target Target, msg Message) {
	outboxID := d.recordToOutbox(ctx, target, msg)

	tokens, err := d.tokensFor(ctx, target)
	if err != nil {
		d.log.Error("notifyx.tokens_lookup_failed", "err", err)
		d.markOutboxError(ctx, outboxID, "tokens_lookup_failed: "+err.Error())
		return
	}
	if len(tokens) == 0 {
		d.log.Debug("notifyx.no_tokens", "target", target)
		d.markOutboxError(ctx, outboxID, "no_device_tokens_registered")
		return
	}
	var failed int
	var lastErr error
	for _, tok := range tokens {
		t := Target{}
		if tok.userID != uuid.Nil {
			t.UserID = tok.userID
		}
		if tok.customerID != uuid.Nil {
			t.CustomerID = tok.customerID
		}
		if err := d.provider.Send(ctx, t, msg); err != nil {
			failed++
			lastErr = err
			d.log.Warn("notifyx.send_failed",
				"err", err, "token_suffix", lastN(tok.token, 8))
		}
	}
	if failed == len(tokens) {
		d.markOutboxError(ctx, outboxID,
			"all_tokens_failed: "+errString(lastErr))
		return
	}
	d.markOutboxDelivered(ctx, outboxID)
}

// recordToOutbox inserts a queued row and returns the new id (zero
// uuid on insert failure — caller still proceeds because the push
// itself can still go through; we just lose the audit trail for it).
func (d *Dispatcher) recordToOutbox(ctx context.Context, target Target, msg Message) uuid.UUID {
	provider := "stub"
	if d.provider != nil {
		provider = d.provider.Name()
	}
	dataJSON := []byte("{}")
	if len(msg.Data) > 0 {
		b, err := json.Marshal(msg.Data)
		if err == nil {
			dataJSON = b
		}
	}
	var (
		kind         string
		userID       *uuid.UUID
		customerID   *uuid.UUID
		recipientIDs []uuid.UUID
	)
	switch {
	case target.UserID != uuid.Nil:
		kind = "user"
		uid := target.UserID
		userID = &uid
	case target.CustomerID != uuid.Nil:
		kind = "customer"
		cid := target.CustomerID
		customerID = &cid
	case len(target.UserIDs) > 0:
		kind = "users"
		recipientIDs = target.UserIDs
	default:
		// Shouldn't happen — Send is only called with one of the three
		// target shapes. Default to "users" with an empty array so the
		// CHECK constraint doesn't trip.
		kind = "users"
		recipientIDs = nil
	}
	var id uuid.UUID
	err := d.pool.QueryRow(ctx, `
		INSERT INTO platform.push_outbox
		    (target_kind, user_id, customer_id, recipient_ids,
		     title, body, deep_link, topic, data, provider)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,''), NULLIF($8,''), $9::jsonb, $10)
		RETURNING id
	`, kind, userID, customerID, recipientIDs,
		msg.Title, msg.Body, msg.DeepLink, msg.Topic,
		string(dataJSON), provider).Scan(&id)
	if err != nil {
		d.log.Warn("notifyx.outbox_insert_failed", "err", err)
		return uuid.Nil
	}
	return id
}

func (d *Dispatcher) markOutboxDelivered(ctx context.Context, id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	_, err := d.pool.Exec(ctx, `
		UPDATE platform.push_outbox
		SET delivered_at = NOW(), delivery_error = NULL
		WHERE id = $1
	`, id)
	if err != nil {
		d.log.Warn("notifyx.outbox_mark_delivered_failed", "err", err, "id", id)
	}
}

func (d *Dispatcher) markOutboxError(ctx context.Context, id uuid.UUID, msg string) {
	if id == uuid.Nil {
		return
	}
	_, err := d.pool.Exec(ctx, `
		UPDATE platform.push_outbox
		SET delivery_error = $2
		WHERE id = $1
	`, id, msg)
	if err != nil {
		d.log.Warn("notifyx.outbox_mark_error_failed", "err", err, "id", id)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type tokenRow struct {
	token      string
	platform   string
	app        string
	userID     uuid.UUID
	customerID uuid.UUID
}

func (d *Dispatcher) tokensFor(ctx context.Context, t Target) ([]tokenRow, error) {
	q := `SELECT token, platform, app,
	             COALESCE(user_id, '00000000-0000-0000-0000-000000000000'),
	             COALESCE(customer_id, '00000000-0000-0000-0000-000000000000')
	      FROM platform.device_tokens
	      WHERE last_seen_at > NOW() - INTERVAL '60 days'`
	args := []any{}
	switch {
	case t.UserID != uuid.Nil:
		args = append(args, t.UserID)
		q += " AND user_id = $1"
	case t.CustomerID != uuid.Nil:
		args = append(args, t.CustomerID)
		q += " AND customer_id = $1"
	case len(t.UserIDs) > 0:
		args = append(args, t.UserIDs)
		q += " AND user_id = ANY($1)"
	default:
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tokenRow
	for rows.Next() {
		var x tokenRow
		if err := rows.Scan(&x.token, &x.platform, &x.app, &x.userID, &x.customerID); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.Repeat("·", n) + s[len(s)-n:]
}

// =============================================================================
// StubPush — default provider. Logs the payload, succeeds.
// =============================================================================

type StubPush struct {
	log *slog.Logger
}

func NewStubPush(log *slog.Logger) *StubPush {
	return &StubPush{log: log}
}

func (s *StubPush) Send(ctx context.Context, target Target, msg Message) error {
	s.log.Info("notifyx.stub_send",
		"title", msg.Title,
		"body", msg.Body,
		"topic", msg.Topic,
		"deep_link", msg.DeepLink,
		"user_id", target.UserID,
		"customer_id", target.CustomerID,
		"now", time.Now().Format(time.RFC3339),
	)
	return nil
}

// Name returns the provider identifier written to platform.push_outbox.
// Stub-provider rows are easy to filter out of admin views.
func (s *StubPush) Name() string { return "stub" }
