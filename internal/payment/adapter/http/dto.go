package http

import (
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
)

// =====================================================================
// Intent DTO
// =====================================================================

type intentDTO struct {
	ID                 string         `json:"id"`
	InvoiceID          string         `json:"invoice_id"`
	CustomerID         *string        `json:"customer_id,omitempty"`
	GatewayID          *string        `json:"gateway_id,omitempty"`
	Amount             float64        `json:"amount"`
	Currency           string         `json:"currency"`
	Status             string         `json:"status"`
	RoutingDecision    *routeDecisionDTO `json:"routing_decision,omitempty"`
	IdempotencyKey     *string        `json:"idempotency_key,omitempty"`
	ExternalPaymentRef *string        `json:"external_payment_ref,omitempty"`
	PaidAt             *string        `json:"paid_at,omitempty"`
	ExpiredAt          *string        `json:"expired_at,omitempty"`
	CancelledAt        *string        `json:"cancelled_at,omitempty"`
	FailureCode        string         `json:"failure_code,omitempty"`
	FailureReason      string         `json:"failure_reason,omitempty"`
	RefundedAmount     float64        `json:"refunded_amount"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
}

type routeDecisionDTO struct {
	ChosenGatewayID   string   `json:"chosen_gateway_id,omitempty"`
	ChosenGatewayCode string   `json:"chosen_gateway_code,omitempty"`
	ConsideredCount   int      `json:"considered_count"`
	ConsideredCodes   []string `json:"considered_codes,omitempty"`
	Reason            string   `json:"reason"`
	DecidedAt         string   `json:"decided_at"`
}

func toIntentDTO(i domain.PaymentIntent) intentDTO {
	dto := intentDTO{
		ID:                 i.ID.String(),
		InvoiceID:          i.InvoiceID.String(),
		Amount:             i.Amount,
		Currency:           i.Currency,
		Status:             string(i.Status),
		IdempotencyKey:     i.IdempotencyKey,
		ExternalPaymentRef: i.ExternalPaymentRef,
		PaidAt:             rfc3339Ptr(i.PaidAt),
		ExpiredAt:          rfc3339Ptr(i.ExpiredAt),
		CancelledAt:        rfc3339Ptr(i.CancelledAt),
		FailureCode:        i.FailureCode,
		FailureReason:      i.FailureReason,
		RefundedAmount:     i.RefundedAmount,
		CreatedAt:          i.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          i.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if i.CustomerID != nil {
		dto.CustomerID = uuidPtrString(i.CustomerID)
	}
	if i.GatewayID != nil {
		dto.GatewayID = uuidPtrString(i.GatewayID)
	}
	if i.RoutingDecision != nil {
		d := i.RoutingDecision
		dto.RoutingDecision = &routeDecisionDTO{
			ChosenGatewayID:   nonNilUUID(d.ChosenGatewayID),
			ChosenGatewayCode: d.ChosenGatewayCode,
			ConsideredCount:   d.ConsideredCount,
			ConsideredCodes:   d.ConsideredCodes,
			Reason:            d.Reason,
			DecidedAt:         d.DecidedAt.UTC().Format(time.RFC3339),
		}
	}
	return dto
}

// =====================================================================
// Refund DTO
// =====================================================================

type refundDTO struct {
	ID                string  `json:"id"`
	PaymentIntentID   string  `json:"payment_intent_id"`
	Amount            float64 `json:"amount"`
	Reason            string  `json:"reason,omitempty"`
	Status            string  `json:"status"`
	ExternalRefundRef *string `json:"external_refund_ref,omitempty"`
	RequestedBy       *string `json:"requested_by,omitempty"`
	ApprovedBy        *string `json:"approved_by,omitempty"`
	ApprovedAt        *string `json:"approved_at,omitempty"`
	CompletedAt       *string `json:"completed_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toRefundDTO(r domain.Refund) refundDTO {
	return refundDTO{
		ID:                r.ID.String(),
		PaymentIntentID:   r.PaymentIntentID.String(),
		Amount:            r.Amount,
		Reason:            r.Reason,
		Status:            string(r.Status),
		ExternalRefundRef: r.ExternalRefundRef,
		RequestedBy:       uuidPtrString(r.RequestedBy),
		ApprovedBy:        uuidPtrString(r.ApprovedBy),
		ApprovedAt:        rfc3339Ptr(r.ApprovedAt),
		CompletedAt:       rfc3339Ptr(r.CompletedAt),
		CreatedAt:         r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Gateway DTO
// =====================================================================

type gatewayDTO struct {
	ID               string   `json:"id"`
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	IsActive         bool     `json:"is_active"`
	Priority         int      `json:"priority"`
	SupportedMethods []string `json:"supported_methods"`
	MinAmount        *float64 `json:"min_amount,omitempty"`
	MaxAmount        *float64 `json:"max_amount,omitempty"`
}

func toGatewayDTO(g domain.PaymentGateway) gatewayDTO {
	return gatewayDTO{
		ID:               g.ID.String(),
		Code:             g.Code,
		Name:             g.Name,
		Kind:             string(g.Kind),
		IsActive:         g.IsActive,
		Priority:         g.Priority,
		SupportedMethods: g.SupportedMethods,
		MinAmount:        g.MinAmount,
		MaxAmount:        g.MaxAmount,
	}
}

// =====================================================================
// H2H DTO
// =====================================================================

type h2hStatementDTO struct {
	ID              string  `json:"id"`
	GatewayID       string  `json:"gateway_id"`
	StatementDate   *string `json:"statement_date,omitempty"`
	RawFilename     string  `json:"raw_filename"`
	RawHash         string  `json:"raw_hash"`
	LineCount       int     `json:"line_count"`
	MatchedCount    int     `json:"matched_count"`
	UnmatchedCount  int     `json:"unmatched_count"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
	CompletedAt     *string `json:"completed_at,omitempty"`
}

func toH2HStatementDTO(s domain.H2HBankStatement) h2hStatementDTO {
	dto := h2hStatementDTO{
		ID:             s.ID.String(),
		GatewayID:      s.GatewayID.String(),
		RawFilename:    s.RawFilename,
		RawHash:        s.RawHash,
		LineCount:      s.LineCount,
		MatchedCount:   s.MatchedCount,
		UnmatchedCount: s.UnmatchedCount,
		Status:         string(s.Status),
		CreatedAt:      s.CreatedAt.UTC().Format(time.RFC3339),
		CompletedAt:    rfc3339Ptr(s.CompletedAt),
	}
	if s.StatementDate != nil {
		v := s.StatementDate.UTC().Format("2006-01-02")
		dto.StatementDate = &v
	}
	return dto
}

// =====================================================================
// Request bodies
// =====================================================================

type createIntentRequest struct {
	InvoiceID       string  `json:"invoice_id"`
	CustomerID      string  `json:"customer_id,omitempty"`
	Amount          float64 `json:"amount"`
	Currency        string  `json:"currency,omitempty"`
	IdempotencyKey  string  `json:"idempotency_key,omitempty"`
	PreferredMethod string  `json:"preferred_method,omitempty"`
}

type requestRefundRequest struct {
	PaymentIntentID string  `json:"payment_intent_id"`
	Amount          float64 `json:"amount"`
	Reason          string  `json:"reason,omitempty"`
}

type rejectRefundRequest struct {
	Reason string `json:"reason"`
}

// =====================================================================
// Helpers
// =====================================================================

func nonNilUUID(s any) string {
	switch v := s.(type) {
	case string:
		return v
	default:
		if v == nil {
			return ""
		}
		// uuid.UUID has a Stringer
		if str, ok := s.(interface{ String() string }); ok {
			return str.String()
		}
		return ""
	}
}
