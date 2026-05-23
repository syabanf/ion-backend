package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// IntentService implements port.IntentUseCase. It composes the intent
// + gateway repos, the routing policy, and the per-gateway clients
// from the gateway registry.
type IntentService struct {
	intents   port.PaymentIntentRepository
	gateways  port.PaymentGatewayRepository
	webhooks  port.PaymentWebhookRepository
	routing   port.RoutingPolicy
	clients   gatewayResolver
	audit     audit.Writer
}

// gatewayResolver is a slim subset of gateway.Registry — the usecase
// only ever calls Resolve, so we narrow the dependency to make tests
// easier to stub.
type gatewayResolver interface {
	Resolve(code string) (port.GatewayClient, error)
}

func NewIntentService(
	intents port.PaymentIntentRepository,
	gateways port.PaymentGatewayRepository,
	webhooks port.PaymentWebhookRepository,
	routing port.RoutingPolicy,
	clients gatewayResolver,
	auditW audit.Writer,
) *IntentService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &IntentService{
		intents:  intents,
		gateways: gateways,
		webhooks: webhooks,
		routing:  routing,
		clients:  clients,
		audit:    auditW,
	}
}

var _ port.IntentUseCase = (*IntentService)(nil)

// CreateIntent is the create-intent surface. Idempotency:
//
//   - Caller supplies an Idempotency-Key header.
//   - Domain.NewPaymentIntent builds a fresh struct.
//   - Repository's CreateOrFetchByIdempotency runs INSERT ... ON CONFLICT
//     DO NOTHING + re-fetch; the canonical row comes back regardless of
//     which path fired.
//
// A duplicate replay returns the original row WITHOUT re-running
// routing (so the same VA / payment URL / external ref keeps showing).
func (s *IntentService) CreateIntent(ctx context.Context, in port.CreateIntentInput) (*domain.PaymentIntent, error) {
	intent, err := domain.NewPaymentIntent(in.InvoiceID, in.Amount, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if in.CustomerID != nil {
		intent.CustomerID = in.CustomerID
	}
	if c := in.Currency; c != "" {
		intent.Currency = c
	}
	fresh, persisted, err := s.intents.CreateOrFetchByIdempotency(ctx, intent)
	if err != nil {
		return nil, err
	}
	if !fresh {
		// Replay — return the canonical row as-is; no side effects.
		return persisted, nil
	}

	// Fresh — try to route immediately so the caller gets the VA /
	// payment URL inline. If routing fails (no eligible gateway), we
	// leave the intent in 'created' so finance can investigate.
	routed, rerr := s.routeAndProvision(ctx, persisted, in.PreferredMethod)
	if rerr != nil {
		// Don't bubble — log via audit and return the created intent.
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:     "payment",
			RecordType: "payment.intent",
			RecordID:   persisted.ID.String(),
			After:      string(persisted.Status),
			Reason:     "intent_create_route_failed:" + rerr.Error(),
		})
		return persisted, nil
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "payment",
		RecordType: "payment.intent",
		RecordID:   routed.ID.String(),
		After:      string(routed.Status),
		Reason:     "intent_created_and_routed",
	})
	return routed, nil
}

// RouteAndPay is the explicit-retry surface. Used when a previous
// create succeeded but routing failed; the operator hits this to
// re-run the routing pass.
func (s *IntentService) RouteAndPay(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	intent, err := s.intents.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if intent.Status != domain.PaymentStatusCreated && intent.Status != domain.PaymentStatusRouting {
		return nil, derrors.Conflict(
			"intent.cannot_route_in_state",
			"intent is in state '"+string(intent.Status)+"' — only created/routing intents can be routed",
		)
	}
	return s.routeAndProvision(ctx, intent, "")
}

// routeAndProvision is the shared routing + gateway-call path. Used
// by both CreateIntent (fresh path) and RouteAndPay (explicit retry).
func (s *IntentService) routeAndProvision(
	ctx context.Context, intent *domain.PaymentIntent, preferredMethod string,
) (*domain.PaymentIntent, error) {
	available, err := s.gateways.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	chosen, decision, err := s.routing.ChooseGateway(ctx, intent, preferredMethod, available)
	if err != nil {
		return nil, err
	}
	if chosen == nil {
		return nil, derrors.Conflict(
			"intent.no_gateway",
			"no payment gateway can accept this intent",
		)
	}
	if err := intent.Route(chosen.ID, decision); err != nil {
		return nil, err
	}
	// Call the gateway client (stub-mode by default).
	client, err := s.clients.Resolve(chosen.Code)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "intent.gateway_client_missing",
			"gateway client not registered for code "+chosen.Code, err)
	}
	res, err := client.CreatePayment(ctx, port.CreatePaymentInput{
		IntentID:  intent.ID,
		InvoiceID: intent.InvoiceID,
		Amount:    intent.Amount,
		Currency:  intent.Currency,
		Method:    preferredMethod,
	})
	if err != nil {
		// Gateway said no — flag the intent as failed but persist the
		// routing decision so the audit trail captures the attempt.
		_ = intent.MarkFailed("gateway_create_error", err.Error())
		if uerr := s.intents.Update(ctx, intent); uerr != nil {
			return nil, uerr
		}
		return intent, derrors.Wrap(derrors.KindUnavailable, "intent.gateway_create_failed",
			"gateway "+chosen.Code+" rejected the payment", err)
	}
	if err := intent.MarkPending(res.ExternalRef); err != nil {
		return nil, err
	}
	if err := s.intents.Update(ctx, intent); err != nil {
		return nil, err
	}
	return intent, nil
}

// ConfirmFromWebhook is called by the WebhookService once an inbound
// payment-paid event verifies. The intent flips pending → succeeded
// (or pending → failed) based on the webhook's parsed status.
func (s *IntentService) ConfirmFromWebhook(ctx context.Context, webhookID uuid.UUID) (*domain.PaymentIntent, error) {
	wh, err := s.webhooks.FindByID(ctx, webhookID)
	if err != nil {
		return nil, err
	}
	if wh.Status != domain.WebhookStatusVerified {
		return nil, derrors.Conflict(
			"intent.webhook_not_verified",
			"webhook must be in 'verified' state before confirming an intent",
		)
	}
	if wh.PaymentIntentID == nil {
		return nil, derrors.Validation(
			"intent.webhook_intent_missing",
			"verified webhook has no payment_intent_id — confirm step needs the linked intent",
		)
	}
	intent, err := s.intents.FindByID(ctx, *wh.PaymentIntentID)
	if err != nil {
		return nil, err
	}
	// The actual status change (succeeded vs failed) is driven by the
	// caller using the webhook's parsed payload — we just guard the
	// state machine here. Return the current intent; the WebhookService
	// is the right place to apply the parsed status.
	return intent, nil
}

func (s *IntentService) CancelIntent(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	intent, err := s.intents.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(intent.Status)
	if err := intent.MarkCancelled(); err != nil {
		return nil, err
	}
	if err := s.intents.Update(ctx, intent); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "payment",
		RecordType:   "payment.intent",
		RecordID:     intent.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(intent.Status),
		Reason:       "intent_cancelled",
	})
	return intent, nil
}

// ExpireStaleIntents flips intents stuck in 'pending' past the cutoff
// to 'expired'. Returns the count of affected rows.
func (s *IntentService) ExpireStaleIntents(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	stale, err := s.intents.ListPendingOlderThan(ctx, cutoff, 500)
	if err != nil {
		return 0, err
	}
	expired := 0
	for i := range stale {
		intent := &stale[i]
		if err := intent.MarkExpired(); err != nil {
			continue
		}
		if err := s.intents.Update(ctx, intent); err != nil {
			continue
		}
		expired++
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:       "payment",
			RecordType:   "payment.intent",
			RecordID:     intent.ID.String(),
			FieldChanged: "status",
			Before:       "pending",
			After:        "expired",
			Reason:       "intent_expired_by_cron",
		})
	}
	return expired, nil
}

func (s *IntentService) GetIntent(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	return s.intents.FindByID(ctx, id)
}

func (s *IntentService) ListIntents(ctx context.Context, f port.IntentListFilter) ([]domain.PaymentIntent, int, error) {
	return s.intents.List(ctx, f)
}
