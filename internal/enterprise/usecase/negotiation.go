package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Compile-time check.
var _ port.NegotiationUseCase = (*Service)(nil)

// WithNegotiation attaches Phase 4b repos. Nil-safe: methods surface
// errNegotiationNotConfigured when called without wiring.
func (s *Service) WithNegotiation(
	cfg port.NegotiationConfigRepository,
	neg port.NegotiationRepository,
	rounds port.NegotiationRoundRepository,
	approvals port.NegotiationRoundApprovalRepository,
) *Service {
	s.negotiationConfigs = cfg
	s.negotiations = neg
	s.negotiationRounds = rounds
	s.negotiationApprovals = approvals
	return s
}

func errNegotiationNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "negotiation.not_configured",
		"negotiation surface is not configured for this service", nil)
}

// =====================================================================
// Config
// =====================================================================

func (s *Service) SetNegotiationConfig(ctx context.Context, in port.SetNegotiationConfigInput) (*domain.NegotiationConfig, error) {
	if s.negotiationConfigs == nil {
		return nil, errNegotiationNotConfigured()
	}
	// Load current to check the lock state.
	current, err := s.negotiationConfigs.GetConfig(ctx, in.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if err := domain.EnsureMutable(current); err != nil {
		return nil, err
	}
	// Build the new config from the input + validate.
	cfg := &domain.NegotiationConfig{
		BOQVersionID:             in.BOQVersionID,
		Enabled:                  in.Enabled,
		Type:                     in.Type,
		Mode:                     domain.ApprovalMode(in.Mode),
		PricingAdjustmentAllowed: in.PricingAdjustmentAllowed,
		MarginFloorPct:           in.MarginFloorPct,
		DiscountCeilingPct:       in.DiscountCeilingPct,
		Participants:             in.Participants,
		LockedAt:                 current.LockedAt, // preserved
	}
	// Stamp boq_version_id on each participant in case the caller
	// constructed them with the zero UUID.
	for i := range cfg.Participants {
		cfg.Participants[i].BOQVersionID = in.BOQVersionID
	}
	if err := domain.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if err := s.negotiationConfigs.SetConfig(ctx, cfg); err != nil {
		return nil, err
	}
	return s.negotiationConfigs.GetConfig(ctx, in.BOQVersionID)
}

func (s *Service) GetNegotiationConfig(ctx context.Context, boqVersionID uuid.UUID) (*domain.NegotiationConfig, error) {
	if s.negotiationConfigs == nil {
		return nil, errNegotiationNotConfigured()
	}
	return s.negotiationConfigs.GetConfig(ctx, boqVersionID)
}

// LockConfigOnApproval is the BOQ-approval hook (TC-NEG-002). Called
// from ApproveStep when the chain completes. Idempotent — locking an
// already-locked config is a no-op.
//
// Also seeds the Negotiation row (status='inactive') if absent, so
// Sales Manager has a target to activate later. Per the catalog the
// negotiation row is "ready but not started" once the BOQ is approved.
func (s *Service) LockConfigOnApproval(ctx context.Context, boqVersionID uuid.UUID) error {
	if s.negotiationConfigs == nil {
		return nil // service wired without negotiation; tolerate
	}
	if err := s.negotiationConfigs.LockConfig(ctx, boqVersionID); err != nil {
		return err
	}
	if s.negotiations == nil {
		return nil
	}
	if _, err := s.negotiations.FindByBOQ(ctx, boqVersionID); err != nil {
		if !derrors.IsNotFound(err) {
			return err
		}
		// Seed the inactive negotiation row.
		n, nerr := domain.NewNegotiation(boqVersionID)
		if nerr != nil {
			return nerr
		}
		if cerr := s.negotiations.Create(ctx, n); cerr != nil {
			return cerr
		}
	}
	return nil
}

// SupersedeOnBOQRevision is the Edge #1 hook — when a rejected BOQ
// starts a revision, any active negotiation on the prior version
// aborts and pending rounds flip to superseded. Stub for now: a
// hook is wired in but the cascade only runs when the negotiation
// row exists.
func (s *Service) SupersedeOnBOQRevision(ctx context.Context, boqVersionID uuid.UUID) error {
	if s.negotiations == nil {
		return nil
	}
	n, err := s.negotiations.FindByBOQ(ctx, boqVersionID)
	if err != nil {
		if derrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if n.Status != domain.NegotiationStatusActive {
		return nil
	}
	_ = n.Abort("boq_revised")
	if err := s.negotiations.Update(ctx, n); err != nil {
		return err
	}
	rounds, err := s.negotiationRounds.List(ctx, n.ID)
	if err != nil {
		return err
	}
	for i := range rounds {
		round := &rounds[i]
		if round.Status == domain.NegotiationRoundPendingApproval {
			round.Supersede()
			_ = s.negotiationRounds.Update(ctx, round)
		}
	}
	return nil
}

// =====================================================================
// Lifecycle
// =====================================================================

func (s *Service) GetNegotiation(ctx context.Context, id uuid.UUID) (*domain.Negotiation, []domain.NegotiationRound, error) {
	if s.negotiations == nil {
		return nil, nil, errNegotiationNotConfigured()
	}
	n, err := s.negotiations.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rounds, err := s.negotiationRounds.List(ctx, n.ID)
	if err != nil {
		return nil, nil, err
	}
	return n, rounds, nil
}

func (s *Service) GetNegotiationByBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Negotiation, error) {
	if s.negotiations == nil {
		return nil, errNegotiationNotConfigured()
	}
	return s.negotiations.FindByBOQ(ctx, boqVersionID)
}

// ActivateNegotiation flips inactive → active. Per TC-NEG-003 a
// quotation must already exist for the BOQ; we look one up and fail
// fast if not.
func (s *Service) ActivateNegotiation(ctx context.Context, in port.ActivateNegotiationInput) (*domain.Negotiation, error) {
	if s.negotiations == nil {
		return nil, errNegotiationNotConfigured()
	}
	if s.quotations == nil {
		return nil, errQuotationNotConfigured()
	}
	// Quotation existence check (TC-NEG-003).
	if _, err := s.quotations.FindLatestForBOQ(ctx, in.BOQVersionID); err != nil {
		if derrors.IsNotFound(err) {
			return nil, derrors.Conflict(
				"negotiation.quotation_required_first",
				"a quotation must be issued before activating negotiation",
			)
		}
		return nil, err
	}
	// Config must also be enabled.
	cfg, err := s.negotiationConfigs.GetConfig(ctx, in.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, derrors.Conflict(
			"negotiation.disabled",
			"negotiation_enabled is false on this BOQ",
		)
	}
	n, err := s.negotiations.FindByBOQ(ctx, in.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if err := n.Activate(in.ActorUserID); err != nil {
		return nil, err
	}
	if err := s.negotiations.Update(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Service) AbortNegotiation(ctx context.Context, in port.AbortNegotiationInput) (*domain.Negotiation, error) {
	if s.negotiations == nil {
		return nil, errNegotiationNotConfigured()
	}
	n, err := s.negotiations.FindByID(ctx, in.NegotiationID)
	if err != nil {
		return nil, err
	}
	if err := n.Abort(in.Reason); err != nil {
		return nil, err
	}
	if err := s.negotiations.Update(ctx, n); err != nil {
		return nil, err
	}
	// Cascade — flip pending rounds to superseded.
	rounds, _ := s.negotiationRounds.List(ctx, n.ID)
	for i := range rounds {
		round := &rounds[i]
		if round.Status == domain.NegotiationRoundPendingApproval {
			round.Supersede()
			_ = s.negotiationRounds.Update(ctx, round)
		}
	}
	return n, nil
}

// =====================================================================
// Round flow — VP submits, chain approves
// =====================================================================

// SubmitRound is the VP's price-change submission. We:
//
//   1. Verify negotiation is active + actor is the configured VP.
//   2. Load current BOQ + lines; build before/after snapshot from input.
//   3. Compute the would-be margin AFTER all changes apply.
//   4. Decide CCO auto-inject:
//        - margin_after < margin_floor -> auto-inject
//        - any line discount > ceiling -> auto-inject
//   5. If no CCO is needed AND margin is below floor -> 422 (catalog
//      TC-NEG-008): the submission is invalid.
//   6. Materialize the round + approval chain (template participants
//      + optional injected CCO step). DO NOT mutate BOQ line prices
//      yet — those land on ApproveRoundStep when the chain completes.
//
// This way the round is auditable on submit even if approval drags on,
// and rejection doesn't leave half-applied price changes on the BOQ.
func (s *Service) SubmitRound(ctx context.Context, in port.SubmitNegotiationRoundInput) (*domain.NegotiationRound, []domain.NegotiationRoundApproval, error) {
	if s.negotiations == nil || s.boqs == nil {
		return nil, nil, errNegotiationNotConfigured()
	}
	n, err := s.negotiations.FindByID(ctx, in.NegotiationID)
	if err != nil {
		return nil, nil, err
	}
	if n.Status != domain.NegotiationStatusActive {
		return nil, nil, derrors.Conflict(
			"negotiation.not_active",
			"can only submit rounds when negotiation is active",
		)
	}
	cfg, err := s.negotiationConfigs.GetConfig(ctx, n.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	// NG-2: VP-only editor (TC-NEG-004). Pull the VP user from the
	// chain config and assert it matches the actor.
	vp := cfg.FindVP()
	if vp == nil {
		return nil, nil, derrors.Conflict(
			"negotiation.no_vp",
			"chain has no vp_sales participant — config requires one when pricing_adjustment_allowed",
		)
	}
	if vp.UserID != in.ActorUserID {
		return nil, nil, derrors.Forbidden(
			"negotiation.not_pricing_editor",
			"only the configured vp_sales user can submit pricing changes",
		)
	}
	if len(in.Changes) == 0 {
		return nil, nil, derrors.Validation("negotiation_round.no_changes", "at least one change required")
	}

	// Load BOQ + lines for math.
	boq, err := s.boqs.FindByID(ctx, n.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	lines, err := s.boqLines.ListByBOQ(ctx, boq.ID)
	if err != nil {
		return nil, nil, err
	}
	linesByID := map[uuid.UUID]*domain.BOQLine{}
	for i := range lines {
		linesByID[lines[i].ID] = &lines[i]
	}

	// Build the price-change snapshot + compute the AFTER state.
	changes := make([]domain.LinePriceChange, 0, len(in.Changes))
	marginBefore := boq.MarginPct
	// Sum the AFTER sell + cost across all lines to derive margin_after.
	sumSellAfter, sumCostAfter := 0.0, 0.0
	maxDiscountAfter := 0.0
	pendingLineMutations := []func(){}

	for _, c := range in.Changes {
		l := linesByID[c.LineID]
		if l == nil {
			return nil, nil, derrors.Validation(
				"negotiation_round.line_unknown",
				"price_change references a line that doesn't belong to this BOQ",
			)
		}
		changes = append(changes, domain.LinePriceChange{
			LineID:            l.ID,
			BeforeSell:        l.SellUnitPrice,
			AfterSell:         c.NewSellUnitPrice,
			BeforeDiscountPct: l.LineDiscountPct,
			AfterDiscountPct:  c.NewDiscountPct,
		})
		// Defer the actual line mutation until approve — for now, set
		// a temporary copy to compute AFTER totals.
		tmpSell := c.NewSellUnitPrice
		tmpDisc := c.NewDiscountPct
		gross := tmpSell * l.Quantity
		afterSell := gross
		if tmpDisc > 0 {
			afterSell = gross * (1 - tmpDisc/100.0)
		}
		sumSellAfter += afterSell
		// Cost is unchanged.
		if l.VendorUnitCost != nil {
			sumCostAfter += *l.VendorUnitCost * l.Quantity
		}
		if tmpDisc > maxDiscountAfter {
			maxDiscountAfter = tmpDisc
		}
		_ = pendingLineMutations // will populate on approve
	}
	// Add lines untouched by this round to the totals.
	for i := range lines {
		l := &lines[i]
		touched := false
		for _, c := range in.Changes {
			if c.LineID == l.ID {
				touched = true
				break
			}
		}
		if touched {
			continue
		}
		sumSellAfter += l.LineSellTotal()
		sumCostAfter += l.LineCostTotal()
		if l.LineDiscountPct > maxDiscountAfter {
			maxDiscountAfter = l.LineDiscountPct
		}
	}
	marginAfter := 0.0
	if sumSellAfter > 0 {
		marginAfter = (sumSellAfter - sumCostAfter) / sumSellAfter * 100.0
	}

	// Auto-inject decision (NG-2/NG-3/Edge #6).
	injection := domain.EvaluateCCOInjection(
		marginAfter, cfg.MarginFloorPct,
		maxDiscountAfter, cfg.DiscountCeilingPct,
	)
	hasCCO := cfg.FindCCO() != nil
	willInjectCCO := injection != domain.CCOInjectionNone && !hasCCO

	// TC-NEG-008: if margin breaches floor AND no CCO is going to be
	// added to the chain, reject the submit outright.
	if injection == domain.CCOInjectionMarginFloor && !willInjectCCO && !hasCCO {
		return nil, nil, derrors.Validation(
			"negotiation_round.margin_floor_violation",
			"projected margin is below the floor and no CCO is in the chain",
		)
	}

	// Build the round.
	nextRound, err := s.negotiationRounds.HighestRoundNo(ctx, n.ID)
	if err != nil {
		return nil, nil, err
	}
	round, err := domain.NewNegotiationRound(
		n.ID, nextRound+1, changes,
		marginBefore, marginAfter, maxDiscountAfter,
		in.ActorUserID,
	)
	if err != nil {
		return nil, nil, err
	}
	round.CCOInjectionReason = injection
	round.CCOAutoInjected = willInjectCCO
	if err := s.negotiationRounds.Create(ctx, round); err != nil {
		return nil, nil, err
	}

	// Materialize the approval chain: chain participants first, then
	// (if auto-injecting) append CCO as the last step. We look up the
	// CCO user from the chain (if present) or from `cco` participants
	// outside the original ordering. For MVP we require a `cco`
	// participant somewhere in the config to support auto-inject;
	// if absent, the chain proceeds without CCO and rejects on
	// margin breach as above.
	approvals := []domain.NegotiationRoundApproval{}
	for _, p := range cfg.Participants {
		ai, aerr := domain.NewNegotiationRoundApproval(
			round.ID, p.StepNo, p.UserID, p.RoleTag, false,
		)
		if aerr != nil {
			return nil, nil, aerr
		}
		approvals = append(approvals, *ai)
	}
	if willInjectCCO {
		// Find any cco user — could be inside the chain already (which
		// case hasCCO==true and we wouldn't get here) OR a cco-tagged
		// participant we choose to include. For MVP we treat the
		// presence of any participant tagged 'cco' as the injection
		// target; without one, we bail.
		cco := cfg.FindCCO()
		if cco == nil {
			return nil, nil, derrors.Validation(
				"negotiation_round.cco_required_for_inject",
				"margin/discount breach requires CCO injection but no cco participant configured",
			)
		}
		injectedStep := cfg.LastStepNo() + 1
		ai, aerr := domain.NewNegotiationRoundApproval(
			round.ID, injectedStep, cco.UserID, "cco", true,
		)
		if aerr != nil {
			return nil, nil, aerr
		}
		approvals = append(approvals, *ai)
	}
	if err := s.negotiationApprovals.CreateBatch(ctx, approvals); err != nil {
		return nil, nil, err
	}
	return round, approvals, nil
}

// ApproveRoundStep mirrors BOQ approval but operates on the
// negotiation_round_approvals table. When the last step approves,
// the round flips to approved + the BOQ line prices update + a new
// quotation version is generated.
func (s *Service) ApproveRoundStep(ctx context.Context, in port.NegotiationRoundActionInput) (*domain.NegotiationRoundApproval, *domain.NegotiationRound, error) {
	if s.negotiationApprovals == nil {
		return nil, nil, errNegotiationNotConfigured()
	}
	ai, err := s.negotiationApprovals.FindByID(ctx, in.ApprovalID)
	if err != nil {
		return nil, nil, err
	}
	round, err := s.negotiationRounds.FindByID(ctx, ai.RoundID)
	if err != nil {
		return nil, nil, err
	}
	if round.Status != domain.NegotiationRoundPendingApproval {
		return nil, nil, derrors.Conflict(
			"negotiation_round.not_pending",
			"round is no longer pending approval",
		)
	}
	// Sequential gating — load all approvals and check prior steps approved.
	n, err := s.negotiations.FindByID(ctx, round.NegotiationID)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := s.negotiationConfigs.GetConfig(ctx, n.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	chain, err := s.negotiationApprovals.ListByRound(ctx, round.ID)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Mode == domain.ApprovalModeSequential {
		for _, c := range chain {
			if c.StepNo < ai.StepNo && c.Status != domain.ApprovalInstanceStatusApproved {
				return nil, nil, derrors.Conflict(
					"negotiation_round.step_not_ready",
					"prior step has not yet been approved",
				)
			}
		}
	}
	if err := ai.Approve(in.ActorUserID); err != nil {
		return nil, nil, err
	}
	if err := s.negotiationApprovals.Update(ctx, ai); err != nil {
		return nil, nil, err
	}

	// Re-fetch the chain to check completion.
	chain, _ = s.negotiationApprovals.ListByRound(ctx, round.ID)
	allApproved := true
	for _, c := range chain {
		if c.Status != domain.ApprovalInstanceStatusApproved {
			allApproved = false
			break
		}
	}
	if allApproved {
		if err := round.MarkApproved(); err != nil {
			return nil, nil, err
		}
		if err := s.negotiationRounds.Update(ctx, round); err != nil {
			return nil, nil, err
		}
		// Apply the price changes to BOQ lines.
		if err := s.applyRoundToBOQ(ctx, round); err != nil {
			return nil, nil, err
		}
		// Mark negotiation completed + fire re-quote.
		if err := n.MarkCompleted(nil); err != nil {
			return nil, nil, err
		}
		if err := s.negotiations.Update(ctx, n); err != nil {
			return nil, nil, err
		}
		// Re-quote: GenerateQuotation handles v(N+1) automatically.
		if newQ, qerr := s.GenerateQuotation(ctx, port.GenerateQuotationInput{
			BOQVersionID: n.BOQVersionID,
		}); qerr == nil {
			n.ResultingQuotationID = &newQ.ID
			_ = s.negotiations.Update(ctx, n)
		} else if s.log != nil {
			s.log.Warn("auto re-quote on negotiation complete failed",
				"negotiation_id", n.ID.String(),
				"err", qerr.Error(),
			)
		}
	}
	return ai, round, nil
}

// RejectRoundStep — any pending step rejects → round aborts, peer
// pending approvals flip to superseded_reset (Edge #4 parity).
func (s *Service) RejectRoundStep(ctx context.Context, in port.NegotiationRoundActionInput) (*domain.NegotiationRoundApproval, *domain.NegotiationRound, error) {
	if s.negotiationApprovals == nil {
		return nil, nil, errNegotiationNotConfigured()
	}
	ai, err := s.negotiationApprovals.FindByID(ctx, in.ApprovalID)
	if err != nil {
		return nil, nil, err
	}
	round, err := s.negotiationRounds.FindByID(ctx, ai.RoundID)
	if err != nil {
		return nil, nil, err
	}
	if round.Status != domain.NegotiationRoundPendingApproval {
		return nil, nil, derrors.Conflict(
			"negotiation_round.not_pending",
			"round is no longer pending approval",
		)
	}
	if err := ai.Reject(in.ActorUserID, domain.ApprovalReasonCode(in.ReasonCode), in.Comment); err != nil {
		return nil, nil, err
	}
	if err := s.negotiationApprovals.Update(ctx, ai); err != nil {
		return nil, nil, err
	}
	// Roll up to the round.
	if err := round.MarkRejected(domain.RejectionReasonCode(ai.ReasonCode), ai.Comment); err != nil {
		return nil, nil, err
	}
	if err := s.negotiationRounds.Update(ctx, round); err != nil {
		return nil, nil, err
	}
	// Edge #4: peer pending steps → superseded_reset.
	chain, _ := s.negotiationApprovals.ListByRound(ctx, round.ID)
	for _, c := range chain {
		if c.ID == ai.ID {
			continue
		}
		if c.Status == domain.ApprovalInstanceStatusPending {
			c.SupersedeReset()
			_ = s.negotiationApprovals.Update(ctx, &c)
		}
	}
	return ai, round, nil
}

// GetRound returns the round + its approval chain.
func (s *Service) GetRound(ctx context.Context, id uuid.UUID) (*domain.NegotiationRound, []domain.NegotiationRoundApproval, error) {
	if s.negotiationRounds == nil {
		return nil, nil, errNegotiationNotConfigured()
	}
	round, err := s.negotiationRounds.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	chain, err := s.negotiationApprovals.ListByRound(ctx, round.ID)
	if err != nil {
		return nil, nil, err
	}
	return round, chain, nil
}

// ListPendingRoundApprovalsForUser is the inbox query. Nil-safe so the
// HTTP layer can short-circuit to an empty list when Phase 4b isn't
// wired in this deployment.
func (s *Service) ListPendingRoundApprovalsForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.NegotiationRoundApproval, error) {
	if s.negotiationApprovals == nil {
		return []domain.NegotiationRoundApproval{}, nil
	}
	return s.negotiationApprovals.ListPendingForUser(ctx, userID, limit, offset)
}

// applyRoundToBOQ mutates the affected BOQ lines with the round's
// after-state pricing + recomputes the BOQ header totals. Idempotent
// per round (we re-apply on every approval but the math is the same).
func (s *Service) applyRoundToBOQ(ctx context.Context, round *domain.NegotiationRound) error {
	if s.boqs == nil {
		return errBOQNotConfigured()
	}
	n, err := s.negotiations.FindByID(ctx, round.NegotiationID)
	if err != nil {
		return err
	}
	for _, c := range round.PriceChanges {
		line, err := s.boqLines.FindByID(ctx, c.LineID)
		if err != nil {
			return err
		}
		if err := line.SetSellPriceAndDiscount(c.AfterSell, c.AfterDiscountPct); err != nil {
			return err
		}
		if err := s.boqLines.Update(ctx, line); err != nil {
			return err
		}
	}
	// Recompute BOQ header totals against the now-mutated lines.
	boq, err := s.boqs.FindByID(ctx, n.BOQVersionID)
	if err != nil {
		return err
	}
	lines, err := s.boqLines.ListByBOQ(ctx, boq.ID)
	if err != nil {
		return err
	}
	boq.RecomputeHeaderTotals(lines)
	return s.boqs.Update(ctx, boq, nil)
}
