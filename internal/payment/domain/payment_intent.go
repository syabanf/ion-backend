package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PaymentStatus tracks the intent lifecycle.
//
// State machine:
//
//	created → routing → pending → succeeded
//	                          ↘ failed / expired / cancelled  (terminal)
//	succeeded → partially_refunded → refunded                  (terminal)
//
// All transitions are enforced in this file's methods; the DB CHECK
// constraint enforces the enum values. The HTTP layer never mutates
// status directly — it always calls the typed transition method so
// invalid transitions surface as Conflict errors rather than DB
// constraint failures.
type PaymentStatus string

const (
	PaymentStatusCreated            PaymentStatus = "created"
	PaymentStatusRouting            PaymentStatus = "routing"
	PaymentStatusPending            PaymentStatus = "pending"
	PaymentStatusSucceeded          PaymentStatus = "succeeded"
	PaymentStatusFailed             PaymentStatus = "failed"
	PaymentStatusExpired            PaymentStatus = "expired"
	PaymentStatusCancelled          PaymentStatus = "cancelled"
	PaymentStatusRefunded           PaymentStatus = "refunded"
	PaymentStatusPartiallyRefunded  PaymentStatus = "partially_refunded"
)

// IsTerminal reports whether a status is end-of-line. Terminal
// statuses can't be transitioned out of (the refund flow takes
// succeeded → partially_refunded but stops at refunded).
func (s PaymentStatus) IsTerminal() bool {
	switch s {
	case PaymentStatusFailed, PaymentStatusExpired,
		PaymentStatusCancelled, PaymentStatusRefunded:
		return true
	}
	return false
}

// RouteDecision is the audit snapshot of why a routing pick happened.
// Stored in `payment_intents.routing_decision` JSONB so finance
// support tooling can replay the decision after the fact.
type RouteDecision struct {
	ChosenGatewayID    uuid.UUID  `json:"chosen_gateway_id"`
	ChosenGatewayCode  string     `json:"chosen_gateway_code"`
	ConsideredCount    int        `json:"considered_count"`
	ConsideredCodes    []string   `json:"considered_codes"`
	Reason             string     `json:"reason"`
	DecidedAt          time.Time  `json:"decided_at"`
}

// PaymentIntent is one checkout attempt. One invoice can have many
// intents (a failed VA followed by a successful credit card creates
// two rows). `IdempotencyKey` is UNIQUE in the DB so a duplicate
// client retry returns the originally-issued row.
type PaymentIntent struct {
	ID                  uuid.UUID
	InvoiceID           uuid.UUID
	CustomerID          *uuid.UUID
	GatewayID           *uuid.UUID
	Amount              float64
	Currency            string
	Status              PaymentStatus
	RoutingDecision     *RouteDecision
	IdempotencyKey      *string
	ExternalPaymentRef  *string
	PaidAt              *time.Time
	ExpiredAt           *time.Time
	CancelledAt         *time.Time
	FailureCode         string
	FailureReason       string
	RefundedAmount      float64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewPaymentIntent constructs a fresh intent in the 'created' state.
// The invoice id + amount are required; everything else (gateway,
// external ref, paid_at) is populated as the intent moves through
// the state machine.
func NewPaymentIntent(invoiceID uuid.UUID, amount float64, idempotencyKey string) (*PaymentIntent, error) {
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("intent.invoice_required", "invoice_id is required")
	}
	if amount <= 0 {
		return nil, errors.Validation("intent.amount_invalid", "amount must be > 0")
	}
	now := time.Now().UTC()
	intent := &PaymentIntent{
		ID:        uuid.New(),
		InvoiceID: invoiceID,
		Amount:    amount,
		Currency:  "IDR",
		Status:    PaymentStatusCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if k := strings.TrimSpace(idempotencyKey); k != "" {
		intent.IdempotencyKey = &k
	}
	return intent, nil
}

// Route flips created → routing, attaches the chosen gateway, and
// snapshots the decision. The routing service builds the
// RouteDecision; the domain just records it.
func (i *PaymentIntent) Route(gatewayID uuid.UUID, decision RouteDecision) error {
	if i.Status != PaymentStatusCreated && i.Status != PaymentStatusRouting {
		return errors.Conflict(
			"intent.cannot_route",
			"only intents in 'created' or 'routing' state can be routed",
		)
	}
	if gatewayID == uuid.Nil {
		return errors.Validation("intent.gateway_required", "gateway_id is required to route")
	}
	i.GatewayID = &gatewayID
	i.RoutingDecision = &decision
	i.Status = PaymentStatusRouting
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkPending flips routing → pending after the gateway accepts the
// intent and issues an external reference (VA number, payment URL, …).
func (i *PaymentIntent) MarkPending(externalRef string) error {
	if i.Status != PaymentStatusRouting {
		return errors.Conflict(
			"intent.cannot_mark_pending",
			"only intents in 'routing' state can move to 'pending'",
		)
	}
	ref := strings.TrimSpace(externalRef)
	if ref == "" {
		return errors.Validation("intent.external_ref_required", "external_payment_ref is required")
	}
	i.ExternalPaymentRef = &ref
	i.Status = PaymentStatusPending
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkSucceeded flips pending → succeeded on the confirmation webhook.
// Idempotent: a duplicate webhook on an already-succeeded intent is a
// no-op (the webhook service dedups via UNIQUE before reaching here,
// but the second-line defense is still useful).
func (i *PaymentIntent) MarkSucceeded(at time.Time) error {
	if i.Status == PaymentStatusSucceeded {
		return nil
	}
	if i.Status != PaymentStatusPending {
		return errors.Conflict(
			"intent.cannot_mark_succeeded",
			"only intents in 'pending' state can succeed",
		)
	}
	at = at.UTC()
	i.PaidAt = &at
	i.Status = PaymentStatusSucceeded
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkFailed flips pending → failed on a failure webhook. Captures
// the gateway's failure code + human reason so finance can triage.
func (i *PaymentIntent) MarkFailed(code, reason string) error {
	if i.Status != PaymentStatusPending && i.Status != PaymentStatusRouting {
		return errors.Conflict(
			"intent.cannot_mark_failed",
			"only intents in 'routing' or 'pending' state can fail",
		)
	}
	i.FailureCode = strings.TrimSpace(code)
	i.FailureReason = strings.TrimSpace(reason)
	i.Status = PaymentStatusFailed
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkExpired flips pending → expired. Called by the ExpireStaleIntents
// cron when an intent passes the gateway's deadline without a webhook.
func (i *PaymentIntent) MarkExpired() error {
	if i.Status != PaymentStatusPending {
		return errors.Conflict(
			"intent.cannot_mark_expired",
			"only intents in 'pending' state can expire",
		)
	}
	now := time.Now().UTC()
	i.ExpiredAt = &now
	i.Status = PaymentStatusExpired
	i.UpdatedAt = now
	return nil
}

// MarkCancelled is the explicit user-driven cancel. Allowed from
// created / routing / pending — once an intent is succeeded the
// only way back is via Refund. Already-cancelled is a no-op.
func (i *PaymentIntent) MarkCancelled() error {
	if i.Status == PaymentStatusCancelled {
		return nil
	}
	switch i.Status {
	case PaymentStatusCreated, PaymentStatusRouting, PaymentStatusPending:
		// allowed
	default:
		return errors.Conflict(
			"intent.cannot_cancel",
			"only non-terminal pre-success intents can be cancelled",
		)
	}
	now := time.Now().UTC()
	i.CancelledAt = &now
	i.Status = PaymentStatusCancelled
	i.UpdatedAt = now
	return nil
}

// MarkPartiallyRefunded flips succeeded / partially_refunded →
// partially_refunded. The cumulative refunded amount is tracked on
// the intent; the refund service computes it from completed refund
// rows and passes it in here.
func (i *PaymentIntent) MarkPartiallyRefunded(cumulativeRefundedAmount float64) error {
	if i.Status != PaymentStatusSucceeded && i.Status != PaymentStatusPartiallyRefunded {
		return errors.Conflict(
			"intent.cannot_partially_refund",
			"only succeeded or partially-refunded intents can absorb a partial refund",
		)
	}
	if cumulativeRefundedAmount <= 0 {
		return errors.Validation("intent.refund_amount_invalid", "refunded amount must be > 0")
	}
	if cumulativeRefundedAmount >= i.Amount {
		return errors.Validation(
			"intent.refund_exceeds_amount",
			"cumulative refund must be strictly less than the intent amount; use MarkFullyRefunded for equal amounts",
		)
	}
	i.RefundedAmount = cumulativeRefundedAmount
	i.Status = PaymentStatusPartiallyRefunded
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkFullyRefunded flips succeeded / partially_refunded → refunded
// once cumulative refunds equal the intent amount.
func (i *PaymentIntent) MarkFullyRefunded() error {
	if i.Status != PaymentStatusSucceeded && i.Status != PaymentStatusPartiallyRefunded {
		return errors.Conflict(
			"intent.cannot_fully_refund",
			"only succeeded or partially-refunded intents can be fully refunded",
		)
	}
	i.RefundedAmount = i.Amount
	i.Status = PaymentStatusRefunded
	i.UpdatedAt = time.Now().UTC()
	return nil
}
