package usecase

import (
	"context"
	"math"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// RefundService implements port.RefundUseCase. Refund lifecycle:
//
//	RequestRefund    → requested
//	ApproveRefund    → approved      (records approver id + timestamp)
//	RejectRefund     → rejected      (terminal)
//	ProcessRefund    → processing    (calls gateway.RefundPayment)
//	MarkRefundCompleted → completed  (records gateway's external ref;
//	                                 also flips intent → partial/full
//	                                 refunded based on cumulative total)
type RefundService struct {
	refunds  port.RefundRepository
	intents  port.PaymentIntentRepository
	gateways port.PaymentGatewayRepository
	clients  gatewayResolver
	audit    audit.Writer
}

func NewRefundService(
	refunds port.RefundRepository,
	intents port.PaymentIntentRepository,
	gateways port.PaymentGatewayRepository,
	clients gatewayResolver,
	auditW audit.Writer,
) *RefundService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &RefundService{
		refunds:  refunds,
		intents:  intents,
		gateways: gateways,
		clients:  clients,
		audit:    auditW,
	}
}

var _ port.RefundUseCase = (*RefundService)(nil)

func (s *RefundService) RequestRefund(ctx context.Context, in port.RequestRefundInput) (*domain.Refund, error) {
	intent, err := s.intents.FindByID(ctx, in.PaymentIntentID)
	if err != nil {
		return nil, err
	}
	if intent.Status != domain.PaymentStatusSucceeded && intent.Status != domain.PaymentStatusPartiallyRefunded {
		return nil, derrors.Conflict(
			"refund.intent_not_eligible",
			"only succeeded or partially-refunded intents can be refunded (current status: "+string(intent.Status)+")",
		)
	}
	// Headroom check — cumulative refunded + this request must not
	// exceed the intent amount.
	headroom := intent.Amount - intent.RefundedAmount
	if in.Amount > headroom+0.001 { // tiny float tolerance
		return nil, derrors.Validation(
			"refund.amount_exceeds_headroom",
			"refund amount exceeds remaining refundable balance",
		)
	}
	r, err := domain.NewRefund(in.PaymentIntentID, in.Amount, in.Reason, in.RequestedBy)
	if err != nil {
		return nil, err
	}
	if err := s.refunds.Create(ctx, r); err != nil {
		return nil, err
	}
	uid := uuid.Nil
	if in.RequestedBy != nil {
		uid = *in.RequestedBy
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:     uid,
		Module:     "payment",
		RecordType: "payment.refund",
		RecordID:   r.ID.String(),
		After:      string(r.Status),
		Reason:     "refund_requested",
	})
	return r, nil
}

func (s *RefundService) ApproveRefund(ctx context.Context, id, by uuid.UUID) (*domain.Refund, error) {
	r, err := s.refunds.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(r.Status)
	if err := r.Approve(by); err != nil {
		return nil, err
	}
	if err := s.refunds.Update(ctx, r); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       by,
		Module:       "payment",
		RecordType:   "payment.refund",
		RecordID:     r.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(r.Status),
		Reason:       "refund_approved",
	})
	return r, nil
}

func (s *RefundService) RejectRefund(ctx context.Context, id uuid.UUID, reason string) (*domain.Refund, error) {
	r, err := s.refunds.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(r.Status)
	if err := r.Reject(reason); err != nil {
		return nil, err
	}
	if err := s.refunds.Update(ctx, r); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "payment",
		RecordType:   "payment.refund",
		RecordID:     r.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(r.Status),
		Reason:       "refund_rejected:" + reason,
	})
	return r, nil
}

// ProcessRefund flips approved → processing AND calls the gateway
// client's RefundPayment. The gateway's response is captured;
// completion is then driven by the gateway's webhook (which calls
// MarkRefundCompleted with the external ref).
func (s *RefundService) ProcessRefund(ctx context.Context, id uuid.UUID) (*domain.Refund, error) {
	r, err := s.refunds.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	intent, err := s.intents.FindByID(ctx, r.PaymentIntentID)
	if err != nil {
		return nil, err
	}
	if intent.GatewayID == nil {
		return nil, derrors.Validation(
			"refund.intent_has_no_gateway",
			"cannot process refund: the parent intent has no chosen gateway",
		)
	}
	gw, err := s.gateways.FindByID(ctx, *intent.GatewayID)
	if err != nil {
		return nil, err
	}
	client, err := s.clients.Resolve(gw.Code)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "refund.gateway_client_missing",
			"gateway client not registered for code "+gw.Code, err)
	}
	if err := r.StartProcessing(); err != nil {
		return nil, err
	}
	if err := s.refunds.Update(ctx, r); err != nil {
		return nil, err
	}
	res, gerr := client.RefundPayment(ctx, intent, r.Amount, r.Reason)
	if gerr != nil {
		_ = r.MarkFailed(gerr.Error())
		_ = s.refunds.Update(ctx, r)
		return r, derrors.Wrap(derrors.KindUnavailable, "refund.gateway_call_failed",
			"gateway "+gw.Code+" rejected the refund", gerr)
	}
	if res != nil && res.ExternalRef != "" {
		ref := res.ExternalRef
		r.ExternalRefundRef = &ref
		if err := s.refunds.Update(ctx, r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// MarkRefundCompleted is called by the WebhookService (or by the
// admin if the gateway doesn't issue refund webhooks) when the
// gateway confirms a refund landed. The intent's RefundedAmount is
// recomputed from completed refund rows and the intent flips to
// partial / full refunded as appropriate.
func (s *RefundService) MarkRefundCompleted(ctx context.Context, id uuid.UUID, externalRef string) (*domain.Refund, error) {
	r, err := s.refunds.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(r.Status)
	if err := r.MarkCompleted(externalRef); err != nil {
		return nil, err
	}
	if err := s.refunds.Update(ctx, r); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "payment",
		RecordType:   "payment.refund",
		RecordID:     r.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(r.Status),
		Reason:       "refund_completed",
	})

	// Recompute the intent's cumulative refunded amount and flip its
	// state accordingly.
	intent, err := s.intents.FindByID(ctx, r.PaymentIntentID)
	if err != nil {
		return r, nil
	}
	cumulative, err := s.refunds.SumCompletedForIntent(ctx, r.PaymentIntentID)
	if err != nil {
		return r, nil
	}
	if math.Abs(cumulative-intent.Amount) < 0.001 {
		if err := intent.MarkFullyRefunded(); err == nil {
			_ = s.intents.Update(ctx, intent)
			audit.SafeWrite(ctx, s.audit, audit.Entry{
				Module:       "payment",
				RecordType:   "payment.intent",
				RecordID:     intent.ID.String(),
				FieldChanged: "status",
				Before:       "succeeded/partially_refunded",
				After:        string(intent.Status),
				Reason:       "intent_fully_refunded",
			})
		}
	} else if cumulative > 0 {
		if err := intent.MarkPartiallyRefunded(cumulative); err == nil {
			_ = s.intents.Update(ctx, intent)
			audit.SafeWrite(ctx, s.audit, audit.Entry{
				Module:       "payment",
				RecordType:   "payment.intent",
				RecordID:     intent.ID.String(),
				FieldChanged: "status",
				Before:       "succeeded",
				After:        string(intent.Status),
				Reason:       "intent_partially_refunded",
			})
		}
	}
	return r, nil
}

func (s *RefundService) GetRefund(ctx context.Context, id uuid.UUID) (*domain.Refund, error) {
	return s.refunds.FindByID(ctx, id)
}

func (s *RefundService) ListRefunds(ctx context.Context, f port.RefundListFilter) ([]domain.Refund, int, error) {
	return s.refunds.List(ctx, f)
}
