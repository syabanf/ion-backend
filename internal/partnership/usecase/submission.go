package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubmissionService implements the monthly-submission flow.
//
// Settlement issuance happens inline on Confirm (composed of
// SettlementService.IssueSettlementForSubmission). We don't take a
// pgx tx because the repos are abstract — if the inline settlement
// fails the submission stays Confirmed and the operator re-runs the
// settlement-issue endpoint manually (best-effort tx semantics, same
// pattern as enterprise's customer_po flow).
type SubmissionService struct {
	submissions port.MonthlySubmissionRepository
	agreements  port.AgreementRepository
	settlements *SettlementService // composed for ConfirmSubmission → IssueSettlementForSubmission
	audit       audit.Writer
}

func NewSubmissionService(
	subs port.MonthlySubmissionRepository,
	ags port.AgreementRepository,
	stl *SettlementService,
	auditW audit.Writer,
) *SubmissionService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &SubmissionService{
		submissions: subs,
		agreements:  ags,
		settlements: stl,
		audit:       auditW,
	}
}

// DraftSubmission opens a fresh draft row for (reseller, year, month).
//
// Refuses if a confirmed submission already exists for that period —
// the reseller can't re-open a closed month. Returns Conflict if any
// non-cancelled submission exists for the period (draft, submitted,
// confirmed, returned all block; cancelled does not so the operator
// can recover from a bad cancel).
func (s *SubmissionService) DraftSubmission(ctx context.Context, in port.DraftSubmissionInput) (*domain.MonthlySubmission, error) {
	if in.ResellerAccountID == uuid.Nil {
		return nil, derrors.Validation("submission.reseller_required", "reseller_account_id is required")
	}

	// Look up the agreement active at the period_end so the submission
	// is bound to the right contract. We can't open a draft without
	// an active agreement — there's nothing to settle against.
	first := time.Date(in.PeriodYear, time.Month(in.PeriodMonth), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := first.AddDate(0, 1, -1)
	agreement, err := s.agreements.FindActive(ctx, in.ResellerAccountID, periodEnd)
	if err != nil {
		if derrors.IsNotFound(err) {
			return nil, derrors.Validation(
				"submission.no_active_agreement",
				"no active partnership agreement covers the requested period",
			)
		}
		return nil, err
	}

	// Pre-check for existing submission for this period (so we can
	// return a clearer error than the raw UNIQUE-constraint violation).
	if existing, err := s.submissions.FindByResellerPeriod(ctx, in.ResellerAccountID, in.PeriodYear, in.PeriodMonth); err == nil {
		// Cancelled rows don't block (operator may want to re-open a
		// mistakenly cancelled month). Everything else does.
		if existing.Status != domain.SubmissionStatusCancelled {
			return nil, derrors.Conflict(
				"submission.already_exists",
				"a submission for this reseller+period already exists (status: "+string(existing.Status)+")",
			)
		}
	} else if !derrors.IsNotFound(err) {
		return nil, err
	}

	sub, err := domain.NewMonthlySubmission(agreement.ID, in.ResellerAccountID, in.PeriodYear, in.PeriodMonth)
	if err != nil {
		return nil, err
	}
	if err := s.submissions.Create(ctx, sub); err != nil {
		return nil, err
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "partnership",
		RecordType: "partnership.monthly_submission",
		RecordID:   sub.ID.String(),
		After:      string(sub.Status),
		Reason:     "submission_drafted",
	})
	return sub, nil
}

// UpdateSubmission applies a partial PATCH on a draft or returned row.
// A returned row implicitly transitions back to draft on edit so the
// reseller can re-submit after applying the Finance Review feedback.
func (s *SubmissionService) UpdateSubmission(ctx context.Context, in port.UpdateSubmissionInput) (*domain.MonthlySubmission, error) {
	sub, err := s.submissions.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if sub.Status != domain.SubmissionStatusDraft && sub.Status != domain.SubmissionStatusReturned {
		return nil, derrors.Conflict(
			"submission.not_editable",
			"only draft or returned submissions can be edited (current: "+string(sub.Status)+")",
		)
	}

	before := string(sub.Status)
	if sub.Status == domain.SubmissionStatusReturned {
		if err := sub.MarkDraft(); err != nil {
			return nil, err
		}
	}

	if in.GrossRevenue != nil {
		v := *in.GrossRevenue
		sub.GrossRevenue = &v
	}
	if in.NetRevenue != nil {
		v := *in.NetRevenue
		sub.NetRevenue = &v
	}
	if in.SubscriberCount != nil {
		v := *in.SubscriberCount
		sub.SubscriberCount = &v
	}
	if in.ChurnCount != nil {
		v := *in.ChurnCount
		sub.ChurnCount = &v
	}
	if in.EvidenceURL != nil {
		sub.EvidenceURL = *in.EvidenceURL
	}
	if in.EvidenceHash != nil {
		sub.EvidenceHash = *in.EvidenceHash
	}
	sub.UpdatedAt = time.Now().UTC()

	if err := s.submissions.UpdateFields(ctx, sub); err != nil {
		return nil, err
	}
	if before != string(sub.Status) {
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:       "partnership",
			RecordType:   "partnership.monthly_submission",
			RecordID:     sub.ID.String(),
			FieldChanged: "status",
			Before:       before,
			After:        string(sub.Status),
			Reason:       "submission_redrafted_via_edit",
		})
	}
	return sub, nil
}

// SubmitForReview flips draft|returned → submitted.
func (s *SubmissionService) SubmitForReview(ctx context.Context, id, byUserID uuid.UUID) (*domain.MonthlySubmission, error) {
	sub, err := s.submissions.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(sub.Status)
	if err := sub.Submit(byUserID); err != nil {
		return nil, err
	}
	if err := s.submissions.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       byUserID,
		Module:       "partnership",
		RecordType:   "partnership.monthly_submission",
		RecordID:     sub.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(sub.Status),
		Reason:       "submission_submitted",
	})
	return sub, nil
}

// ConfirmSubmission flips submitted → confirmed AND issues a settlement.
//
// The settlement issuance is a separate repository write; we don't
// hold a pgx tx across both because the repos are abstract. If the
// settlement fails, the submission stays Confirmed and the operator
// can re-issue via a separate endpoint (TC-PS phase 2; not in Wave
// 100 scope). The audit row still captures the confirm event so the
// flow is auditable.
func (s *SubmissionService) ConfirmSubmission(ctx context.Context, id, byUserID uuid.UUID) (*domain.MonthlySubmission, *domain.Settlement, error) {
	sub, err := s.submissions.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	before := string(sub.Status)
	if err := sub.Confirm(byUserID); err != nil {
		return nil, nil, err
	}
	if err := s.submissions.UpdateStatus(ctx, sub); err != nil {
		return nil, nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       byUserID,
		Module:       "partnership",
		RecordType:   "partnership.monthly_submission",
		RecordID:     sub.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(sub.Status),
		Reason:       "submission_confirmed",
	})

	// Settlement issuance — composed in the same logical step. Wire is
	// optional so this svc remains useful even if settlement deps
	// aren't configured (test rigs, partial bootstraps).
	if s.settlements == nil {
		return sub, nil, nil
	}
	stl, err := s.settlements.IssueSettlementForSubmission(ctx, sub.ID)
	if err != nil {
		// Submission stays Confirmed; caller decides whether to retry.
		return sub, nil, err
	}
	return sub, stl, nil
}

// ReturnSubmission flips submitted → returned with a Finance Review
// comment (TC-PMS-005).
func (s *SubmissionService) ReturnSubmission(ctx context.Context, id uuid.UUID, reason string, byUserID uuid.UUID) (*domain.MonthlySubmission, error) {
	sub, err := s.submissions.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(sub.Status)
	at := time.Now().UTC()
	if err := sub.Return(reason, at); err != nil {
		return nil, err
	}
	if err := s.submissions.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       byUserID,
		Module:       "partnership",
		RecordType:   "partnership.monthly_submission",
		RecordID:     sub.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(sub.Status),
		Reason:       "submission_returned: " + reason,
	})
	return sub, nil
}

// CancelSubmission flips draft|returned → cancelled (terminal).
func (s *SubmissionService) CancelSubmission(ctx context.Context, id uuid.UUID) (*domain.MonthlySubmission, error) {
	sub, err := s.submissions.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(sub.Status)
	if err := sub.Cancel(); err != nil {
		return nil, err
	}
	if err := s.submissions.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "partnership",
		RecordType:   "partnership.monthly_submission",
		RecordID:     sub.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(sub.Status),
		Reason:       "submission_cancelled",
	})
	return sub, nil
}

func (s *SubmissionService) GetSubmission(ctx context.Context, id uuid.UUID) (*domain.MonthlySubmission, error) {
	return s.submissions.FindByID(ctx, id)
}

func (s *SubmissionService) ListSubmissions(ctx context.Context, f port.SubmissionListFilter) ([]domain.MonthlySubmission, int, error) {
	return s.submissions.List(ctx, f)
}
