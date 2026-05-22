// Package http — DTOs for the billing adapter.
//
// All HTTP-layer request/response shapes for billing live in this file
// (invoices, payments, cycles, commissions, referrals, terminations,
// customer portal, policy). Conversion helpers `toXxxDTO` sit next to
// their target type so a change to the wire shape touches one file
// instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/billing/invoices
//     return?").
package http

import (
	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Invoices
// =====================================================================

type lineDTO struct {
	ID          string  `json:"id"`
	LineOrder   int     `json:"line_order"`
	Description string  `json:"description"`
	ItemType    string  `json:"item_type"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	Amount      float64 `json:"amount"`
}

type invoiceDTO struct {
	ID                string       `json:"id"`
	InvoiceNumber     string       `json:"invoice_number"`
	CustomerID        string       `json:"customer_id"`
	CustomerName      string       `json:"customer_name,omitempty"`
	CustomerNumber    string       `json:"customer_number,omitempty"`
	OrderID           *string      `json:"order_id,omitempty"`
	OrderNumber       string       `json:"order_number,omitempty"`
	InvoiceType       string       `json:"invoice_type"`
	InvoiceDate       string       `json:"invoice_date"`
	DueDate           string       `json:"due_date"`
	Subtotal          float64      `json:"subtotal"`
	PPNRate           float64      `json:"ppn_rate"`
	PPNAmount         float64      `json:"ppn_amount"`
	Total             float64      `json:"total"`
	PaidAmount        float64      `json:"paid_amount"`
	OutstandingAmount float64      `json:"outstanding_amount"`
	Status            string       `json:"status"`
	PaidAt            *string      `json:"paid_at,omitempty"`
	Notes             string       `json:"notes,omitempty"`
	CreatedAt         string       `json:"created_at"`
	Lines             []lineDTO    `json:"lines,omitempty"`
	Payments          []paymentDTO `json:"payments,omitempty"`
}

func toInvoiceDTO(v port.InvoiceView) invoiceDTO {
	d := invoiceDTO{
		ID:                v.Invoice.ID.String(),
		InvoiceNumber:     v.Invoice.InvoiceNumber,
		CustomerID:        v.Invoice.CustomerID.String(),
		CustomerName:      v.CustomerName,
		CustomerNumber:    v.CustomerNumber,
		OrderNumber:       v.OrderNumber,
		InvoiceType:       string(v.Invoice.InvoiceType),
		InvoiceDate:       v.Invoice.InvoiceDate.Format("2006-01-02"),
		DueDate:           v.Invoice.DueDate.Format("2006-01-02"),
		Subtotal:          v.Invoice.Subtotal,
		PPNRate:           v.Invoice.PPNRate,
		PPNAmount:         v.Invoice.PPNAmount,
		Total:             v.Invoice.Total,
		PaidAmount:        v.PaidAmount,
		OutstandingAmount: v.OutstandingAmount,
		Status:            string(v.Invoice.Status),
		Notes:             v.Invoice.Notes,
		CreatedAt:         httpserver.FormatRFC3339(v.Invoice.CreatedAt),
	}
	if v.Invoice.OrderID != nil {
		s := v.Invoice.OrderID.String()
		d.OrderID = &s
	}
	if v.Invoice.PaidAt != nil {
		s := httpserver.FormatRFC3339(*v.Invoice.PaidAt)
		d.PaidAt = &s
	}
	for _, l := range v.Lines {
		d.Lines = append(d.Lines, lineDTO{
			ID: l.ID.String(), LineOrder: l.LineOrder,
			Description: l.Description, ItemType: l.ItemType,
			Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Amount,
		})
	}
	for _, p := range v.Payments {
		d.Payments = append(d.Payments, paymentDTO{
			ID:                   p.ID.String(),
			Amount:               p.Amount,
			PaymentMethod:        p.PaymentMethod,
			GatewayTransactionID: p.GatewayTransactionID,
			PaymentDate:          httpserver.FormatRFC3339(p.PaymentDate),
			Status:               string(p.Status),
			Notes:                p.Notes,
		})
	}
	return d
}

type createInvoiceRequest struct {
	CustomerID  string  `json:"customer_id"`
	OrderID     string  `json:"order_id,omitempty"`
	InvoiceType string  `json:"invoice_type"`
	PPNRate     float64 `json:"ppn_rate,omitempty"`
	DueDate     string  `json:"due_date,omitempty"`
	Notes       string  `json:"notes,omitempty"`
	Issue       bool    `json:"issue,omitempty"`
	Lines       []struct {
		Description string  `json:"description"`
		ItemType    string  `json:"item_type"`
		Quantity    float64 `json:"quantity,omitempty"`
		UnitPrice   float64 `json:"unit_price"`
	} `json:"lines"`
}

// =====================================================================
// Payments
// =====================================================================

type paymentDTO struct {
	ID                   string  `json:"id"`
	Amount               float64 `json:"amount"`
	PaymentMethod        string  `json:"payment_method"`
	GatewayTransactionID string  `json:"gateway_transaction_id,omitempty"`
	PaymentDate          string  `json:"payment_date"`
	Status               string  `json:"status"`
	Notes                string  `json:"notes,omitempty"`
}

type recordPaymentRequest struct {
	Amount               float64 `json:"amount"`
	PaymentMethod        string  `json:"payment_method"`
	GatewayTransactionID string  `json:"gateway_transaction_id,omitempty"`
	Notes                string  `json:"notes,omitempty"`
}

// =====================================================================
// Policy
// =====================================================================

type policyDTO struct {
	LateFeeGraceDays            int     `json:"late_fee_grace_days"`
	LateFeeAmount               float64 `json:"late_fee_amount"`
	SuspendAfterDays            int     `json:"suspend_after_days"`
	TerminateAfterSuspendedDays int     `json:"terminate_after_suspended_days"`
	NotifyCustomerDaysBefore    int     `json:"notify_customer_days_before"`
	UpdatedAt                   string  `json:"updated_at"`
}

func toPolicyDTO(p *domain.Policy) policyDTO {
	return policyDTO{
		LateFeeGraceDays:            p.LateFeeGraceDays,
		LateFeeAmount:               p.LateFeeAmount,
		SuspendAfterDays:            p.SuspendAfterDays,
		TerminateAfterSuspendedDays: p.TerminateAfterSuspendedDays,
		NotifyCustomerDaysBefore:    p.NotifyCustomerDaysBefore,
		UpdatedAt:                   httpserver.FormatRFC3339(p.UpdatedAt),
	}
}

type updatePolicyRequest struct {
	LateFeeGraceDays            *int     `json:"late_fee_grace_days,omitempty"`
	LateFeeAmount               *float64 `json:"late_fee_amount,omitempty"`
	SuspendAfterDays            *int     `json:"suspend_after_days,omitempty"`
	TerminateAfterSuspendedDays *int     `json:"terminate_after_suspended_days,omitempty"`
	NotifyCustomerDaysBefore    *int     `json:"notify_customer_days_before,omitempty"`
}

// =====================================================================
// Cycles
// =====================================================================

type cycleDTO struct {
	ID          string  `json:"id"`
	CustomerID  string  `json:"customer_id"`
	OrderID     string  `json:"order_id"`
	PeriodStart string  `json:"period_start"`
	PeriodEnd   string  `json:"period_end"`
	InvoiceID   *string `json:"invoice_id,omitempty"`
	Status      string  `json:"status"`
	Notes       string  `json:"notes,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

func toCycleDTO(c domain.BillingCycle) cycleDTO {
	d := cycleDTO{
		ID:          c.ID.String(),
		CustomerID:  c.CustomerID.String(),
		OrderID:     c.OrderID.String(),
		PeriodStart: c.PeriodStart.Format("2006-01-02"),
		PeriodEnd:   c.PeriodEnd.Format("2006-01-02"),
		Status:      string(c.Status),
		Notes:       c.Notes,
		CreatedAt:   httpserver.FormatRFC3339(c.CreatedAt),
	}
	if c.InvoiceID != nil {
		s := c.InvoiceID.String()
		d.InvoiceID = &s
	}
	return d
}

// =====================================================================
// Manual tick
// =====================================================================

type tickReportDTO struct {
	StartedAt             string   `json:"started_at"`
	CompletedAt           string   `json:"completed_at"`
	RecurringGenerated    int      `json:"recurring_generated"`
	RecurringSkipped      int      `json:"recurring_skipped"`
	LateFeesApplied       int      `json:"late_fees_applied"`
	CustomersSuspended    int      `json:"customers_suspended"`
	CustomersRestored     int      `json:"customers_restored"`
	TerminationsTriggered int      `json:"terminations_triggered"`
	Errors                []string `json:"errors,omitempty"`
}

// =====================================================================
// Commissions
// =====================================================================

type commissionDTO struct {
	ID         string  `json:"id"`
	OrderID    string  `json:"order_id"`
	CustomerID string  `json:"customer_id"`
	PartyType  string  `json:"party_type"`
	UserID     *string `json:"user_id,omitempty"`
	BranchID   *string `json:"branch_id,omitempty"`
	Amount     float64 `json:"amount"`
	Percentage float64 `json:"percentage"`
	BaseAmount float64 `json:"base_amount"`
	Notes      string  `json:"notes,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

func toCommissionDTO(c domain.CommissionRecord) commissionDTO {
	d := commissionDTO{
		ID:         c.ID.String(),
		OrderID:    c.OrderID.String(),
		CustomerID: c.CustomerID.String(),
		PartyType:  string(c.PartyType),
		Amount:     c.Amount,
		Percentage: c.Percentage,
		BaseAmount: c.BaseAmount,
		Notes:      c.Notes,
		CreatedAt:  httpserver.FormatRFC3339(c.CreatedAt),
	}
	if c.UserID != nil {
		s := c.UserID.String()
		d.UserID = &s
	}
	if c.BranchID != nil {
		s := c.BranchID.String()
		d.BranchID = &s
	}
	return d
}

// =====================================================================
// Terminations
// =====================================================================

type terminationDTO struct {
	ID                   string  `json:"id"`
	CustomerID           string  `json:"customer_id"`
	OrderID              *string `json:"order_id,omitempty"`
	Kind                 string  `json:"kind"`
	Status               string  `json:"status"`
	Reason               string  `json:"reason,omitempty"`
	RequestedByUserID    *string `json:"requested_by_user_id,omitempty"`
	FinalInvoiceID       *string `json:"final_invoice_id,omitempty"`
	PenaltyAmount        float64 `json:"penalty_amount"`
	OutstandingAtRequest float64 `json:"outstanding_at_request"`
	WOID                 *string `json:"wo_id,omitempty"`
	RequestedAt          string  `json:"requested_at"`
	CompletedAt          *string `json:"completed_at,omitempty"`
	Notes                string  `json:"notes,omitempty"`
}

func toTerminationDTO(t *domain.TerminationRequest) terminationDTO {
	d := terminationDTO{
		ID:                   t.ID.String(),
		CustomerID:           t.CustomerID.String(),
		Kind:                 string(t.Kind),
		Status:               string(t.Status),
		Reason:               t.Reason,
		PenaltyAmount:        t.PenaltyAmount,
		OutstandingAtRequest: t.OutstandingAtRequest,
		RequestedAt:          httpserver.FormatRFC3339(t.RequestedAt),
		Notes:                t.Notes,
	}
	if t.OrderID != nil {
		s := t.OrderID.String()
		d.OrderID = &s
	}
	if t.RequestedByUserID != nil {
		s := t.RequestedByUserID.String()
		d.RequestedByUserID = &s
	}
	if t.FinalInvoiceID != nil {
		s := t.FinalInvoiceID.String()
		d.FinalInvoiceID = &s
	}
	if t.WOID != nil {
		s := t.WOID.String()
		d.WOID = &s
	}
	if t.CompletedAt != nil {
		s := httpserver.FormatRFC3339(*t.CompletedAt)
		d.CompletedAt = &s
	}
	return d
}

type requestTerminationRequest struct {
	CustomerID string `json:"customer_id"`
	Reason     string `json:"reason,omitempty"`
}

type cancelTerminationRequest struct {
	Reason string `json:"reason,omitempty"`
}

// =====================================================================
// Referrals
// =====================================================================

type rewardDTO struct {
	ID                 string  `json:"id"`
	ReferralID         string  `json:"referral_id"`
	ReferrerCustomerID *string `json:"referrer_customer_id,omitempty"`
	RefereeCustomerID  string  `json:"referee_customer_id"`
	OrderID            *string `json:"order_id,omitempty"`
	InvoiceID          *string `json:"invoice_id,omitempty"`
	Amount             float64 `json:"amount"`
	Status             string  `json:"status"`
	PaidAt             *string `json:"paid_at,omitempty"`
	Notes              string  `json:"notes,omitempty"`
	CreatedAt          string  `json:"created_at"`
}

func toRewardDTO(r domain.ReferralReward) rewardDTO {
	d := rewardDTO{
		ID:                r.ID.String(),
		ReferralID:        r.ReferralID.String(),
		RefereeCustomerID: r.RefereeCustomerID.String(),
		Amount:            r.Amount,
		Status:            string(r.Status),
		Notes:             r.Notes,
		CreatedAt:         httpserver.FormatRFC3339(r.CreatedAt),
	}
	if r.ReferrerCustomerID != nil {
		s := r.ReferrerCustomerID.String()
		d.ReferrerCustomerID = &s
	}
	if r.OrderID != nil {
		s := r.OrderID.String()
		d.OrderID = &s
	}
	if r.InvoiceID != nil {
		s := r.InvoiceID.String()
		d.InvoiceID = &s
	}
	if r.PaidAt != nil {
		s := httpserver.FormatRFC3339(*r.PaidAt)
		d.PaidAt = &s
	}
	return d
}

// =====================================================================
// Portal (unauthenticated self-service)
// =====================================================================

type portalRequestOTPRequest struct {
	CustomerNumber string `json:"customer_number"`
	Phone          string `json:"phone"`
}

type portalRequestOTPResponse struct {
	ExpiresAt string `json:"expires_at"`
	// DevOTP is only set when the binary was started with the dev knob;
	// production deployments leave it empty and rely on out-of-band
	// delivery. The local dev environment surfaces it so e2e tests don't
	// need a real WhatsApp client.
	DevOTP string `json:"dev_otp,omitempty"`
}

type portalConfirmRequest struct {
	CustomerNumber string `json:"customer_number"`
	OTP            string `json:"otp"`
	Reason         string `json:"reason,omitempty"`
}

type portalConfirmResponse struct {
	TerminationID string `json:"termination_id"`
	Status        string `json:"status"`
}
