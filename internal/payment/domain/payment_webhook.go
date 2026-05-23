package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// WebhookStatus tracks an inbound webhook event through verification +
// processing.
//
// State machine:
//
//	received → verified → processed                          (positive path)
//	received → failed                                         (verification ok but processing crashed)
//	received → duplicate                                      (terminal — re-delivery, no side effect)
//	received → suspect                                        (terminal — signature mismatch)
type WebhookStatus string

const (
	WebhookStatusReceived  WebhookStatus = "received"
	WebhookStatusVerified  WebhookStatus = "verified"
	WebhookStatusProcessed WebhookStatus = "processed"
	WebhookStatusFailed    WebhookStatus = "failed"
	WebhookStatusDuplicate WebhookStatus = "duplicate"
	WebhookStatusSuspect   WebhookStatus = "suspect"
)

// PaymentWebhook is one inbound delivery from a gateway. The DB
// `(gateway_id, external_event_id)` UNIQUE constraint is the
// authoritative dedup — this struct just carries the parsed payload
// and status while it moves through the ingest flow.
type PaymentWebhook struct {
	ID               uuid.UUID
	GatewayID        uuid.UUID
	ExternalEventID  string
	Payload          []byte // raw JSON; never `string` so signature verification is byte-exact
	SignatureValid   bool
	Status           WebhookStatus
	PaymentIntentID  *uuid.UUID
	ErrorMsg         string
	ReceivedAt       time.Time
	ProcessedAt      *time.Time
}

// NewPaymentWebhook builds a freshly-received row before signature
// verification. The adapter sets `SignatureValid` after the constant-
// time HMAC compare; the usecase then transitions to verified or
// suspect from there.
func NewPaymentWebhook(gatewayID uuid.UUID, externalEventID string, payload []byte) (*PaymentWebhook, error) {
	if gatewayID == uuid.Nil {
		return nil, errors.Validation("webhook.gateway_required", "gateway_id is required")
	}
	externalEventID = strings.TrimSpace(externalEventID)
	if externalEventID == "" {
		return nil, errors.Validation("webhook.event_id_required", "external_event_id is required")
	}
	if len(payload) == 0 {
		return nil, errors.Validation("webhook.payload_empty", "payload is required")
	}
	return &PaymentWebhook{
		ID:              uuid.New(),
		GatewayID:       gatewayID,
		ExternalEventID: externalEventID,
		Payload:         payload,
		Status:          WebhookStatusReceived,
		ReceivedAt:      time.Now().UTC(),
	}, nil
}

// MarkVerified flips received → verified after a passing signature
// check. Idempotent on already-verified.
func (w *PaymentWebhook) MarkVerified() error {
	if w.Status == WebhookStatusVerified {
		return nil
	}
	if w.Status != WebhookStatusReceived {
		return errors.Conflict(
			"webhook.cannot_verify",
			"only received webhooks can be verified",
		)
	}
	w.SignatureValid = true
	w.Status = WebhookStatusVerified
	return nil
}

// MarkProcessed flips verified → processed once the side effect (intent
// status flip, refund completion, …) is committed.
func (w *PaymentWebhook) MarkProcessed(intentID *uuid.UUID) error {
	if w.Status != WebhookStatusVerified {
		return errors.Conflict(
			"webhook.cannot_process",
			"only verified webhooks can be marked processed",
		)
	}
	now := time.Now().UTC()
	w.PaymentIntentID = intentID
	w.ProcessedAt = &now
	w.Status = WebhookStatusProcessed
	return nil
}

// MarkSuspect is the terminal signature-mismatch path. The payload is
// still stored so the SRE runbook can investigate replay attempts.
func (w *PaymentWebhook) MarkSuspect(reason string) {
	w.SignatureValid = false
	w.Status = WebhookStatusSuspect
	w.ErrorMsg = strings.TrimSpace(reason)
}

// MarkDuplicate is the terminal "already saw this event" path. The
// row is still inserted (well — re-attempted then surfaced from the
// existing row) so the audit trail captures the re-delivery attempt.
func (w *PaymentWebhook) MarkDuplicate() {
	w.Status = WebhookStatusDuplicate
}

// MarkFailed is the terminal "verified but couldn't process" path.
// Distinct from suspect (where the signature itself failed) so finance
// can triage retriable bugs vs. attempted forgery.
func (w *PaymentWebhook) MarkFailed(reason string) {
	w.Status = WebhookStatusFailed
	w.ErrorMsg = strings.TrimSpace(reason)
}

// VerifySignature is a stub HMAC-SHA256 helper. Most adapters use
// pkg/webhookx for the production path; this domain-level helper
// exists so unit tests can exercise the verification flow without
// dragging the HTTP layer in. Constant-time compare via hmac.Equal.
func VerifySignature(payload []byte, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.TrimSpace(signature)))
}
