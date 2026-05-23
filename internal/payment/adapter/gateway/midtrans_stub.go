package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// MidtransStub mirrors XenditStub for the Midtrans aggregator. Kept
// separate because the production REST flows diverge — Midtrans uses
// a SHA-512 signature_key + per-request order id, whereas Xendit signs
// the entire body. For stub mode we reuse the HMAC-SHA256 pattern.
type MidtransStub struct {
	code   string
	secret string
}

func NewMidtransStub(secret string) *MidtransStub {
	return &MidtransStub{code: "midtrans", secret: secret}
}

var _ port.GatewayClient = (*MidtransStub)(nil)

func (m *MidtransStub) Code() string { return m.code }

func (m *MidtransStub) CreatePayment(ctx context.Context, in port.CreatePaymentInput) (*port.CreatePaymentResult, error) {
	short := strings.ReplaceAll(in.IntentID.String(), "-", "")[:10]
	expires := time.Now().Add(24 * time.Hour)
	return &port.CreatePaymentResult{
		ExternalRef: "mt_" + short,
		PaymentURL:  "https://checkout.example.invalid/midtrans/" + short,
		VANumber:    "9900" + short,
		ExpiresAt:   &expires,
	}, nil
}

func (m *MidtransStub) RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*port.RefundResult, error) {
	return &port.RefundResult{
		ExternalRef: "mt_refund_" + intent.ID.String()[:8],
	}, nil
}

func (m *MidtransStub) CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*port.CheckStatusResult, error) {
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

func (m *MidtransStub) VerifySignature(payload []byte, signature string) bool {
	if m.secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(m.secret))
	_, _ = mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.TrimSpace(signature)))
}

type midtransWebhookEnvelope struct {
	TransactionID     string  `json:"transaction_id"`
	OrderID           string  `json:"order_id"`
	TransactionStatus string  `json:"transaction_status"`
	StatusCode        string  `json:"status_code"`
	GrossAmount       string  `json:"gross_amount,omitempty"`
	PaymentType       string  `json:"payment_type,omitempty"`
	SettlementTime    string  `json:"settlement_time,omitempty"`
	StatusMessage     string  `json:"status_message,omitempty"`
	Fraud             string  `json:"fraud_status,omitempty"`
	Amount            float64 `json:"-"`
}

func (m *MidtransStub) ParseWebhook(payload []byte) (port.ParsedWebhook, error) {
	var env midtransWebhookEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return port.ParsedWebhook{}, fmt.Errorf("midtrans: parse webhook: %w", err)
	}
	out := port.ParsedWebhook{
		ExternalEventID:    env.TransactionID,
		ExternalPaymentRef: env.OrderID,
	}
	switch strings.ToLower(env.TransactionStatus) {
	case "settlement", "capture", "success":
		out.NewStatus = domain.PaymentStatusSucceeded
		if env.SettlementTime != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", env.SettlementTime); err == nil {
				out.PaidAt = &t
			}
		}
		if out.PaidAt == nil {
			now := time.Now().UTC()
			out.PaidAt = &now
		}
	case "deny", "cancel", "expire", "failure":
		out.NewStatus = domain.PaymentStatusFailed
		out.FailureCode = env.StatusCode
		out.FailureReason = env.StatusMessage
	}
	return out, nil
}

func (m *MidtransStub) ParseH2HStatement(content []byte) ([]port.ParsedH2HLine, error) {
	return nil, errors.New("midtrans: H2H statement parsing not supported")
}
