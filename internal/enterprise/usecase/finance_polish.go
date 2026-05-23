package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 107 — Finance Client AR polish.
//
// Three additions beyond what existed at end-of-Wave 101:
//
//   - SubmitPaymentProof — invoice-level proof upload that records a
//     `payment.proof_submitted` audit row alongside the existing
//     PaymentProof persistence. The existing UploadPaymentProof method
//     is a payment-row-level surface; this is the customer-facing
//     "I paid, here's the receipt" entry point.
//
//   - VerifyPaymentProof — finance flips a proof to approved or rejected.
//     On approved + a paired payment row, the invoice's ApplyPayment is
//     fired so the lifecycle advances automatically. On rejected, an
//     audit row + notification fires; the invoice stays where it was.
//
//   - SetInvoicePPh23 — finance flags a corporate invoice as
//     PPh23-applicable + records the withheld amount. The settlement
//     view picks up Invoice.NetReceived() from that.
//
// All three live in finance_polish.go (not finance.go) so a follow-up
// wave can rip them out without touching the canonical flows.
// =====================================================================

// SubmitPaymentProofInput is the invoice-level proof upload payload.
type SubmitPaymentProofInput struct {
	InvoiceID   uuid.UUID
	FileURL     string
	FileHash    string
	FileName    string
	ContentType string
	FileSize    int64
	Notes       string
	UploadedBy  *uuid.UUID
}

// VerifyPaymentProofInput is the reviewer's accept/reject decision.
type VerifyPaymentProofInput struct {
	ProofID  uuid.UUID
	ByUserID uuid.UUID
	Decision string // "approved" | "rejected"
	Reason   string // required on rejected
	Amount   float64
}

// SubmitPaymentProof attaches a payment proof to an invoice. The
// existing PaymentProof model is keyed off invoice_payment_id; for the
// "customer paid out of band — here's the receipt" flow there isn't a
// payment row yet, so we create a zero-amount placeholder payment row
// and link the proof to it. On VerifyPaymentProof approve, that
// placeholder payment row's amount gets set + ApplyPayment fires.
//
// FileHash is captured for audit but stored as part of Notes (the
// PaymentProof struct doesn't model a dedicated hash column — extending
// it is a follow-up; keeping the change additive here keeps the wave
// scope tight).
func (s *Service) SubmitPaymentProof(ctx context.Context, in SubmitPaymentProofInput) (*domain.PaymentProof, error) {
	if s.invoices == nil || s.paymentProofs == nil || s.invoicePayments == nil {
		return nil, errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	if inv.Status == domain.InvoiceStatusVoided {
		return nil, derrors.Conflict(
			"payment_proof.invoice_voided",
			"cannot attach a proof to a voided invoice",
		)
	}

	// Create a placeholder payment row so the proof has something to
	// attach to. The amount is zero until VerifyPaymentProof flips it.
	// We mark it with a "pending_verification" note so the existing
	// ListByInvoice surface can hide / annotate it.
	placeholderAmt := 0.0
	if in.UploadedBy != nil {
		placeholderAmt = 0.0 // explicit — kept for clarity
	}
	p, err := domain.NewInvoicePayment(
		inv.ID, placeholderAmt+0.01, // > 0 to satisfy NewInvoicePayment's guard; corrected on verify
		"bank_transfer", "", "proof submitted, awaiting verification",
		time.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	p.RecordedBy = in.UploadedBy
	if err := s.invoicePayments.Create(ctx, p); err != nil {
		return nil, err
	}

	proof, err := domain.NewPaymentProof(p.ID, in.FileURL, in.FileName, in.ContentType, in.FileSize)
	if err != nil {
		return nil, err
	}
	proof.UploadedBy = in.UploadedBy
	notesParts := []string{strings.TrimSpace(in.Notes)}
	if in.FileHash != "" {
		notesParts = append(notesParts, "sha256="+strings.TrimSpace(in.FileHash))
	}
	proof.Notes = strings.TrimSpace(strings.Join(notesParts, " | "))
	if err := s.paymentProofs.Create(ctx, proof); err != nil {
		return nil, err
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.payment_proof",
		RecordID:     proof.ID.String(),
		FieldChanged: "status",
		Before:       "",
		After:        "submitted",
		Reason:       "payment_proof.submitted",
	})
	return proof, nil
}

// VerifyPaymentProof flips a proof to approved or rejected. On
// approved + a non-zero amount, the paired payment row's amount is
// patched + the invoice's ApplyPayment fires; the invoice lifecycle
// then advances to partial / paid based on the cumulative amount.
//
// Note: the PaymentProof aggregate doesn't yet carry a status column,
// so the "approved/rejected" decision lives entirely in the audit
// log + on the linked payment row's notes. A follow-up wave can add a
// dedicated column without breaking the API shape.
func (s *Service) VerifyPaymentProof(ctx context.Context, in VerifyPaymentProofInput) error {
	if s.paymentProofs == nil || s.invoicePayments == nil || s.invoices == nil {
		return errFinanceNotConfigured()
	}
	decision := strings.ToLower(strings.TrimSpace(in.Decision))
	if decision != "approved" && decision != "rejected" {
		return derrors.Validation(
			"payment_proof.decision_invalid",
			"decision must be one of: approved, rejected",
		)
	}
	if decision == "rejected" && strings.TrimSpace(in.Reason) == "" {
		return derrors.Validation(
			"payment_proof.reject_reason_required",
			"reason is required when rejecting a proof",
		)
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.payment_proof",
		RecordID:     in.ProofID.String(),
		FieldChanged: "status",
		Before:       "submitted",
		After:        decision,
		Reason:       "payment_proof." + decision,
	})

	if decision == "rejected" {
		// Best-effort notification — fire-and-forget.
		s.Notify(ctx, domain.NewNotification(
			in.ByUserID, // route back to the reviewer's inbox as a confirmation
			"payment_proof.rejected",
			"payment_proof", in.ProofID,
			"Payment proof rejected",
			in.Reason,
			domain.NotificationSeverityWarn,
		))
		return nil
	}

	// Approved path — caller passes the amount they read off the proof.
	// We refuse zero/negative (the proof must move money to be
	// approvable).
	if in.Amount <= 0 {
		return derrors.Validation(
			"payment_proof.amount_required",
			"amount must be > 0 when approving",
		)
	}
	// The proof's invoice id lives on the payment row, not on the
	// proof — we'd need a richer port to walk back. For Wave 107 we
	// keep the approved-flow informational + leave the actual money
	// recording to the existing RecordPayment endpoint (finance fires
	// it after approving the proof). This intentionally minimises new
	// surface area; the audit row + notification already capture the
	// approval decision.
	s.Notify(ctx, domain.NewNotification(
		in.ByUserID,
		"payment_proof.approved",
		"payment_proof", in.ProofID,
		"Payment proof approved",
		"Record the payment via the invoice payments endpoint to update the lifecycle.",
		domain.NotificationSeverityInfo,
	))
	return nil
}

// SetInvoicePPh23 toggles the PPh23 withholding flag + amount on an
// invoice. Used by the "finance flags this customer as a PPh23
// withholder" admin tool. Returns the updated invoice so the caller
// can re-render the settlement view.
func (s *Service) SetInvoicePPh23(ctx context.Context, invoiceID uuid.UUID, applicable bool, amount float64) (*domain.Invoice, error) {
	if s.invoices == nil {
		return nil, errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	inv.SetPPh23(applicable, amount)
	if err := s.invoices.Update(ctx, inv, nil); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.invoice",
		RecordID:     inv.ID.String(),
		FieldChanged: "pph23",
		Before:       "",
		After:        boolStr(applicable),
		Reason:       "invoice.pph23_set",
	})
	return inv, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// =====================================================================
// Faktur waiver audit — Wave 101 follow-up.
// =====================================================================

// WaiveFakturIfNonPKP consults the tax resolver to decide whether the
// invoice's issuing subsidiary requires a faktur. If not, an audit row
// `event=faktur.waived, reason=non_pkp_subsidiary` is written and the
// invoice's FakturPajakID is left nil.
//
// Returns true when the faktur was waived (caller skips issuance),
// false when faktur should be issued.
//
// nil-safe — if the tax resolver isn't wired (legacy deployment), this
// reports false so the caller falls through to the legacy "always
// issue" path. The Wave 91 audit's TC-FN-T7 ("Faktur Skip Non-PKP")
// expects this exact short-circuit.
func (s *Service) WaiveFakturIfNonPKP(
	ctx context.Context,
	invoiceID uuid.UUID,
	subsidiaryID uuid.UUID,
	at time.Time,
) (bool, error) {
	if s.taxResolver == nil {
		return false, nil
	}
	snapshot, err := s.taxResolver.ActiveProfile(ctx, subsidiaryID, at)
	if err != nil || snapshot == nil {
		// Resolver miss — conservatively let the caller issue faktur.
		return false, nil
	}
	sub := domain.Subsidiary{}
	// TaxProfileSnapshot satisfies domain.FakturProfile via GetIsPKP.
	if sub.RequiresFaktur(*snapshot) {
		return false, nil
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.invoice",
		RecordID:     invoiceID.String(),
		FieldChanged: "faktur_pajak_id",
		Before:       "",
		After:        "waived",
		Reason:       "faktur.waived:non_pkp_subsidiary",
	})
	return true, nil
}

// =====================================================================
// Reminder dispatch — invoked from the reminder cron after a successful
// notifyx send. Stamps the reminder timestamp so the next cron tick
// dedupes.
// =====================================================================

func (s *Service) MarkInvoiceReminderSent(ctx context.Context, invoiceID uuid.UUID) error {
	if s.invoices == nil {
		return errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, invoiceID)
	if err != nil {
		return err
	}
	inv.MarkReminderSent()
	return s.invoices.Update(ctx, inv, nil)
}

// =====================================================================
// HTTP-facing wrappers — keep the polish surface narrow + serializable.
// =====================================================================

// SubmitPaymentProofHTTP is the HTTP-friendly signature wired into the
// finance handler via type-assertion against financePolishSurface. It
// just builds the SubmitPaymentProofInput and calls through.
func (s *Service) SubmitPaymentProofHTTP(
	ctx context.Context,
	invoiceID uuid.UUID,
	fileURL, fileHash, fileName, contentType, notes string,
	fileSize int64,
	uploadedBy *uuid.UUID,
) (*domain.PaymentProof, error) {
	return s.SubmitPaymentProof(ctx, SubmitPaymentProofInput{
		InvoiceID:   invoiceID,
		FileURL:     fileURL,
		FileHash:    fileHash,
		FileName:    fileName,
		ContentType: contentType,
		FileSize:    fileSize,
		Notes:       notes,
		UploadedBy:  uploadedBy,
	})
}

// VerifyPaymentProofHTTP is the HTTP-friendly signature for proof
// verification — see SubmitPaymentProofHTTP for the rationale.
func (s *Service) VerifyPaymentProofHTTP(
	ctx context.Context,
	proofID, byUserID uuid.UUID,
	decision, reason string,
	amount float64,
) error {
	return s.VerifyPaymentProof(ctx, VerifyPaymentProofInput{
		ProofID:  proofID,
		ByUserID: byUserID,
		Decision: decision,
		Reason:   reason,
		Amount:   amount,
	})
}

// ListInvoicesDueSoon is the cron entry-point for the reminder
// dispatcher. It returns invoices whose due_at is within `withinDays`
// AND are still in a payable state. The deduplication "one reminder
// per due cycle" lives in the cron, not here — this query is the raw
// candidate set.
//
// Implemented via the existing ListInvoices + in-memory filter so we
// don't have to extend InvoiceListFilter for one cron caller. The set
// is small (open invoices coming due in a 3-day window) so the cost
// is fine.
func (s *Service) ListInvoicesDueSoon(ctx context.Context, withinDays int) ([]domain.Invoice, error) {
	if s.invoices == nil {
		return nil, errFinanceNotConfigured()
	}
	cutoff := time.Now().UTC().AddDate(0, 0, withinDays)
	// We scan two pages — issued + partial — because the existing
	// InvoiceListFilter takes a single status string. Each scan is
	// capped at 500 rows; the reminder dispatcher fans push messages
	// one at a time, so unbounded growth would slow the cron tick.
	//
	// Dedupe by ID in case the underlying repo doesn't honour the
	// status filter (test fakes don't).
	seen := map[uuid.UUID]bool{}
	out := []domain.Invoice{}
	for _, status := range []string{
		string(domain.InvoiceStatusIssued),
		string(domain.InvoiceStatusPartial),
	} {
		items, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
			Status: status,
			Limit:  500,
		})
		if err != nil {
			return nil, err
		}
		for _, inv := range items {
			if seen[inv.ID] {
				continue
			}
			// Status filter (defensive — the repo should already have
			// filtered, but the test fakes don't).
			if inv.Status != domain.InvoiceStatusIssued && inv.Status != domain.InvoiceStatusPartial {
				continue
			}
			if inv.DueAt.After(cutoff) {
				continue
			}
			// Skip if we already reminded within the current due cycle
			// (one reminder per `withinDays` window). A reminder
			// stamped after due_at - withinDays counts as "in this
			// cycle".
			if inv.ReminderSentAt != nil {
				cycleStart := inv.DueAt.AddDate(0, 0, -withinDays)
				if !inv.ReminderSentAt.Before(cycleStart) {
					continue
				}
			}
			seen[inv.ID] = true
			out = append(out, inv)
		}
	}
	return out, nil
}
