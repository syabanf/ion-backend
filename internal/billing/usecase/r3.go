// M6 r3 — voluntary termination, auto-termination follow-through, and
// referral reward minting.
//
// Voluntary flow:
//   RequestVoluntaryTermination → checks lock-in + open requests
//                                → snapshots outstanding balance
//                                → mints a final invoice if needed (penalty +
//                                  open balance lines)
//                                → status flips: requested → awaiting_payment
//                                  → wo_pending on full payment
//                                → CreateTerminationWO → status wo_created
//                                → field-svc closes WO → status completed
//                                  (round-3 leaves the wo_created → completed
//                                  edge to ops since the customer-flip
//                                  belongs to the field BAST verify hook)
//
// Auto flow (driven from the tick):
//   tickTerminations scans suspended customers whose suspended_at exceeds
//   the policy's terminate_after_suspended_days, mints a termination
//   request (kind=auto), and immediately creates the WO. No final invoice
//   because the unpaid invoices ARE the reason.
//
// Referral reward (hooked from RecordPayment):
//   maybeApplyReferralReward fires alongside the commission hook when an
//   OTC flips to paid. It looks up the referee's referral row and accrues
//   one row in billing.referral_rewards if one doesn't already exist.

package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// LockInPenaltyMultiplier is applied to remaining lock-in months when a
// voluntary termination is requested before lock_in_until. Held in code
// rather than policy because it's a contract decision rather than an
// operational knob.
const LockInPenaltyMonthlyMultiplier = 1.0

// WithR3 attaches the r3 repos + field gateway. Nil-safe — r1/r2
// callers stay valid.
func (s *Service) WithR3(
	terminations port.TerminationRequestRepository,
	rewards port.ReferralRewardRepository,
	field port.FieldGateway,
) *Service {
	s.terminations = terminations
	s.rewards = rewards
	s.field = field
	return s
}

// --- Voluntary termination -----------------------------------------------

func (s *Service) RequestVoluntaryTermination(ctx context.Context, in port.RequestTerminationInput) (*domain.TerminationRequest, error) {
	if s.terminations == nil || s.crm == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r3_not_wired", "termination repo not configured")
	}
	if in.CustomerID == uuid.Nil {
		return nil, derrors.Validation("termination.customer_required", "customer_id is required")
	}

	// Reject if another request is already in flight.
	if open, _ := s.terminations.FindOpenForCustomer(ctx, in.CustomerID); open != nil {
		return nil, derrors.Conflict("termination.open_exists",
			"a termination request is already in progress for this customer")
	}

	summary, err := s.crm.CustomerSummary(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	if summary.Status == "terminated" {
		return nil, derrors.Conflict("termination.already_terminated",
			"customer is already terminated")
	}

	now := time.Now().UTC()
	outstanding, err := s.outstandingFor(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	penalty := 0.0
	if summary.LockInUntil != nil && summary.LockInUntil.After(now) && summary.MonthlyPrice > 0 {
		// Remaining lock-in months × monthly × multiplier (round up).
		monthsLeft := monthsBetween(now, *summary.LockInUntil)
		if monthsLeft < 1 {
			monthsLeft = 1
		}
		penalty = round2(float64(monthsLeft) * summary.MonthlyPrice * LockInPenaltyMonthlyMultiplier)
	}

	t := &domain.TerminationRequest{
		ID:                   uuid.New(),
		CustomerID:           in.CustomerID,
		OrderID:              summary.OrderID,
		Kind:                 domain.TerminationKindVoluntary,
		Status:               domain.TerminationStatusRequested,
		Reason:               in.Reason,
		RequestedByUserID:    nilUUID(in.RequestedBy),
		PenaltyAmount:        penalty,
		OutstandingAtRequest: outstanding,
		RequestedAt:          now,
	}

	// Decide initial status. If the customer owes nothing and there's no
	// lock-in penalty, we can skip 'awaiting_payment' and go straight to
	// minting the WO.
	owes := outstanding + penalty
	if owes < 0.01 {
		// Straight to WO creation.
		t.Status = domain.TerminationStatusWOPending
		if err := s.terminations.Create(ctx, t); err != nil {
			return nil, err
		}
		_ = s.mintTerminationWO(ctx, t, summary)
		return s.terminations.FindByID(ctx, t.ID)
	}

	// Otherwise mint a final invoice (penalty line + outstanding-balance
	// notice line is informational only; we don't re-bill paid invoices).
	lines := []port.LineItemInput{}
	if penalty > 0 {
		lines = append(lines, port.LineItemInput{
			Description: "Lock-in early termination penalty",
			ItemType:    "penalty",
			Quantity:    1,
			UnitPrice:   penalty,
		})
	}
	if outstanding > 0 {
		// We don't double-bill — the existing invoices remain open.
		// We just remind via a zero-amount line in the request.notes.
		t.Notes = fmt.Sprintf("Outstanding open invoices total Rp %.0f at request time.", outstanding)
	}
	if len(lines) > 0 {
		due := now.AddDate(0, 0, 7)
		inv, err := s.CreateInvoice(ctx, port.CreateInvoiceInput{
			CustomerID:       in.CustomerID,
			OrderID:          summary.OrderID,
			InvoiceType:      domain.InvoiceTypeAddon,
			PPNRate:          11.0,
			DueDate:          due,
			Notes:            "Voluntary termination final invoice",
			IssueImmediately: true,
			Lines:            lines,
		})
		if err != nil {
			return nil, err
		}
		invID := inv.Invoice.ID
		t.FinalInvoiceID = &invID
	}
	t.Status = domain.TerminationStatusAwaitingPayment
	if err := s.terminations.Create(ctx, t); err != nil {
		return nil, err
	}
	return s.terminations.FindByID(ctx, t.ID)
}

func (s *Service) CancelTerminationRequest(ctx context.Context, id uuid.UUID, by uuid.UUID, reason string) (*domain.TerminationRequest, error) {
	if s.terminations == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r3_not_wired", "termination repo not configured")
	}
	t, err := s.terminations.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status == domain.TerminationStatusCompleted ||
		t.Status == domain.TerminationStatusCancelled {
		return nil, derrors.Conflict("termination.terminal", "termination already terminal")
	}
	t.Status = domain.TerminationStatusCancelled
	now := time.Now().UTC()
	t.CompletedAt = &now
	t.Notes = strJoin(t.Notes, fmt.Sprintf("cancelled by %s: %s", by, reason))
	if err := s.terminations.Update(ctx, t); err != nil {
		return nil, err
	}
	return s.terminations.FindByID(ctx, id)
}

func (s *Service) ListTerminationRequests(ctx context.Context, f port.TerminationRequestFilter) ([]domain.TerminationRequest, int, error) {
	if s.terminations == nil {
		return nil, 0, derrors.New(derrors.KindInternal, "billing.r3_not_wired", "termination repo not configured")
	}
	return s.terminations.List(ctx, f)
}

func (s *Service) GetTerminationRequest(ctx context.Context, id uuid.UUID) (*domain.TerminationRequest, error) {
	if s.terminations == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r3_not_wired", "termination repo not configured")
	}
	return s.terminations.FindByID(ctx, id)
}

func (s *Service) ListReferralRewards(ctx context.Context, f port.ReferralRewardFilter) ([]domain.ReferralReward, error) {
	if s.rewards == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r3_not_wired", "rewards repo not configured")
	}
	return s.rewards.List(ctx, f)
}

// CompleteTerminationByWO is the field→billing callback fired when NOC
// approves a termination-type BAST. We:
//   1. Find the termination_request for the given wo_id.
//   2. Flip it to 'completed' (idempotent: skip if already completed).
//   3. Flip the customer to 'terminated' (drives suspended_at/terminated_at).
//   4. Tell RADIUS to deactivate (best-effort; non-fatal).
//
// No-op when r3 isn't wired so field-svc can keep running against an
// older billing-svc.
func (s *Service) CompleteTerminationByWO(ctx context.Context, woID uuid.UUID) error {
	if s.terminations == nil || s.crm == nil {
		return nil
	}
	t, err := s.terminations.FindByWOID(ctx, woID)
	if err != nil {
		return err
	}
	if t == nil {
		// Not all approved WOs are terminations — silently skip.
		return nil
	}
	if t.Status == domain.TerminationStatusCompleted ||
		t.Status == domain.TerminationStatusCancelled {
		return nil
	}
	now := time.Now().UTC()
	t.Status = domain.TerminationStatusCompleted
	t.CompletedAt = &now
	if err := s.terminations.Update(ctx, t); err != nil {
		return err
	}
	reason := "termination completed via wo " + woID.String()
	if err := s.crm.SetCustomerStatus(ctx, t.CustomerID, "terminated", reason); err != nil {
		// Don't roll back the termination flip — the operator can still
		// reconcile customer state manually.
		s.logErr("crm terminate failed", err)
	}
	if s.network != nil {
		if err := s.network.DeactivateCustomer(ctx, t.CustomerID, reason); err != nil {
			s.logErr("radius deactivate failed", err)
		}
	}
	return nil
}

// --- Auto-termination (called from tickTerminations) --------------------

// shouldAutoTerminate is the cutoff predicate: true when the customer
// has been suspended long enough (>= threshold days) for the scheduler
// to mint an auto-termination request.
//
// Boundary semantics — locked in by TestShouldAutoTerminate:
//   - SuspendedAt == nil          → false (never suspended)
//   - SuspendedAt > cutoff         → false (too recent; still inside window)
//   - SuspendedAt <= cutoff        → true  (aged past threshold, eligible)
//
// "cutoff" is `now - thresholdDays`. The boundary case where SuspendedAt
// equals cutoff exactly evaluates to true — we deliberately don't bake
// in an off-by-one to defer eligibility.
func shouldAutoTerminate(now time.Time, suspendedAt *time.Time, thresholdDays int) bool {
	if suspendedAt == nil {
		return false
	}
	cutoff := now.AddDate(0, 0, -thresholdDays)
	return !suspendedAt.After(cutoff)
}

// runAutoTermination scans suspended customers older than the threshold
// and creates a termination request + WO for each. Idempotent: skip
// customers that already have an open request.
func (s *Service) runAutoTermination(ctx context.Context, now time.Time, policy *domain.Policy, rep *port.TickReport) error {
	if s.terminations == nil || s.crm == nil {
		// No-op without r3 wiring.
		return nil
	}
	suspended, err := s.crm.SuspendedCustomers(ctx)
	if err != nil {
		return err
	}
	for _, sc := range suspended {
		// Resolve the suspension schema per-customer so an override
		// (longer / shorter terminate-after threshold) is honoured.
		// Falls back to legacy policy fields when the resolver isn't
		// wired or the keys are missing.
		custPolicy := s.resolvedSuspensionPolicy(ctx, sc.CustomerID, policy)
		if !shouldAutoTerminate(now, sc.SuspendedAt, custPolicy.TerminateAfterSuspendedDays) {
			continue
		}
		// Skip if a request is already open.
		if open, _ := s.terminations.FindOpenForCustomer(ctx, sc.CustomerID); open != nil {
			continue
		}
		outstanding, _ := s.outstandingFor(ctx, sc.CustomerID)
		t := &domain.TerminationRequest{
			ID:                   uuid.New(),
			CustomerID:           sc.CustomerID,
			OrderID:              sc.OrderID,
			Kind:                 domain.TerminationKindAuto,
			Status:               domain.TerminationStatusWOPending,
			Reason:               fmt.Sprintf("auto-termination after %d days suspended", custPolicy.TerminateAfterSuspendedDays),
			OutstandingAtRequest: outstanding,
			RequestedAt:          now,
		}
		if err := s.terminations.Create(ctx, t); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("auto-term %s: %v", sc.CustomerID, err))
			continue
		}
		summary, err := s.crm.CustomerSummary(ctx, sc.CustomerID)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("auto-term summary %s: %v", sc.CustomerID, err))
			continue
		}
		if err := s.mintTerminationWO(ctx, t, summary); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("auto-term wo %s: %v", sc.CustomerID, err))
			continue
		}
		rep.TerminationsTriggered++
	}
	return nil
}

func (s *Service) mintTerminationWO(ctx context.Context, t *domain.TerminationRequest, summary *port.CustomerSummary) error {
	if s.field == nil {
		// No field gateway wired — leave the request at wo_pending so an
		// operator can act on it manually.
		return nil
	}
	woID, err := s.field.CreateTerminationWO(ctx, port.CreateTerminationWOInput{
		CustomerID: t.CustomerID,
		OrderID:    t.OrderID,
		Address:    summary.Address,
		BranchID:   summary.BranchID,
		Notes:      "Termination WO — " + string(t.Kind),
	})
	if err != nil {
		return err
	}
	t.WOID = &woID
	t.Status = domain.TerminationStatusWOCreated
	return s.terminations.Update(ctx, t)
}

// --- Referral reward hook (called from RecordPayment) -------------------

func (s *Service) maybeApplyReferralReward(ctx context.Context, invoice *port.InvoiceView, payment *domain.Payment) {
	if s.rewards == nil || s.crm == nil {
		return
	}
	if invoice.Invoice.Status != domain.InvoiceStatusPaid {
		return
	}
	if invoice.Invoice.InvoiceType != domain.InvoiceTypeOTC {
		return
	}
	ref, err := s.crm.ReferralForReferee(ctx, invoice.Invoice.CustomerID)
	if err != nil || ref == nil {
		return
	}
	if ref.Status != "pending" {
		return // already rewarded or voided
	}
	if exists, _ := s.rewards.ExistsForReferral(ctx, ref.ID); exists {
		return
	}
	// Reward base = referee's monthly_price × ReferralRewardPercentOfMonthly.
	summary, err := s.crm.CustomerSummary(ctx, invoice.Invoice.CustomerID)
	if err != nil || summary == nil || summary.MonthlyPrice <= 0 {
		return
	}
	amount := round2(summary.MonthlyPrice * domain.ReferralRewardPercentOfMonthly / 100.0)
	invID := invoice.Invoice.ID
	r := &domain.ReferralReward{
		ID:                 uuid.New(),
		ReferralID:         ref.ID,
		ReferrerCustomerID: ref.ReferrerCustomerID,
		RefereeCustomerID:  ref.RefereeCustomerID,
		OrderID:            invoice.Invoice.OrderID,
		InvoiceID:          &invID,
		Amount:             amount,
		Status:             domain.ReferralRewardAccrued,
		CreatedAt:          time.Now().UTC(),
	}
	if err := s.rewards.Create(ctx, r); err != nil {
		s.logErr("referral reward create failed", err)
	}
}

// --- Helpers -------------------------------------------------------------

// outstandingFor sums total - paid for every non-cancelled invoice on a
// customer. We use the existing invoice view list — at customer scale
// this is small and trivially cached.
func (s *Service) outstandingFor(ctx context.Context, customerID uuid.UUID) (float64, error) {
	views, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
		CustomerID: &customerID,
		Limit:      500,
	})
	if err != nil {
		return 0, err
	}
	sum := 0.0
	for _, v := range views {
		if v.Invoice.Status == domain.InvoiceStatusCancelled ||
			v.Invoice.Status == domain.InvoiceStatusPaid {
			continue
		}
		if v.OutstandingAmount > 0 {
			sum += v.OutstandingAmount
		}
	}
	return round2(sum), nil
}

// monthsBetween returns the number of whole calendar months between a
// and b (always >= 0). Used for lock-in penalty calc.
func monthsBetween(a, b time.Time) int {
	if !b.After(a) {
		return 0
	}
	years := b.Year() - a.Year()
	months := int(b.Month()) - int(a.Month())
	return years*12 + months
}

func nilUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

func strJoin(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// satisfy unused warnings if log is nil-only.
var _ = slog.Logger{}
