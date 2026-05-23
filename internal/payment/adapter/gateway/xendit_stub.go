// Package gateway holds per-gateway clients (port.GatewayClient
// implementations) for the payment bounded context.
//
// Wave 111 ships stubs only: every adapter satisfies the contract
// without making real network calls. The stub mode lets the rest of
// the codebase (intent.go, refund.go, h2h.go, webhook.go) exercise the
// full flow against deterministic, in-memory responses — perfect for
// the broadband-UAT TC catalog.
//
// Production credentials slot in behind the env flags:
//
//   - XENDIT_ENABLED=true        → real Xendit REST client (TODO Wave 112+)
//   - BCA_H2H_ENABLED=true       → real BCA H2H SFTP poller (TODO Wave 112+)
//   - MIDTRANS_ENABLED=true      → real Midtrans REST client
//   - STRIPE_ENABLED=true        → real Stripe REST client
//
// When the env flag is unset (default) the binary keeps using stubs so
// `go test ./...` and local dev DBs stay self-contained.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// XenditStub satisfies port.GatewayClient for the Xendit aggregator
// without hitting the real REST API. CreatePayment returns a synthetic
// VA number; the webhook ingest is driven by a separate
// SimulateWebhook helper that the http handler exposes only in
// stub mode.
type XenditStub struct {
	code   string
	secret string
}

// NewXenditStub constructs a Xendit stub adapter. The signing secret
// is used by VerifySignature so the local webhook simulator can sign
// payloads with the same key the production Xendit would.
func NewXenditStub(secret string) *XenditStub {
	return &XenditStub{code: "xendit", secret: secret}
}

var _ port.GatewayClient = (*XenditStub)(nil)

func (x *XenditStub) Code() string { return x.code }

func (x *XenditStub) CreatePayment(ctx context.Context, in port.CreatePaymentInput) (*port.CreatePaymentResult, error) {
	// Deterministic VA number derived from the intent id so the same
	// retry hits the same VA — handy for local dev replays.
	short := strings.ReplaceAll(in.IntentID.String(), "-", "")[:10]
	va := "8800" + short
	expiresAt := time.Now().Add(24 * time.Hour)
	return &port.CreatePaymentResult{
		ExternalRef: "xnd_" + short,
		PaymentURL:  "https://checkout.example.invalid/xendit/" + short,
		VANumber:    va,
		ExpiresAt:   &expiresAt,
	}, nil
}

func (x *XenditStub) RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*port.RefundResult, error) {
	return &port.RefundResult{
		ExternalRef: "xnd_refund_" + uuid.New().String()[:8],
	}, nil
}

func (x *XenditStub) CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*port.CheckStatusResult, error) {
	// Stub returns the current status verbatim — the cron uses this to
	// disambiguate "really never paid" from "webhook lost", but for
	// stub-mode we trust the local DB.
	ref := ""
	if intent.ExternalPaymentRef != nil {
		ref = *intent.ExternalPaymentRef
	}
	return &port.CheckStatusResult{
		Status:      string(intent.Status),
		ExternalRef: ref,
		PaidAt:      intent.PaidAt,
	}, nil
}

// VerifySignature performs HMAC-SHA256 hex verification. Real Xendit
// uses a static `X-Callback-Token` rather than HMAC; we use HMAC in
// the stub so the tests for the webhook flow exercise the constant-
// time compare path that the production adapter will rely on.
func (x *XenditStub) VerifySignature(payload []byte, signature string) bool {
	if x.secret == "" {
		// Empty secret = local dev — accept everything but log via the
		// caller's logger so it's obvious in the access log.
		return true
	}
	mac := hmac.New(sha256.New, []byte(x.secret))
	_, _ = mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.TrimSpace(signature)))
}

// xenditWebhookEnvelope is the subset of Xendit's webhook payload we
// care about. Real Xendit ships ~30 fields; we map only the load-bearing
// ones onto port.ParsedWebhook.
type xenditWebhookEnvelope struct {
	ID          string  `json:"id"`
	ExternalID  string  `json:"external_id"`
	Status      string  `json:"status"`
	PaidAt      string  `json:"paid_at,omitempty"`
	FailureCode string  `json:"failure_code,omitempty"`
	Amount      float64 `json:"amount,omitempty"`
	EventID     string  `json:"event_id,omitempty"`
}

func (x *XenditStub) ParseWebhook(payload []byte) (port.ParsedWebhook, error) {
	var env xenditWebhookEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return port.ParsedWebhook{}, fmt.Errorf("xendit: parse webhook: %w", err)
	}
	out := port.ParsedWebhook{
		ExternalEventID:    coalesce(env.EventID, env.ID),
		ExternalPaymentRef: env.ID,
	}
	switch strings.ToLower(env.Status) {
	case "paid", "settled", "succeeded", "completed":
		out.NewStatus = domain.PaymentStatusSucceeded
		if env.PaidAt != "" {
			if t, err := time.Parse(time.RFC3339, env.PaidAt); err == nil {
				out.PaidAt = &t
			}
		}
		if out.PaidAt == nil {
			now := time.Now().UTC()
			out.PaidAt = &now
		}
	case "failed", "expired":
		out.NewStatus = domain.PaymentStatus(strings.ToLower(env.Status))
		out.FailureCode = env.FailureCode
	default:
		// Unrecognised status — leave NewStatus empty and let the
		// usecase log + drop without crashing.
	}
	return out, nil
}

// ParseH2HStatement isn't supported on Xendit (it's an aggregator).
func (x *XenditStub) ParseH2HStatement(content []byte) ([]port.ParsedH2HLine, error) {
	return nil, fmt.Errorf("xendit: H2H bank statement parsing is not supported by this gateway kind")
}

func coalesce(parts ...string) string {
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return ""
}
