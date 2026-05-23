package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WebhookService implements port.WebhookUseCase. Ingest is the entry
// point — one call per inbound HTTP POST from a gateway. The flow:
//
//  1. Resolve the gateway by code (404 if unknown).
//  2. Resolve the GatewayClient (stub-mode by default) so we can
//     VerifySignature + ParseWebhook with the right per-vendor logic.
//  3. Look up the event id (gateway-supplied; SHA-256 the body if none).
//  4. INSERT ... ON CONFLICT DO NOTHING on (gateway_id, external_event_id).
//     A conflict means we've already processed this delivery → flip
//     the in-memory webhook to Duplicate and return WITHOUT replaying
//     side effects.
//  5. Verify signature → flip to Verified or Suspect.
//  6. Parse the body → if it carries a paid status, look up the intent
//     by external_payment_ref and flip the intent's state machine.
//  7. Persist final webhook state with Update.
//
// Every state flip is recorded via audit.SafeWrite so finance can
// audit "did we drop a real webhook?" after the fact.
type WebhookService struct {
	webhooks port.PaymentWebhookRepository
	intents  port.PaymentIntentRepository
	gateways port.PaymentGatewayRepository
	clients  gatewayResolver
	audit    audit.Writer
}

func NewWebhookService(
	webhooks port.PaymentWebhookRepository,
	intents port.PaymentIntentRepository,
	gateways port.PaymentGatewayRepository,
	clients gatewayResolver,
	auditW audit.Writer,
) *WebhookService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &WebhookService{
		webhooks: webhooks,
		intents:  intents,
		gateways: gateways,
		clients:  clients,
		audit:    auditW,
	}
}

var _ port.WebhookUseCase = (*WebhookService)(nil)

func (s *WebhookService) Ingest(ctx context.Context, in port.WebhookIngestInput) (*domain.PaymentWebhook, error) {
	gw, err := s.gateways.FindByCode(ctx, in.GatewayCode)
	if err != nil {
		return nil, err
	}
	client, err := s.clients.Resolve(in.GatewayCode)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "webhook.gateway_client_missing",
			"gateway client not registered", err)
	}

	// Event id — use the gateway-supplied one (from header / payload)
	// or fall back to SHA-256(body) so two identical retries still
	// collapse onto the same row.
	eventID := in.EventID
	if eventID == "" {
		sum := sha256.Sum256(in.Payload)
		eventID = hex.EncodeToString(sum[:])
	}

	wh, err := domain.NewPaymentWebhook(gw.ID, eventID, in.Payload)
	if err != nil {
		return nil, err
	}
	fresh, persisted, err := s.webhooks.CreateOrFetchByDedup(ctx, wh)
	if err != nil {
		return nil, err
	}
	if !fresh {
		// Re-delivery — mark the in-memory copy as Duplicate so the
		// HTTP handler can return 200 with a duplicate flag. We don't
		// touch the persisted row because its status is already
		// terminal.
		persisted.MarkDuplicate()
		return persisted, nil
	}

	// Verify signature.
	if !client.VerifySignature(in.Payload, in.Signature) {
		persisted.MarkSuspect("signature_invalid")
		if err := s.webhooks.Update(ctx, persisted); err != nil {
			return nil, err
		}
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:     "payment",
			RecordType: "payment.webhook",
			RecordID:   persisted.ID.String(),
			After:      string(persisted.Status),
			Reason:     "webhook_signature_invalid",
		})
		return persisted, nil
	}
	if err := persisted.MarkVerified(); err != nil {
		return nil, err
	}

	parsed, perr := client.ParseWebhook(in.Payload)
	if perr != nil {
		persisted.MarkFailed("parse_error:" + perr.Error())
		_ = s.webhooks.Update(ctx, persisted)
		return persisted, nil
	}

	// Link the intent if we can resolve it by external_payment_ref.
	var intentPtr *uuid.UUID
	if parsed.ExternalPaymentRef != "" {
		intent, lerr := s.intents.FindByExternalRef(ctx, parsed.ExternalPaymentRef)
		if lerr == nil {
			id := intent.ID
			intentPtr = &id
			if err := s.applyParsedStatus(ctx, intent, parsed); err != nil {
				persisted.MarkFailed("intent_apply_error:" + err.Error())
				_ = s.webhooks.Update(ctx, persisted)
				return persisted, nil
			}
		}
	}
	if err := persisted.MarkProcessed(intentPtr); err != nil {
		return nil, err
	}
	if err := s.webhooks.Update(ctx, persisted); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "payment",
		RecordType: "payment.webhook",
		RecordID:   persisted.ID.String(),
		After:      string(persisted.Status),
		Reason:     "webhook_processed",
	})
	return persisted, nil
}

// applyParsedStatus drives the intent's state machine from the parsed
// webhook payload. Each terminal status has its own dedicated method
// on the intent so invalid transitions surface as Conflict errors.
func (s *WebhookService) applyParsedStatus(ctx context.Context, intent *domain.PaymentIntent, parsed port.ParsedWebhook) error {
	before := string(intent.Status)
	switch parsed.NewStatus {
	case domain.PaymentStatusSucceeded:
		at := time.Now().UTC()
		if parsed.PaidAt != nil {
			at = *parsed.PaidAt
		}
		if err := intent.MarkSucceeded(at); err != nil {
			return err
		}
	case domain.PaymentStatusFailed:
		if err := intent.MarkFailed(parsed.FailureCode, parsed.FailureReason); err != nil {
			return err
		}
	case domain.PaymentStatusExpired:
		if err := intent.MarkExpired(); err != nil {
			return err
		}
	default:
		// Unrecognised — leave the intent alone (the webhook still
		// transitions to processed for the audit trail).
		return nil
	}
	if err := s.intents.Update(ctx, intent); err != nil {
		return err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "payment",
		RecordType:   "payment.intent",
		RecordID:     intent.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(intent.Status),
		Reason:       "intent_status_from_webhook",
	})
	return nil
}

func (s *WebhookService) GetWebhook(ctx context.Context, id uuid.UUID) (*domain.PaymentWebhook, error) {
	return s.webhooks.FindByID(ctx, id)
}
