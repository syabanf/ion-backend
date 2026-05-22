package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/pdf"
)

// Compile-time check that Service satisfies the QuotationUseCase.
var _ port.QuotationUseCase = (*Service)(nil)

// WithQuotations attaches the Phase-4a quotation repo. Optional builder
// so a deployment without quotations (test envs etc.) still compiles —
// the methods surface a clean "not configured" error in that case.
func (s *Service) WithQuotations(q port.QuotationRepository) *Service {
	s.quotations = q
	return s
}

func errQuotationNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "quotation.not_configured",
		"quotation surface is not configured for this service", nil)
}

// =====================================================================
// Generation — manual + auto-on-approve
// =====================================================================

// GenerateQuotation renders + persists a Quotation row for an approved
// BOQ. Decides v1 vs v(N+1) by looking up the latest existing
// quotation for the same BOQ:
//
//   - No prior quote → v1 with a fresh quotation_number
//   - Prior quote exists → v(N+1) reusing the same quotation_number;
//     the prior version is flipped to superseded in the same tx.
//
// The PDF bytes are rendered IN-PROCESS by pkg/pdf and stored in the
// DB along with their SHA-256 hash. Determinism is enforced by
// passing the row's IssuedAt timestamp into the renderer (gofpdf's
// CreationDate would otherwise default to time.Now() and break
// repeatable hashes).
func (s *Service) GenerateQuotation(ctx context.Context, in port.GenerateQuotationInput) (*domain.Quotation, error) {
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	if s.boqs == nil {
		return nil, errBOQNotConfigured()
	}

	// 1. Load BOQ + lines (must be approved).
	boq, err := s.boqs.FindByID(ctx, in.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if boq.Status != domain.BOQStatusApproved {
		return nil, derrors.Conflict(
			"quotation.boq_not_approved",
			"quotation can only be generated for approved BOQs",
		)
	}
	lines, err := s.boqLines.ListByBOQ(ctx, boq.ID)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, derrors.Validation("quotation.no_lines", "BOQ has no lines to quote")
	}

	// 2. Load opportunity (for account name + PIC fields baked into the PDF).
	op, err := s.opps.FindByID(ctx, boq.OpportunityID)
	if err != nil {
		return nil, err
	}

	// 3. Decide v1 vs v(N+1). Prior quote → supersede it.
	var (
		quotationNumber string
		versionNo       int
		prior           *domain.Quotation
	)
	priorResult, err := s.quotations.FindLatestForBOQ(ctx, boq.ID)
	if err == nil && priorResult != nil {
		prior = priorResult
		quotationNumber = prior.QuotationNumber
		versionNo = prior.VersionNo + 1
	} else {
		// NotFound is the happy "first time quoting this BOQ" path.
		// Any other error bubbles up.
		if err != nil && !derrors.IsNotFound(err) {
			return nil, err
		}
		quotationNumber = domain.GenerateQuotationNumber(time.Now())
		versionNo = 1
	}

	// 4. Build the domain row.
	q, err := domain.NewQuotation(boq.ID, op.ID)
	if err != nil {
		return nil, err
	}
	q.QuotationNumber = quotationNumber
	q.VersionNo = versionNo
	q.SellTotal = boq.SellTotal
	q.CostTotal = boq.CostTotal
	q.MarginPct = boq.MarginPct
	q.IssuedBy = in.IssuedBy
	q.Notes = in.Notes
	if in.ValidityDays > 0 {
		q.ValidUntil = q.IssuedAt.Add(time.Duration(in.ValidityDays) * 24 * time.Hour)
	}

	// 5. Render the PDF. We need the SLA labels — fetch them once and
	// map by id so the renderer can show a human label per line.
	slaLabels := map[uuid.UUID]string{}
	if s.slaTemplates != nil {
		templates, err := s.slaTemplates.List(ctx, false)
		if err == nil {
			for _, t := range templates {
				slaLabels[t.ID] = t.Key
			}
		}
	}
	pdfLines := make([]pdf.QuotationLine, 0, len(lines))
	for _, l := range lines {
		label := slaLabels[l.SLATemplateID]
		if label == "" {
			label = l.SLATemplateID.String()[:8] + "…"
		}
		pdfLines = append(pdfLines, pdf.QuotationLine{
			SKU:           l.SKU,
			Name:          l.Name,
			Unit:          l.Unit,
			Quantity:      l.Quantity,
			SellUnitPrice: l.SellUnitPrice,
			DiscountPct:   l.LineDiscountPct,
			SLALabel:      label,
		})
	}
	bytes, err := pdf.RenderQuotation(pdf.QuotationData{
		QuotationNumber:   q.QuotationNumber,
		VersionNo:         q.VersionNo,
		IssuedAt:          q.IssuedAt,
		ValidUntil:        q.ValidUntil,
		Currency:          q.Currency,
		IssuerName:        "ION Core",
		IssuerAddress:     "—",
		IssuerEmail:       "billing@ion.local",
		OpportunityNumber: op.OpportunityNumber,
		AccountName:       op.AccountName,
		PICName:           op.PICName,
		PICTitle:          op.PICTitle,
		PICEmail:          op.PICEmail,
		Lines:             pdfLines,
		SellTotal:         q.SellTotal,
		Notes:             q.Notes,
	})
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "quotation.render", "render quotation pdf", err)
	}

	// 6. Hash + size.
	sum := sha256.Sum256(bytes)
	q.PDFBytes = bytes
	q.PDFHash = hex.EncodeToString(sum[:])
	q.PDFBytesSize = len(bytes)
	if err := q.EnsurePDFReady(); err != nil {
		return nil, err
	}

	// 7. Persist new row; supersede the prior if any.
	if err := s.quotations.Create(ctx, q); err != nil {
		return nil, err
	}
	if prior != nil {
		if err := prior.Supersede(); err == nil {
			_ = s.quotations.Update(ctx, prior, nil)
		}
	}
	return q, nil
}

// AutoGenerateQuotationOnApproval is the hook the BOQ-approval path
// fires once it transitions a BOQ to boq_approved. Best-effort:
// failures are logged but don't roll back the approval (the operator
// can manually regenerate via the quotation endpoint).
//
// Kept as a small, narrowly-scoped method so the approval logic in
// boq.go stays readable — the side-effect is a single line there.
func (s *Service) AutoGenerateQuotationOnApproval(ctx context.Context, boqID uuid.UUID) {
	if s.quotations == nil {
		// Service was wired without quotations; nothing to do.
		return
	}
	_, err := s.GenerateQuotation(ctx, port.GenerateQuotationInput{
		BOQVersionID: boqID,
	})
	if err != nil && s.log != nil {
		s.log.Warn("auto-generate quotation failed",
			"boq_id", boqID.String(),
			"err", err.Error(),
		)
	}
}

// =====================================================================
// List / get
// =====================================================================

func (s *Service) ListQuotations(ctx context.Context, f port.QuotationListFilter) ([]domain.Quotation, int, error) {
	if s.quotations == nil {
		return nil, 0, errQuotationNotConfigured()
	}
	return s.quotations.List(ctx, f)
}

func (s *Service) GetQuotation(ctx context.Context, id uuid.UUID) (*domain.Quotation, error) {
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	return s.quotations.FindByID(ctx, id)
}

func (s *Service) GetQuotationPDF(ctx context.Context, id uuid.UUID) ([]byte, string, error) {
	if s.quotations == nil {
		return nil, "", errQuotationNotConfigured()
	}
	return s.quotations.FindPDFBytes(ctx, id)
}

// =====================================================================
// Lifecycle actions
// =====================================================================

func (s *Service) AcceptQuotation(ctx context.Context, in port.AcceptQuotationInput) (*domain.Quotation, error) {
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	q, err := s.quotations.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := q.MarkAccepted(); err != nil {
		return nil, err
	}
	q.Revision++
	if err := s.quotations.Update(ctx, q, in.IfRevision); err != nil {
		return nil, err
	}
	// Phase 5 hook — auto-create invoice + EWO. Best-effort, swallows
	// errors (logged inside). The operator can manually issue via
	// POST /invoices/from-quotation/{id} if the auto-fire misses.
	_ = s.AutoCreateOnQuotationAccept(ctx, q.ID)
	return q, nil
}

func (s *Service) RejectQuotation(ctx context.Context, in port.RejectQuotationInput) (*domain.Quotation, error) {
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	q, err := s.quotations.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := q.MarkRejected(in.Reason); err != nil {
		return nil, err
	}
	q.Revision++
	if err := s.quotations.Update(ctx, q, in.IfRevision); err != nil {
		return nil, err
	}
	return q, nil
}

func (s *Service) CancelQuotation(ctx context.Context, in port.CancelQuotationInput) (*domain.Quotation, error) {
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	q, err := s.quotations.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := q.Cancel(in.Reason); err != nil {
		return nil, err
	}
	q.Revision++
	if err := s.quotations.Update(ctx, q, in.IfRevision); err != nil {
		return nil, err
	}
	// Pre-launch cascade — if a termin plan is attached and still
	// active/draft, cancel it too. Issued termin invoices are not
	// retroactively voided (that would require operator review); they
	// stay valid but the plan stops emitting new ones.
	if s.invoicePlans != nil {
		if plan, perr := s.invoicePlans.FindByQuotationID(ctx, q.ID); perr == nil && plan != nil {
			if plan.Status == domain.InvoicePlanStatusDraft || plan.Status == domain.InvoicePlanStatusActive {
				_ = plan.Cancel("quotation cancelled: " + in.Reason)
				_ = s.invoicePlans.Update(ctx, plan)
			}
		}
	}
	return q, nil
}
