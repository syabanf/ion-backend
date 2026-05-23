package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
)

// TestRoutingService_PriorityPick covers the happy path: among two
// active gateways the lower-priority one wins.
func TestRoutingService_PriorityPick(t *testing.T) {
	s := NewRoutingService()
	intent := &domain.PaymentIntent{Amount: 100000}
	available := []domain.PaymentGateway{
		{ID: uuid.New(), Code: "midtrans", IsActive: true, Priority: 30,
			Kind: domain.GatewayKindVAAggregator,
			SupportedMethods: []string{"va_bca"}},
		{ID: uuid.New(), Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator,
			SupportedMethods: []string{"va_bca"}},
	}
	chosen, decision, err := s.ChooseGateway(context.Background(), intent, "va_bca", available)
	if err != nil {
		t.Fatalf("ChooseGateway: %v", err)
	}
	if chosen.Code != "xendit" {
		t.Errorf("chose %q, want xendit (priority 10 < 30)", chosen.Code)
	}
	if decision.ChosenGatewayCode != "xendit" {
		t.Errorf("decision code = %q, want xendit", decision.ChosenGatewayCode)
	}
	if decision.ConsideredCount != 2 {
		t.Errorf("considered_count = %d, want 2", decision.ConsideredCount)
	}
}

// TestRoutingService_InactiveFiltered ensures inactive gateways are
// dropped from the candidate list.
func TestRoutingService_InactiveFiltered(t *testing.T) {
	s := NewRoutingService()
	intent := &domain.PaymentIntent{Amount: 100000}
	available := []domain.PaymentGateway{
		{ID: uuid.New(), Code: "xendit", IsActive: false, Priority: 10,
			SupportedMethods: []string{"va_bca"}},
		{ID: uuid.New(), Code: "midtrans", IsActive: true, Priority: 30,
			SupportedMethods: []string{"va_bca"}},
	}
	chosen, _, err := s.ChooseGateway(context.Background(), intent, "va_bca", available)
	if err != nil {
		t.Fatalf("ChooseGateway: %v", err)
	}
	if chosen == nil {
		t.Fatalf("expected midtrans to be chosen (xendit inactive)")
	}
	if chosen.Code != "midtrans" {
		t.Errorf("chose %q, want midtrans", chosen.Code)
	}
}

// TestRoutingService_AmountFilter ensures gateways outside their
// min/max bracket are skipped.
func TestRoutingService_AmountFilter(t *testing.T) {
	s := NewRoutingService()
	intent := &domain.PaymentIntent{Amount: 5000000}
	min1, max1 := 0.0, 1000000.0
	available := []domain.PaymentGateway{
		{ID: uuid.New(), Code: "small_va", IsActive: true, Priority: 5,
			MinAmount: &min1, MaxAmount: &max1,
			SupportedMethods: []string{"va_bca"}},
		{ID: uuid.New(), Code: "big_va", IsActive: true, Priority: 10,
			SupportedMethods: []string{"va_bca"}},
	}
	chosen, _, err := s.ChooseGateway(context.Background(), intent, "va_bca", available)
	if err != nil {
		t.Fatalf("ChooseGateway: %v", err)
	}
	if chosen == nil || chosen.Code != "big_va" {
		t.Fatalf("expected big_va (small_va capped at 1M), got %+v", chosen)
	}
}

// TestRoutingService_NoMatch returns nil chosen + reason snapshot.
func TestRoutingService_NoMatch(t *testing.T) {
	s := NewRoutingService()
	intent := &domain.PaymentIntent{Amount: 100000}
	available := []domain.PaymentGateway{
		{ID: uuid.New(), Code: "card_only", IsActive: true, Priority: 10,
			SupportedMethods: []string{"credit_card"}},
	}
	chosen, decision, err := s.ChooseGateway(context.Background(), intent, "va_bca", available)
	if err != nil {
		t.Fatalf("ChooseGateway: %v", err)
	}
	if chosen != nil {
		t.Errorf("expected no gateway match")
	}
	if decision.Reason != "no_matching_gateway" {
		t.Errorf("reason = %q, want no_matching_gateway", decision.Reason)
	}
}

// TestRoutingService_MethodAgnostic verifies that an empty preferred
// method means "accept any supported method" — no filtering on kind.
func TestRoutingService_MethodAgnostic(t *testing.T) {
	s := NewRoutingService()
	intent := &domain.PaymentIntent{Amount: 100000}
	available := []domain.PaymentGateway{
		{ID: uuid.New(), Code: "card_only", IsActive: true, Priority: 5,
			SupportedMethods: []string{"credit_card"}},
		{ID: uuid.New(), Code: "va_only", IsActive: true, Priority: 10,
			SupportedMethods: []string{"va_bca"}},
	}
	chosen, _, err := s.ChooseGateway(context.Background(), intent, "", available)
	if err != nil {
		t.Fatalf("ChooseGateway: %v", err)
	}
	if chosen.Code != "card_only" {
		t.Errorf("chose %q, want card_only (priority 5)", chosen.Code)
	}
}
