// Package usecase wires the payment bounded context's domain rules to
// the adapter layer. Same conventions as the other contexts:
//   - One service struct per aggregate.
//   - Use-case constructors take repository interfaces (driven ports).
//   - Audit emission for every state transition via pkg/audit.
//   - No HTTP / pgx imports — those live in the adapter layer.
package usecase

import (
	"context"
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// RoutingService is the default port.RoutingPolicy implementation.
// Strategy (kept simple for Wave 111; per-tenant overrides land later):
//
//  1. Filter the candidate list by `is_active == true`.
//  2. Filter by `MatchesAmount(amount)` — gateways outside their min/max
//     bracket are dropped.
//  3. Filter by `SupportsMethod(preferredMethod)` when the caller asked
//     for a specific rail; otherwise accept all kinds.
//  4. Sort by priority ascending (lower = preferred).
//  5. Pick the head of the list.
//
// The decision (which gateways were considered, why the chosen one
// won) is recorded in domain.RouteDecision so finance can audit it
// later. The audit lives on the intent's routing_decision JSONB
// column, not in identity.audit_logs — it's per-intent telemetry, not
// a config change.
type RoutingService struct{}

func NewRoutingService() *RoutingService { return &RoutingService{} }

var _ port.RoutingPolicy = (*RoutingService)(nil)

func (s *RoutingService) ChooseGateway(
	ctx context.Context,
	intent *domain.PaymentIntent,
	preferredMethod string,
	available []domain.PaymentGateway,
) (*domain.PaymentGateway, domain.RouteDecision, error) {
	considered := []string{}
	var candidates []domain.PaymentGateway

	for _, g := range available {
		considered = append(considered, g.Code)
		if !g.IsActive {
			continue
		}
		if !g.MatchesAmount(intent.Amount) {
			continue
		}
		if preferredMethod != "" && !g.SupportsMethod(preferredMethod) {
			continue
		}
		candidates = append(candidates, g)
	}

	if len(candidates) == 0 {
		return nil, domain.RouteDecision{
			ConsideredCount: len(considered),
			ConsideredCodes: considered,
			Reason:          "no_matching_gateway",
			DecidedAt:       time.Now().UTC(),
		}, nil
	}

	// Sort candidates by priority ascending. Use a stable insertion-sort
	// so repeated routes for the same intent are deterministic.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].Priority < candidates[j-1].Priority; j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
	chosen := candidates[0]
	reason := "priority_pick"
	if preferredMethod != "" {
		reason = "priority_pick_with_method:" + preferredMethod
	}
	return &chosen, domain.RouteDecision{
		ChosenGatewayID:   chosen.ID,
		ChosenGatewayCode: chosen.Code,
		ConsideredCount:   len(considered),
		ConsideredCodes:   considered,
		Reason:            reason,
		DecidedAt:         time.Now().UTC(),
	}, nil
}
