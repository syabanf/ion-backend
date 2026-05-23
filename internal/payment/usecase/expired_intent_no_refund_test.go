// Wave 120 — refund on non-eligible intent state.
//
// Pins TC-PAY-* "refund_request must reject expired/failed/cancelled/pending
// intents with a Conflict; only succeeded + partially_refunded are
// eligible". RefundService.RequestRefund enforces this branch and emits
// `refund.intent_not_eligible`.

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func TestRefundService_RefundOnExpiredIntent_Conflicts(t *testing.T) {
	cases := []struct {
		name   string
		status domain.PaymentStatus
		want   string // expected code; "" = succeed
	}{
		{"pending_blocked", domain.PaymentStatusPending, "refund.intent_not_eligible"},
		{"expired_blocked", domain.PaymentStatusExpired, "refund.intent_not_eligible"},
		{"failed_blocked", domain.PaymentStatusFailed, "refund.intent_not_eligible"},
		{"cancelled_blocked", domain.PaymentStatusCancelled, "refund.intent_not_eligible"},
		{"refunded_blocked", domain.PaymentStatusRefunded, "refund.intent_not_eligible"},
		{"succeeded_ok", domain.PaymentStatusSucceeded, ""},
		{"partial_ok", domain.PaymentStatusPartiallyRefunded, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intents := newMemIntentRepo()
			intent := &domain.PaymentIntent{
				ID:             uuid.New(),
				InvoiceID:      uuid.New(),
				Amount:         100000,
				RefundedAmount: 0,
				Status:         tc.status,
			}
			if tc.status == domain.PaymentStatusPartiallyRefunded {
				intent.RefundedAmount = 25000
			}
			intents.rows[intent.ID] = intent

			refunds := newMemRefundRepo()
			gateways := &memGatewayRepo{}
			registry := &stubRegistry{clients: map[string]port.GatewayClient{}}
			svc := NewRefundService(refunds, intents, gateways, registry, nil)

			_, err := svc.RequestRefund(context.Background(), port.RequestRefundInput{
				PaymentIntentID: intent.ID,
				Amount:          10000,
				Reason:          "test",
			})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected success on %s, got %v", tc.status, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s to be blocked, got success", tc.status)
			}
			if de := derrors.As(err); de == nil || de.Code != tc.want {
				t.Fatalf("status=%s err = %v, want code %s", tc.status, err, tc.want)
			}
		})
	}
}
