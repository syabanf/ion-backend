package usecase

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type memRefundRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.Refund
}

func newMemRefundRepo() *memRefundRepo {
	return &memRefundRepo{rows: map[uuid.UUID]*domain.Refund{}}
}

func (m *memRefundRepo) Create(ctx context.Context, r *domain.Refund) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cpy := *r
	m.rows[r.ID] = &cpy
	return nil
}

func (m *memRefundRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, derrors.NotFound("refund.not_found", "not found")
	}
	cpy := *r
	return &cpy, nil
}

func (m *memRefundRepo) List(ctx context.Context, f port.RefundListFilter) ([]domain.Refund, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Refund{}
	for _, r := range m.rows {
		if f.PaymentIntentID != nil && r.PaymentIntentID != *f.PaymentIntentID {
			continue
		}
		if f.Status != "" && string(r.Status) != f.Status {
			continue
		}
		out = append(out, *r)
	}
	return out, len(out), nil
}

func (m *memRefundRepo) Update(ctx context.Context, r *domain.Refund) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[r.ID]; !ok {
		return derrors.NotFound("refund.not_found", "not found")
	}
	cpy := *r
	m.rows[r.ID] = &cpy
	return nil
}

func (m *memRefundRepo) SumCompletedForIntent(ctx context.Context, intentID uuid.UUID) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, r := range m.rows {
		if r.PaymentIntentID == intentID && r.Status == domain.RefundStatusCompleted {
			sum += r.Amount
		}
	}
	return sum, nil
}

func TestRefundService_RequestApproveProcessComplete(t *testing.T) {
	// Set up an intent already in 'succeeded' state.
	intents := newMemIntentRepo()
	gwID := uuid.New()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: gwID, Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator, SupportedMethods: []string{"va_bca"}},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	intentSvc := NewIntentService(intents, gateways, webhooks, NewRoutingService(), registry, nil)

	intent, err := intentSvc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      uuid.New(),
		Amount:         100000,
		IdempotencyKey: "refund-test-1",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	// Push intent to succeeded directly via repo so we don't depend
	// on the webhook flow.
	intents.mu.Lock()
	intents.rows[intent.ID].Status = domain.PaymentStatusSucceeded
	intents.mu.Unlock()

	refunds := newMemRefundRepo()
	refundSvc := NewRefundService(refunds, intents, gateways, registry, nil)

	requestedBy := uuid.New()
	r, err := refundSvc.RequestRefund(context.Background(), port.RequestRefundInput{
		PaymentIntentID: intent.ID,
		Amount:          40000,
		Reason:          "duplicate charge",
		RequestedBy:     &requestedBy,
	})
	if err != nil {
		t.Fatalf("RequestRefund: %v", err)
	}
	if r.Status != domain.RefundStatusRequested {
		t.Fatalf("status = %q, want requested", r.Status)
	}

	approver := uuid.New()
	r2, err := refundSvc.ApproveRefund(context.Background(), r.ID, approver)
	if err != nil {
		t.Fatalf("ApproveRefund: %v", err)
	}
	if r2.Status != domain.RefundStatusApproved {
		t.Fatalf("status = %q, want approved", r2.Status)
	}

	r3, err := refundSvc.ProcessRefund(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("ProcessRefund: %v", err)
	}
	if r3.Status != domain.RefundStatusProcessing {
		t.Fatalf("status = %q, want processing", r3.Status)
	}
	if r3.ExternalRefundRef == nil {
		t.Fatalf("expected external_refund_ref")
	}

	r4, err := refundSvc.MarkRefundCompleted(context.Background(), r.ID, "gateway-ref-99")
	if err != nil {
		t.Fatalf("MarkRefundCompleted: %v", err)
	}
	if r4.Status != domain.RefundStatusCompleted {
		t.Fatalf("status = %q, want completed", r4.Status)
	}

	// Intent should be partially_refunded (40k of 100k).
	updated, _ := intents.FindByID(context.Background(), intent.ID)
	if updated.Status != domain.PaymentStatusPartiallyRefunded {
		t.Errorf("intent status = %q, want partially_refunded", updated.Status)
	}
	if updated.RefundedAmount != 40000 {
		t.Errorf("refunded amount = %v, want 40000", updated.RefundedAmount)
	}
}

func TestRefundService_HeadroomExceeded(t *testing.T) {
	intents := newMemIntentRepo()
	intent := &domain.PaymentIntent{
		ID:             uuid.New(),
		InvoiceID:      uuid.New(),
		Amount:         100000,
		RefundedAmount: 80000,
		Status:         domain.PaymentStatusPartiallyRefunded,
	}
	intents.rows[intent.ID] = intent

	refunds := newMemRefundRepo()
	gateways := &memGatewayRepo{}
	registry := &stubRegistry{clients: map[string]port.GatewayClient{}}
	svc := NewRefundService(refunds, intents, gateways, registry, nil)
	_, err := svc.RequestRefund(context.Background(), port.RequestRefundInput{
		PaymentIntentID: intent.ID,
		Amount:          50000, // headroom = 20k
		Reason:          "test",
	})
	if err == nil {
		t.Fatalf("expected headroom-exceeded error")
	}
	if de := derrors.As(err); de == nil || de.Code != "refund.amount_exceeds_headroom" {
		t.Fatalf("error code: %+v", err)
	}
}
