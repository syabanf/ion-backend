package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SettlementService implements the settlement lifecycle.
//
// IssueSettlementForSubmission is the internal hook called by
// SubmissionService.ConfirmSubmission. The HTTP surface only exposes
// list/get/approve/mark-paid — issuance is automatic on confirm.
type SettlementService struct {
	settlements port.SettlementRepository
	submissions port.MonthlySubmissionRepository
	agreements  port.AgreementRepository
	evidence    port.EvidenceStore       // PDFs stored here; same interface as evidence blobs
	pdfGen      port.SettlementPDFGenerator
	audit       audit.Writer
	log         *slog.Logger
}

func NewSettlementService(
	settlements port.SettlementRepository,
	submissions port.MonthlySubmissionRepository,
	agreements port.AgreementRepository,
	evidence port.EvidenceStore,
	pdfGen port.SettlementPDFGenerator,
	auditW audit.Writer,
	log *slog.Logger,
) *SettlementService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &SettlementService{
		settlements: settlements,
		submissions: submissions,
		agreements:  agreements,
		evidence:    evidence,
		pdfGen:      pdfGen,
		audit:       auditW,
		log:         log,
	}
}

// IssueSettlementForSubmission reads the confirmed submission, looks up
// the agreement active at the submission's period_end, computes the
// revshare per agreement.RevshareForRevenue(net), snapshots
// agreement.TermsSnapshot(), persists the Settlement, then renders +
// stores the PDF.
//
// PDF failure is best-effort logged — we don't roll back the
// settlement on a PDF problem since the underlying revenue numbers
// are still valid; an operator can re-render via a follow-up endpoint
// (not in Wave 100 scope).
func (s *SettlementService) IssueSettlementForSubmission(ctx context.Context, submissionID uuid.UUID) (*domain.Settlement, error) {
	sub, err := s.submissions.FindByID(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	if sub.Status != domain.SubmissionStatusConfirmed {
		return nil, derrors.Conflict(
			"settlement.submission_not_confirmed",
			"can only issue settlement for a confirmed submission (current: "+string(sub.Status)+")",
		)
	}
	// Refuse if a settlement already exists for this submission (the
	// DB UNIQUE constraint on submission_id catches this too; we
	// short-circuit for a nicer error).
	if existing, err := s.settlements.FindBySubmission(ctx, sub.ID); err == nil && existing != nil {
		return nil, derrors.Conflict(
			"settlement.already_exists",
			"a settlement already exists for this submission",
		)
	} else if err != nil && !derrors.IsNotFound(err) {
		return nil, err
	}

	// Active agreement at submission.period_end. We don't trust the
	// agreement_id on the submission row alone because it was stamped
	// at draft time; the agreement could have been superseded between
	// draft and confirm. The active-at-period_end query is the
	// canonical contract.
	agreement, err := s.agreements.FindActive(ctx, sub.ResellerAccountID, sub.PeriodEnd())
	if err != nil {
		return nil, err
	}

	// Compute the formula.
	gross := nonNilFloat(sub.GrossRevenue)
	net := nonNilFloat(sub.NetRevenue)
	revshare := agreement.RevshareForRevenue(net)
	// Tax is left at zero for Wave 100 — the per-agreement tax rate
	// (PPN, withholding) lands in a follow-up. The column is on the
	// row so the wire format is stable; we just don't populate it yet.
	tax := 0.0
	payable := revshare + tax

	stl, err := domain.NewSettlement(
		sub.ID, agreement.ID,
		sub.PeriodYear, sub.PeriodMonth,
		gross, net, revshare, tax, payable,
		agreement.TermsSnapshot(),
	)
	if err != nil {
		return nil, err
	}
	if err := s.settlements.Create(ctx, stl); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "partnership",
		RecordType: "partnership.settlement",
		RecordID:   stl.ID.String(),
		After:      string(stl.Status),
		Reason:     "settlement_issued",
	})

	// PDF render — best-effort. We pull the generator + storage at
	// call time so a missing stub just skips this step rather than
	// failing the issue.
	if s.pdfGen != nil && s.evidence != nil {
		pdfBytes, _, err := s.pdfGen.Generate(ctx, stl, agreement, sub)
		if err != nil {
			if s.log != nil {
				s.log.Warn("settlement pdf generate failed",
					"settlement_id", stl.ID.String(), "err", err.Error())
			}
			return stl, nil
		}
		url, hash, err := s.evidence.Store(ctx, pdfBytes, "settlement-"+stl.ID.String()+".pdf")
		if err != nil {
			if s.log != nil {
				s.log.Warn("settlement pdf store failed",
					"settlement_id", stl.ID.String(), "err", err.Error())
			}
			return stl, nil
		}
		if err := s.settlements.UpdatePDF(ctx, stl.ID, url, hash); err != nil {
			if s.log != nil {
				s.log.Warn("settlement pdf update failed",
					"settlement_id", stl.ID.String(), "err", err.Error())
			}
			return stl, nil
		}
		stl.PDFURL = url
		stl.PDFHash = hash
	}

	return stl, nil
}

// ApproveSettlement flips pending → approved.
func (s *SettlementService) ApproveSettlement(ctx context.Context, id, byUserID uuid.UUID) (*domain.Settlement, error) {
	stl, err := s.settlements.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(stl.Status)
	if err := stl.Approve(byUserID); err != nil {
		return nil, err
	}
	if err := s.settlements.UpdateStatus(ctx, stl); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       byUserID,
		Module:       "partnership",
		RecordType:   "partnership.settlement",
		RecordID:     stl.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(stl.Status),
		Reason:       "settlement_approved",
	})
	return stl, nil
}

// MarkSettlementPaid flips approved → paid. `at` is the payment timestamp
// (typically pulled from the bank-transfer proof upload).
func (s *SettlementService) MarkSettlementPaid(ctx context.Context, id uuid.UUID, at time.Time) (*domain.Settlement, error) {
	stl, err := s.settlements.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(stl.Status)
	if err := stl.Pay(at); err != nil {
		return nil, err
	}
	if err := s.settlements.UpdateStatus(ctx, stl); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "partnership",
		RecordType:   "partnership.settlement",
		RecordID:     stl.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(stl.Status),
		Reason:       "settlement_marked_paid",
	})
	return stl, nil
}

// CancelSettlement flips pending|approved → cancelled.
func (s *SettlementService) CancelSettlement(ctx context.Context, id uuid.UUID) (*domain.Settlement, error) {
	stl, err := s.settlements.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(stl.Status)
	if err := stl.Cancel(); err != nil {
		return nil, err
	}
	if err := s.settlements.UpdateStatus(ctx, stl); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "partnership",
		RecordType:   "partnership.settlement",
		RecordID:     stl.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(stl.Status),
		Reason:       "settlement_cancelled",
	})
	return stl, nil
}

func (s *SettlementService) GetSettlement(ctx context.Context, id uuid.UUID) (*domain.Settlement, error) {
	return s.settlements.FindByID(ctx, id)
}

func (s *SettlementService) ListSettlements(ctx context.Context, f port.SettlementListFilter) ([]domain.Settlement, int, error) {
	return s.settlements.List(ctx, f)
}

func nonNilFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
