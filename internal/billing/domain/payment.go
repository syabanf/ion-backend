package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusConfirmed PaymentStatus = "confirmed"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusRefunded  PaymentStatus = "refunded"
)

type Payment struct {
	ID                   uuid.UUID
	InvoiceID            uuid.UUID
	CustomerID           uuid.UUID
	Amount               float64
	PaymentMethod        string
	GatewayTransactionID string
	PaymentDate          time.Time
	ConfirmedBy          *uuid.UUID
	Status               PaymentStatus
	Notes                string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewPayment constructs a confirmed payment by default. Round-1 use-case is
// manual finance staff entry, where "confirmed" is the only state that
// matters. The Xendit webhook in round 2 will start in 'pending' until
// the signature verifier flips it.
func NewPayment(invoiceID, customerID uuid.UUID, amount float64, method string, confirmedBy *uuid.UUID, notes string) (*Payment, error) {
	method = strings.TrimSpace(method)
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("payment.invoice_required", "invoice_id is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("payment.customer_required", "customer_id is required")
	}
	if amount <= 0 {
		return nil, errors.Validation("payment.amount_invalid", "amount must be > 0")
	}
	if method == "" {
		return nil, errors.Validation("payment.method_required", "payment_method is required")
	}
	now := time.Now().UTC()
	return &Payment{
		ID:            uuid.New(),
		InvoiceID:     invoiceID,
		CustomerID:    customerID,
		Amount:        amount,
		PaymentMethod: method,
		PaymentDate:   now,
		ConfirmedBy:   confirmedBy,
		Status:        PaymentStatusConfirmed,
		Notes:         notes,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}
