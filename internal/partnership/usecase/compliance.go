package usecase

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/notifyx"
)

// ComplianceService runs the monthly compliance evaluator + serves the
// read surface for the dashboard.
//
// The cron in internal/partnership/cron calls EvaluateMonth(year, month)
// once per closed calendar month; the HTTP surface also exposes a
// synchronous "evaluate now" endpoint for admin triggers + smoke tests.
//
// notifyx.Dispatcher is optional — when nil, breach notifications are
// logged but no push is dispatched. This lets the smoke test run
// without configuring notifyx tables.
type ComplianceService struct {
	compliances port.ComplianceEvaluationRepository
	submissions port.MonthlySubmissionRepository
	agreements  port.AgreementRepository
	notifier    *notifyx.Dispatcher
	audit       audit.Writer
	log         *slog.Logger
}

func NewComplianceService(
	compliances port.ComplianceEvaluationRepository,
	submissions port.MonthlySubmissionRepository,
	agreements port.AgreementRepository,
	notifier *notifyx.Dispatcher,
	auditW audit.Writer,
	log *slog.Logger,
) *ComplianceService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &ComplianceService{
		compliances: compliances,
		submissions: submissions,
		agreements:  agreements,
		notifier:    notifier,
		audit:       auditW,
		log:         log,
	}
}

// EvaluateMonth runs the compliance check for every reseller with an
// active agreement covering (year, month).
//
// Per reseller:
//  1. Look up the agreement active at the period_end of (year, month).
//  2. Look up the confirmed submission for (reseller, year, month).
//     If missing → skip (no eval emitted; the dashboard surfaces "no
//     submission" via the missing row).
//  3. Count confirmed submissions strictly before (year, month) →
//     monthsSinceFirstSubmission.
//  4. Call domain.Evaluate(submission, agreement, monthsSince, now).
//  5. Persist the row. UNIQUE (reseller, year, month) makes this
//     idempotent — re-running on the same period is a no-op
//     (Conflict on insert → logged + skipped, not an error).
//  6. If status == breached, fire a notifyx push (best-effort).
//
// Returns a summary tuple for the cron to log.
func (s *ComplianceService) EvaluateMonth(ctx context.Context, year, month int) (port.EvaluateMonthSummary, error) {
	if month < 1 || month > 12 {
		return port.EvaluateMonthSummary{}, derrors.Validation(
			"compliance.month_invalid", "month must be 1..12")
	}
	periodFirst := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodFirst.AddDate(0, 1, -1)
	now := time.Now().UTC()

	resellers, err := s.agreements.ListResellersWithActiveAgreement(ctx, periodEnd)
	if err != nil {
		return port.EvaluateMonthSummary{}, err
	}

	var summary port.EvaluateMonthSummary
	for _, rid := range resellers {
		if err := s.evaluateOne(ctx, rid, year, month, periodEnd, now, &summary); err != nil {
			if s.log != nil {
				s.log.Warn("compliance evaluate one failed",
					"reseller_id", rid.String(), "err", err.Error())
			}
			continue
		}
	}
	return summary, nil
}

// evaluateOne is the per-reseller body of EvaluateMonth.
func (s *ComplianceService) evaluateOne(
	ctx context.Context,
	resellerID uuid.UUID,
	year, month int,
	periodEnd, evaluatedAt time.Time,
	summary *port.EvaluateMonthSummary,
) error {
	agreement, err := s.agreements.FindActive(ctx, resellerID, periodEnd)
	if err != nil {
		if derrors.IsNotFound(err) {
			return nil // shouldn't happen — we got rid from ListResellersWithActiveAgreement
		}
		return err
	}

	sub, err := s.submissions.FindByResellerPeriod(ctx, resellerID, year, month)
	if err != nil {
		if derrors.IsNotFound(err) {
			summary.Skipped++
			return nil
		}
		return err
	}
	if sub.Status != domain.SubmissionStatusConfirmed {
		// Only confirmed submissions count. Draft / submitted /
		// returned are pending; cancelled never counts.
		summary.Skipped++
		return nil
	}

	monthsSince, err := s.submissions.CountConfirmedBefore(ctx, resellerID, year, month)
	if err != nil {
		return err
	}

	eval := domain.Evaluate(sub, agreement, monthsSince, evaluatedAt)
	if err := s.compliances.Create(ctx, &eval); err != nil {
		if derrors.IsConflict(err) {
			// Already evaluated for this period; idempotent re-run.
			if s.log != nil {
				s.log.Debug("compliance evaluation already exists",
					"reseller_id", resellerID.String(),
					"year", year, "month", month)
			}
			return nil
		}
		return err
	}
	summary.Evaluated++
	switch eval.Status {
	case domain.ComplianceStatusRampSkipped:
		summary.RampSkipped++
	case domain.ComplianceStatusPassed:
		summary.Passed++
	case domain.ComplianceStatusBreached:
		summary.Breached++
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "partnership",
		RecordType: "partnership.compliance_evaluation",
		RecordID:   eval.ID.String(),
		After:      string(eval.Status),
		Reason:     "compliance_evaluated",
	})

	// Breach notification — best-effort. Routes to the reseller-account
	// user(s) keyed off the reseller_account_id; the notifyx adapter
	// will resolve the device tokens.
	if eval.Status == domain.ComplianceStatusBreached && s.notifier != nil {
		s.notifier.Send(ctx,
			notifyx.Target{CustomerID: resellerID},
			notifyx.Message{
				Title: "Partnership compliance breach",
				Body:  eval.Reason,
				Topic: "partnership_compliance_breach",
				Data: map[string]string{
					"reseller_account_id": resellerID.String(),
					"year":                intStr(year),
					"month":               intStr(month),
				},
			},
		)
	}

	return nil
}

// GetEvaluation returns the (one) row for a (reseller, year, month).
func (s *ComplianceService) GetEvaluation(ctx context.Context, resellerID uuid.UUID, year, month int) (*domain.ComplianceEvaluation, error) {
	return s.compliances.FindByResellerPeriod(ctx, resellerID, year, month)
}

func (s *ComplianceService) ListEvaluations(ctx context.Context, f port.ComplianceListFilter) ([]domain.ComplianceEvaluation, int, error) {
	return s.compliances.List(ctx, f)
}

// intStr is a tiny helper for the notifyx Data map (which is map[string]string).
func intStr(n int) string {
	return strconv.Itoa(n)
}
