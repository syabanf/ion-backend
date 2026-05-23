package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// InvoiceStatus is the canonical lifecycle of an enterprise invoice.
//
//   draft   -> issued (auto on create at MVP)
//   issued  -> partial (on first payment that doesn't clear the bill)
//   issued  -> paid    (on payment that clears the bill in full)
//   partial -> paid    (on payment that closes the remaining balance)
//   any non-paid -> voided (with a reason; voided is terminal)
type InvoiceStatus string

const (
	InvoiceStatusDraft   InvoiceStatus = "draft"
	InvoiceStatusIssued  InvoiceStatus = "issued"
	InvoiceStatusPartial InvoiceStatus = "partial"
	InvoiceStatusPaid    InvoiceStatus = "paid"
	InvoiceStatusVoided  InvoiceStatus = "voided"
)

type Invoice struct {
	ID            uuid.UUID
	InvoiceNumber string
	QuotationID   uuid.UUID
	OpportunityID uuid.UUID
	BOQVersionID  uuid.UUID
	Status        InvoiceStatus
	// TotalAmount is the GRAND total (subtotal + tax). Customer pays
	// this. SubtotalAmount + TaxPct + TaxAmount are the breakdown
	// surfaced for invoice rendering + DJP e-Faktur integration later.
	TotalAmount    float64
	SubtotalAmount float64
	TaxPct         float64
	TaxAmount      float64
	PaidAmount     float64
	Currency       string
	IssuedAt       time.Time
	DueAt          time.Time
	PaidAt         *time.Time
	VoidedAt       *time.Time
	VoidReason     string
	Notes          string
	IssuedBy       *uuid.UUID
	// Phase 5 E5 — optional linkage to a termin schedule. Null when the
	// invoice was issued directly without a plan.
	InvoicePlanID     *uuid.UUID
	InvoicePlanItemID *uuid.UUID
	Revision          int
	CreatedAt         time.Time
	UpdatedAt         time.Time

	// Wave 101 — tax snapshot chain. Copied from the source quotation
	// (which itself inherits from the BOQ); pinned at invoice creation.
	// FakturPajakID is the backlink to tax.faktur_pajak_records.id when
	// faktur was actually issued; nil when waived (Non-PKP path).
	TaxSnapshotHash *string
	FakturPajakID   *uuid.UUID

	// Wave 107 — Finance Client AR polish.
	//
	// ReminderSentAt is the timestamp of the last reminder email sent
	// by the reminder cron. We use it to gate "one reminder per due
	// cycle" (skip if reminder_sent_at >= due_at - 3 days).
	//
	// PPh23WithheldAmount / IsPPh23Applicable model the Indonesian
	// withholding tax: corporate customers often keep 2% of the
	// service-portion total. When applicable, the settlement view
	// computes net_received as TotalAmount - PPh23WithheldAmount.
	// Both columns ship with safe defaults (0 / false) so legacy rows
	// continue to behave exactly as before migration 0073.
	ReminderSentAt      *time.Time
	PPh23WithheldAmount float64
	IsPPh23Applicable   bool
}

// MarkReminderSent stamps the reminder timestamp. Called by the
// reminder cron after a successful notifyx dispatch. No side effect on
// the invoice status — this is purely an audit + dedupe field.
func (i *Invoice) MarkReminderSent() {
	now := time.Now().UTC()
	i.ReminderSentAt = &now
	i.UpdatedAt = now
}

// SetPPh23 toggles the PPh23 withholding flag + amount. Amount may be
// zero (a flag-only set) so finance can mark "applicable, computed
// later". Negative amounts are clamped to 0 as a safety net.
func (i *Invoice) SetPPh23(applicable bool, withheldAmount float64) {
	if withheldAmount < 0 {
		withheldAmount = 0
	}
	i.IsPPh23Applicable = applicable
	if applicable {
		i.PPh23WithheldAmount = withheldAmount
	} else {
		i.PPh23WithheldAmount = 0
	}
	i.UpdatedAt = time.Now().UTC()
}

// NetReceived is the cash actually deposited after PPh23 withholding.
// When PPh23 isn't applicable, equals TotalAmount.
func (i *Invoice) NetReceived() float64 {
	if !i.IsPPh23Applicable {
		return i.TotalAmount
	}
	return i.TotalAmount - i.PPh23WithheldAmount
}

// InheritTaxSnapshot copies the quotation's tax_snapshot_hash onto the
// invoice. Mirrors Quotation.InheritTaxSnapshot — conflict if the
// invoice already carries a different value.
func (i *Invoice) InheritTaxSnapshot(hash string) error {
	if hash == "" {
		return nil
	}
	if i.TaxSnapshotHash != nil && *i.TaxSnapshotHash != "" && *i.TaxSnapshotHash != hash {
		return derrors.Conflict(
			"tax_snapshot.mismatch",
			"invoice tax_snapshot_hash already set to a different value — possible quotation drift",
		)
	}
	h := hash
	i.TaxSnapshotHash = &h
	return nil
}

// NewInvoice builds the header row from a quotation snapshot. The
// caller (usecase) provides the totals + due window because the
// invoice is a sealed snapshot at issue time — a later quotation
// revision MUST NOT mutate this invoice.
//
// Tax breakdown (E6): if `subtotal` is 0 we fall back to legacy mode
// where `totalAmount` is the whole bill and tax fields stay 0. When
// `subtotal` > 0 we treat `totalAmount` as subtotal + tax and persist
// the breakdown — that's the path Phase 5 callers use.
func NewInvoice(
	quotationID, opportunityID, boqVersionID uuid.UUID,
	invoiceNumber string,
	totalAmount, subtotal, taxPct float64,
	currency string,
	due time.Time,
) (*Invoice, error) {
	if totalAmount <= 0 {
		return nil, derrors.Validation(
			"invoice.total_must_be_positive",
			"invoice total must be > 0",
		)
	}
	if invoiceNumber == "" {
		return nil, derrors.Validation(
			"invoice.invoice_number_required",
			"invoice_number is required",
		)
	}
	if currency == "" {
		currency = "IDR"
	}
	if taxPct < 0 {
		taxPct = 0
	}
	taxAmount := 0.0
	if subtotal > 0 {
		taxAmount = subtotal * (taxPct / 100.0)
	}
	now := time.Now().UTC()
	return &Invoice{
		ID:             uuid.New(),
		InvoiceNumber:  invoiceNumber,
		QuotationID:    quotationID,
		OpportunityID:  opportunityID,
		BOQVersionID:   boqVersionID,
		Status:         InvoiceStatusIssued,
		TotalAmount:    totalAmount,
		SubtotalAmount: subtotal,
		TaxPct:         taxPct,
		TaxAmount:      taxAmount,
		PaidAmount:     0,
		Currency:       currency,
		IssuedAt:       now,
		DueAt:          due,
		Revision:       1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// ApplyPayment mutates the invoice's paid_amount + status given a
// new payment amount. Returns an error if the payment would push the
// total over the invoice cap or the invoice is in a non-payable state.
func (i *Invoice) ApplyPayment(amount float64) error {
	if i.Status == InvoiceStatusVoided {
		return derrors.Conflict(
			"invoice.voided",
			"voided invoices do not accept payments",
		)
	}
	if i.Status == InvoiceStatusPaid {
		return derrors.Conflict(
			"invoice.already_paid",
			"this invoice is already settled",
		)
	}
	if amount <= 0 {
		return derrors.Validation(
			"invoice.payment_amount_invalid",
			"payment amount must be > 0",
		)
	}
	newPaid := i.PaidAmount + amount
	if newPaid > i.TotalAmount+0.005 {
		return derrors.Validation(
			"invoice.payment_exceeds_balance",
			"payment exceeds the remaining invoice balance",
		)
	}
	i.PaidAmount = newPaid
	now := time.Now().UTC()
	if i.TotalAmount-i.PaidAmount <= 0.005 {
		i.Status = InvoiceStatusPaid
		i.PaidAt = &now
	} else {
		i.Status = InvoiceStatusPartial
	}
	return nil
}

// Void flips an invoice to voided with a recorded reason. Allowed
// from any non-paid, non-voided state.
func (i *Invoice) Void(reason string) error {
	if i.Status == InvoiceStatusVoided {
		return derrors.Conflict(
			"invoice.already_voided",
			"this invoice is already voided",
		)
	}
	if i.Status == InvoiceStatusPaid {
		return derrors.Conflict(
			"invoice.paid_invoice_void",
			"paid invoices cannot be voided — issue a refund instead",
		)
	}
	if reason == "" {
		return derrors.Validation(
			"invoice.void_reason_required",
			"void reason is required",
		)
	}
	now := time.Now().UTC()
	i.Status = InvoiceStatusVoided
	i.VoidedAt = &now
	i.VoidReason = reason
	return nil
}

// Balance is total - paid; helpful for the FE and for sweepers.
func (i *Invoice) Balance() float64 {
	return i.TotalAmount - i.PaidAmount
}

// =====================================================================
// InvoicePayment — append-only ledger row
// =====================================================================

type PaymentMethod string

const (
	PaymentMethodBankTransfer PaymentMethod = "bank_transfer"
	PaymentMethodCash         PaymentMethod = "cash"
	PaymentMethodCheck        PaymentMethod = "check"
	PaymentMethodCard         PaymentMethod = "card"
	PaymentMethodOther        PaymentMethod = "other"
)

func ValidPaymentMethod(m string) bool {
	switch PaymentMethod(m) {
	case PaymentMethodBankTransfer, PaymentMethodCash, PaymentMethodCheck,
		PaymentMethodCard, PaymentMethodOther:
		return true
	}
	return false
}

type InvoicePayment struct {
	ID         uuid.UUID
	InvoiceID  uuid.UUID
	Amount     float64
	Method     PaymentMethod
	Reference  string
	PaidAt     time.Time
	Notes      string
	RecordedBy *uuid.UUID
	CreatedAt  time.Time
}

func NewInvoicePayment(
	invoiceID uuid.UUID,
	amount float64,
	method, reference, notes string,
	paidAt time.Time,
) (*InvoicePayment, error) {
	if amount <= 0 {
		return nil, derrors.Validation(
			"invoice_payment.amount_invalid",
			"payment amount must be > 0",
		)
	}
	if !ValidPaymentMethod(method) {
		return nil, derrors.Validation(
			"invoice_payment.method_invalid",
			fmt.Sprintf("method %q is not supported", method),
		)
	}
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	}
	return &InvoicePayment{
		ID:        uuid.New(),
		InvoiceID: invoiceID,
		Amount:    amount,
		Method:    PaymentMethod(method),
		Reference: reference,
		Notes:     notes,
		PaidAt:    paidAt,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// =====================================================================
// Numbering helper — INV-YYYYMMDD-<short>
// =====================================================================

func GenerateInvoiceNumber(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("INV-%s-%s",
		now.UTC().Format("20060102"),
		uuid.New().String()[:8],
	)
}
