// Wave 120 — refund headroom edge.
//
// Pins TC-PAY-* "refund cannot exceed intent.amount - already_refunded".
// This complements the existing TestRefundService_HeadroomExceeded in
// refund_test.go by additionally exercising the boundary (request EXACTLY
// the remaining headroom — must succeed) and the just-over case (headroom
// + 0.01 — must fail with the validation code).

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func TestRefundService_RefundExceedingIntent_Boundary(t *testing.T) {
	cases := []struct {
		name        string
		intentAmt   float64
		alreadyDone float64
		requestAmt  float64
		wantCode    string // empty = must succeed
	}{
		{
			name:        "exactly_at_headroom_ok",
			intentAmt:   100000,
			alreadyDone: 60000,
			requestAmt:  40000,
			wantCode:    "",
		},
		{
			name:        "one_rupiah_over_headroom_fails",
			intentAmt:   100000,
			alreadyDone: 60000,
			requestAmt:  40001,
			wantCode:    "refund.amount_exceeds_headroom",
		},
		{
			name:        "double_full_amount_fails",
			intentAmt:   100000,
			alreadyDone: 0,
			requestAmt:  200000,
			wantCode:    "refund.amount_exceeds_headroom",
		},
		{
			name:        "single_full_refund_ok",
			intentAmt:   100000,
			alreadyDone: 0,
			requestAmt:  100000,
			wantCode:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intents := newMemIntentRepo()
			intentID := uuid.New()
			intent := &domain.PaymentIntent{
				ID:             intentID,
				InvoiceID:      uuid.New(),
				Amount:         tc.intentAmt,
				RefundedAmount: tc.alreadyDone,
				Status:         domain.PaymentStatusSucceeded,
			}
			if tc.alreadyDone > 0 {
				intent.Status = domain.PaymentStatusPartiallyRefunded
			}
			intents.rows[intent.ID] = intent

			refunds := newMemRefundRepo()
			gateways := &memGatewayRepo{}
			registry := &stubRegistry{clients: map[string]port.GatewayClient{}}
			svc := NewRefundService(refunds, intents, gateways, registry, nil)

			_, err := svc.RequestRefund(context.Background(), port.RequestRefundInput{
				PaymentIntentID: intent.ID,
				Amount:          tc.requestAmt,
				Reason:          "boundary",
			})
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error code %q, got nil", tc.wantCode)
			}
			if de := derrors.As(err); de == nil || de.Code != tc.wantCode {
				t.Fatalf("error code = %v, want %q", err, tc.wantCode)
			}
		})
	}
}
