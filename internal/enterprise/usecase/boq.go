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
	"github.com/ion-core/backend/pkg/httpserver"
)

// Compile-time confirmation Service implements the Phase-3 surface.
var _ port.BOQUseCase = (*Service)(nil)

// =====================================================================
// SLA templates (admin surface)
// =====================================================================

func (s *Service) ListSLATemplates(ctx context.Context, activeOnly bool) ([]domain.SLATemplate, error) {
	if s.slaTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	return s.slaTemplates.List(ctx, activeOnly)
}

func (s *Service) GetSLATemplate(ctx context.Context, id uuid.UUID) (*domain.SLATemplate, error) {
	if s.slaTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	return s.slaTemplates.FindByID(ctx, id)
}

func (s *Service) CreateSLATemplate(ctx context.Context, in port.CreateSLATemplateInput) (*domain.SLATemplate, error) {
	if s.slaTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	if existing, _ := s.slaTemplates.FindByKey(ctx, in.Key); existing != nil {
		return nil, derrors.Conflict("sla_template.key_taken", "key already in use")
	}
	t, err := domain.NewSLATemplate(in.Key, in.Name)
	if err != nil {
		return nil, err
	}
	t.Description = in.Description
	if len(in.Details) > 0 {
		t.Details = in.Details
	}
	if err := s.slaTemplates.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) UpdateSLATemplate(ctx context.Context, in port.UpdateSLATemplateInput) (*domain.SLATemplate, error) {
	if s.slaTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	t, err := s.slaTemplates.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Details != nil {
		t.Details = in.Details
	}
	if in.Active != nil {
		t.Active = *in.Active
	}
	if err := s.slaTemplates.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// =====================================================================
// Approval templates (admin surface)
// =====================================================================

func (s *Service) ListApprovalTemplates(ctx context.Context, activeOnly bool) ([]domain.ApprovalTemplate, error) {
	if s.approvalTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	return s.approvalTemplates.List(ctx, activeOnly)
}

func (s *Service) GetApprovalTemplate(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, []domain.ApprovalTemplateMember, error) {
	if s.approvalTemplates == nil {
		return nil, nil, errBOQNotConfigured()
	}
	t, err := s.approvalTemplates.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	members, err := s.approvalTemplates.ListMembers(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, members, nil
}

func (s *Service) CreateApprovalTemplate(ctx context.Context, in port.CreateApprovalTemplateInput) (*domain.ApprovalTemplate, error) {
	if s.approvalTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	if existing, _ := s.approvalTemplates.FindByKey(ctx, in.Key); existing != nil {
		return nil, derrors.Conflict("approval_template.key_taken", "key already in use")
	}
	t, err := domain.NewApprovalTemplate(in.Key, in.Name, domain.ApprovalMode(in.Mode))
	if err != nil {
		return nil, err
	}
	t.Description = in.Description
	if err := validateMembers(in.Members); err != nil {
		return nil, err
	}
	members := membersFromInputs(t.ID, in.Members)
	if err := s.approvalTemplates.Create(ctx, t, members); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) UpdateApprovalTemplate(ctx context.Context, in port.UpdateApprovalTemplateInput) (*domain.ApprovalTemplate, error) {
	if s.approvalTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	t, err := s.approvalTemplates.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Active != nil {
		t.Active = *in.Active
	}
	if err := s.approvalTemplates.Update(ctx, t); err != nil {
		return nil, err
	}
	if in.Members != nil {
		if err := validateMembers(*in.Members); err != nil {
			return nil, err
		}
		members := membersFromInputs(t.ID, *in.Members)
		if err := s.approvalTemplates.ReplaceMembers(ctx, t.ID, members); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (s *Service) PublishApprovalTemplate(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, error) {
	if s.approvalTemplates == nil {
		return nil, errBOQNotConfigured()
	}
	t, err := s.approvalTemplates.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	members, err := s.approvalTemplates.ListMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, derrors.Validation(
			"approval_template.no_members",
			"cannot publish an approval template with no members",
		)
	}
	if err := t.Publish(); err != nil {
		return nil, err
	}
	if err := s.approvalTemplates.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func validateMembers(members []port.ApprovalTemplateMemberInput) error {
	if len(members) == 0 {
		return derrors.Validation(
			"approval_template.members_required",
			"at least one approver is required",
		)
	}
	for _, m := range members {
		if m.UserID == uuid.Nil {
			return derrors.Validation(
				"approval_template.member_user_invalid",
				"each member must have a non-nil user_id",
			)
		}
		if m.StepNo < 1 {
			return derrors.Validation(
				"approval_template.member_step_invalid",
				"step_no must be >= 1",
			)
		}
	}
	return nil
}

func membersFromInputs(templateID uuid.UUID, inputs []port.ApprovalTemplateMemberInput) []domain.ApprovalTemplateMember {
	out := make([]domain.ApprovalTemplateMember, 0, len(inputs))
	now := time.Now().UTC()
	for _, in := range inputs {
		out = append(out, domain.ApprovalTemplateMember{
			ID:         uuid.New(),
			TemplateID: templateID,
			UserID:     in.UserID,
			StepNo:     in.StepNo,
			RoleTag:    in.RoleTag,
			CreatedAt:  now,
		})
	}
	return out
}

// =====================================================================
// BOQ — CRUD
// =====================================================================

func (s *Service) ListBOQs(ctx context.Context, f port.BOQListFilter) ([]domain.BOQ, int, error) {
	if s.boqs == nil {
		return nil, 0, errBOQNotConfigured()
	}
	return s.boqs.List(ctx, f)
}

func (s *Service) GetBOQ(ctx context.Context, id uuid.UUID) (*domain.BOQ, []domain.BOQLine, error) {
	if s.boqs == nil {
		return nil, nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, nil, err
	}
	return b, lines, nil
}

func (s *Service) CreateBOQ(ctx context.Context, in port.CreateBOQInput) (*domain.BOQ, error) {
	if s.boqs == nil {
		return nil, errBOQNotConfigured()
	}
	// Opportunity must exist + be in a stage that allows BOQ creation
	// (Warm or later). Cold opportunities don't have enough signal yet.
	op, err := s.opps.FindByID(ctx, in.OpportunityID)
	if err != nil {
		return nil, err
	}
	if op.Stage == domain.OpportunityStageCold {
		return nil, derrors.Conflict(
			"boq.opportunity_too_early",
			"BOQ creation requires the opportunity to be at least in Warm stage",
		)
	}
	if op.Stage == domain.OpportunityStageLost {
		return nil, derrors.Conflict(
			"boq.opportunity_lost",
			"cannot create BOQs on a Lost opportunity",
		)
	}
	// Pricebook must be published.
	pb, err := s.pricebooks.FindByID(ctx, in.PricebookID)
	if err != nil {
		return nil, err
	}
	if pb.Status != domain.PricebookStatusPublished {
		return nil, derrors.Validation(
			"boq.pricebook_not_published",
			"BOQ must reference a published pricebook",
		)
	}
	b, err := domain.NewBOQ(in.OpportunityID, in.PricebookID)
	if err != nil {
		return nil, err
	}
	b.BOQNumber = domain.GenerateBOQNumber(time.Now())
	b.Notes = in.Notes
	b.CreatedBy = in.CreatedBy
	if err := s.boqs.Create(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) UpdateBOQ(ctx context.Context, in port.UpdateBOQInput) (*domain.BOQ, error) {
	if s.boqs == nil {
		return nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// BR-3: in_approval / approved / superseded / rejected are
	// immutable. Edits are only allowed in draft or revision_draft.
	switch b.Status {
	case domain.BOQStatusDraft, domain.BOQStatusRevisionDraft:
		// ok
	default:
		return nil, derrors.Conflict(
			"boq.state_locked",
			"BOQ is not editable in status "+string(b.Status),
		)
	}
	if in.Notes != nil {
		b.Notes = *in.Notes
	}
	if err := s.boqs.Update(ctx, b, in.IfRevision); err != nil {
		return nil, err
	}
	return b, nil
}

// =====================================================================
// BOQ — line CRUD
// =====================================================================

func (s *Service) CreateBOQLine(ctx context.Context, in port.CreateBOQLineInput) (*domain.BOQLine, error) {
	if s.boqLines == nil {
		return nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, in.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if b.Status != domain.BOQStatusDraft && b.Status != domain.BOQStatusRevisionDraft {
		return nil, derrors.Conflict(
			"boq_line.parent_locked",
			"cannot add lines when BOQ is in status "+string(b.Status),
		)
	}
	// Snapshot from the pricebook line. The line must belong to the
	// pricebook the BOQ is pinned to — otherwise pricing pins make
	// no sense.
	pl, err := s.lines.FindByID(ctx, in.PricebookLineID)
	if err != nil {
		return nil, err
	}
	if pl.PricebookID != b.PricebookID {
		return nil, derrors.Validation(
			"boq_line.pricebook_mismatch",
			"pricebook_line belongs to a different pricebook than the BOQ",
		)
	}
	// SLA template must exist and be active (FK + active flag).
	sla, err := s.slaTemplates.FindByID(ctx, in.SLATemplateID)
	if err != nil {
		return nil, err
	}
	if !sla.Active {
		return nil, derrors.Validation(
			"boq_line.sla_inactive",
			"sla_template is inactive — pick an active one",
		)
	}
	l, err := domain.NewBOQLine(
		in.BOQVersionID, in.PricebookLineID, in.SLATemplateID,
		pl.SKU, pl.Name, pl.Unit,
		pl.BasePrice, pl.MinMarginPct, pl.MaxDiscountPct,
		in.Quantity,
	)
	if err != nil {
		return nil, err
	}
	l.Notes = in.Notes
	l.SortOrder = in.SortOrder
	if err := s.boqLines.Create(ctx, l); err != nil {
		return nil, err
	}
	// Recompute header totals.
	if err := s.recomputeBOQHeader(ctx, b); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) UpdateBOQLine(ctx context.Context, in port.UpdateBOQLineInput) (*domain.BOQLine, error) {
	if s.boqLines == nil {
		return nil, errBOQNotConfigured()
	}
	l, err := s.boqLines.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	b, err := s.boqs.FindByID(ctx, l.BOQVersionID)
	if err != nil {
		return nil, err
	}
	// Edge #2 (E2) — pre-launch: mid-approval price edits are allowed
	// but trigger an approval chain reset. Terminal states (rejected,
	// approved, superseded) stay locked because the chain is already
	// closed.
	if b.Status != domain.BOQStatusDraft &&
		b.Status != domain.BOQStatusRevisionDraft &&
		b.Status != domain.BOQStatusInApproval {
		return nil, derrors.Conflict(
			"boq_line.parent_locked",
			"cannot edit lines when BOQ is in status "+string(b.Status),
		)
	}
	priceTouching := in.SellUnitPrice != nil || in.LineDiscountPct != nil || in.Quantity != nil
	wasInApproval := b.Status == domain.BOQStatusInApproval

	if in.Quantity != nil {
		if *in.Quantity <= 0 {
			return nil, derrors.Validation("boq_line.quantity_invalid", "quantity must be > 0")
		}
		l.Quantity = *in.Quantity
	}
	if in.SellUnitPrice != nil || in.LineDiscountPct != nil {
		sell := l.SellUnitPrice
		if in.SellUnitPrice != nil {
			sell = *in.SellUnitPrice
		}
		disc := l.LineDiscountPct
		if in.LineDiscountPct != nil {
			disc = *in.LineDiscountPct
		}
		if err := l.SetSellPriceAndDiscount(sell, disc); err != nil {
			return nil, err
		}
	}
	if in.AssignedProviderCompanyID != nil {
		// E4: when a provider company is assigned (or changed), start
		// the vendor SLA clock — default 48h to fill in vendor_unit_cost.
		// Only set if not already present (we don't reset on reassign).
		if l.AssignedProviderCompanyID == nil ||
			(in.AssignedProviderCompanyID != nil &&
				*l.AssignedProviderCompanyID != *in.AssignedProviderCompanyID) {
			if l.VendorDueAt == nil && l.VendorUnitCost == nil {
				due := time.Now().UTC().Add(48 * time.Hour)
				l.VendorDueAt = &due
			}
		}
		l.AssignedProviderCompanyID = in.AssignedProviderCompanyID
	}
	if in.ProviderUserID != nil {
		newAssignee := in.ProviderUserID
		// E10: notify the newly-assigned vendor user.
		if newAssignee != nil &&
			(l.ProviderUserID == nil || *l.ProviderUserID != *newAssignee) {
			s.Notify(ctx, domain.NewNotification(
				*newAssignee,
				"boq_line.assigned_to_me",
				"boq_line", l.ID,
				"BOQ line assigned to you for vendor cost",
				"You've been assigned a BOQ line. Submit vendor_unit_cost via the vendor portal.",
				domain.NotificationSeverityInfo,
			))
		}
		l.ProviderUserID = newAssignee
	}
	if in.SLATemplateID != nil {
		sla, err := s.slaTemplates.FindByID(ctx, *in.SLATemplateID)
		if err != nil {
			return nil, err
		}
		if !sla.Active {
			return nil, derrors.Validation("boq_line.sla_inactive", "sla_template is inactive")
		}
		l.SLATemplateID = *in.SLATemplateID
	}
	if in.Notes != nil {
		l.Notes = *in.Notes
	}
	if in.SortOrder != nil {
		l.SortOrder = *in.SortOrder
	}
	if err := s.boqLines.Update(ctx, l); err != nil {
		return nil, err
	}
	if err := s.recomputeBOQHeader(ctx, b); err != nil {
		return nil, err
	}
	// Edge #2 — if the BOQ was already in_approval and we touched a
	// price-relevant field, reset the chain so prior approvals can't
	// carry into a different commercial state.
	if wasInApproval && priceTouching {
		if err := s.resetApprovalChain(ctx, b, "pricing_changed"); err != nil {
			if s.log != nil {
				s.log.Warn("approval chain reset failed",
					"boq_version_id", b.ID.String(),
					"err", err.Error(),
				)
			}
		}
	}
	return l, nil
}

// resetApprovalChain implements Edge #2: when a BOQ in `in_approval`
// has its commercial state changed, the chain has to start over so
// no previously-approved step can carry into a different number set.
//
// Mechanics:
//   1. Flip every existing instance (any status) to superseded_reset
//      with reason recorded.
//   2. Re-materialize a fresh `pending` chain from the same template
//      (mirrors the original Submit path).
//   3. Fire notifications to every fresh-pending approver so they know
//      to re-evaluate.
//
// The BOQ stays at `in_approval` — only the chain underneath is rebuilt.
func (s *Service) resetApprovalChain(ctx context.Context, b *domain.BOQ, reason string) error {
	if s.approvalInstances == nil || s.approvalTemplates == nil {
		return errBOQNotConfigured()
	}
	if b.ApprovalTemplateID == nil {
		return derrors.Conflict("boq.no_template", "BOQ has no approval template")
	}
	// 1. Flag existing instances.
	chain, err := s.approvalInstances.ListByBOQ(ctx, b.ID)
	if err != nil {
		return err
	}
	for i := range chain {
		c := &chain[i]
		_ = c.SupersedeResetWithReason(reason)
		if err := s.approvalInstances.Update(ctx, c); err != nil {
			return err
		}
	}

	// 2. Re-materialize.
	if _, err := s.approvalTemplates.FindByID(ctx, *b.ApprovalTemplateID); err != nil {
		return err
	}
	members, err := s.approvalTemplates.ListMembers(ctx, *b.ApprovalTemplateID)
	if err != nil {
		return err
	}
	fresh := make([]domain.ApprovalInstance, 0, len(members))
	for _, m := range members {
		inst, ierr := domain.NewApprovalInstance(b.ID, *b.ApprovalTemplateID, m.StepNo, m.UserID, m.RoleTag)
		if ierr != nil {
			return ierr
		}
		fresh = append(fresh, *inst)
	}
	if err := s.approvalInstances.CreateBatch(ctx, fresh); err != nil {
		return err
	}

	// 3. Notify the freshly-pending approvers.
	for _, inst := range fresh {
		s.Notify(ctx, domain.NewNotification(
			inst.ApproverUserID,
			"boq.approval_reset",
			"boq",
			b.ID,
			"BOQ approval re-issued",
			"The BOQ commercial state changed; the chain was reset and your approval step is pending again.",
			domain.NotificationSeverityWarn,
		))
	}
	return nil
}

func (s *Service) DeleteBOQLine(ctx context.Context, id uuid.UUID) error {
	if s.boqLines == nil {
		return errBOQNotConfigured()
	}
	l, err := s.boqLines.FindByID(ctx, id)
	if err != nil {
		return err
	}
	b, err := s.boqs.FindByID(ctx, l.BOQVersionID)
	if err != nil {
		return err
	}
	if b.Status != domain.BOQStatusDraft && b.Status != domain.BOQStatusRevisionDraft {
		return derrors.Conflict(
			"boq_line.parent_locked",
			"cannot delete lines when BOQ is in status "+string(b.Status),
		)
	}
	if err := s.boqLines.Delete(ctx, id); err != nil {
		return err
	}
	return s.recomputeBOQHeader(ctx, b)
}

// SetVendorCost is the vendor-scoped endpoint. The actor must be
// the provider_user_id on the line (TC-IV-004 / BR-2 vendor isolation).
func (s *Service) SetVendorCost(ctx context.Context, in port.SetVendorCostInput) (*domain.BOQLine, error) {
	if s.boqLines == nil {
		return nil, errBOQNotConfigured()
	}
	l, err := s.boqLines.FindByID(ctx, in.LineID)
	if err != nil {
		return nil, err
	}
	// Vendor isolation — the actor must be the assigned vendor user.
	if l.ProviderUserID == nil || *l.ProviderUserID != in.ActorUserID {
		return nil, derrors.Forbidden(
			"boq_line.not_vendor",
			"only the assigned vendor user can set vendor cost on this line",
		)
	}
	// Parent BOQ must still be in draft (vendor can only input cost
	// during the build phase — TC-IV-005 state_lock).
	b, err := s.boqs.FindByID(ctx, l.BOQVersionID)
	if err != nil {
		return nil, err
	}
	if b.Status != domain.BOQStatusDraft && b.Status != domain.BOQStatusRevisionDraft {
		return nil, derrors.Conflict(
			"boq.state_locked",
			"vendor cost cannot be set when BOQ is in status "+string(b.Status),
		)
	}
	if err := l.SetVendorCost(in.VendorUnitCost); err != nil {
		return nil, err
	}
	// E4: clear the vendor SLA window once cost is provided.
	l.VendorDueAt = nil
	if err := s.boqLines.Update(ctx, l); err != nil {
		return nil, err
	}
	if err := s.recomputeBOQHeader(ctx, b); err != nil {
		return nil, err
	}
	return l, nil
}

// recomputeBOQHeader re-runs the weighted-margin math given the
// current line set + persists the BOQ row. Called after any line
// mutation. No optimistic concurrency check here — the totals are
// derived and re-runnable; we don't want the FE to need to refresh
// the BOQ revision after every line edit.
func (s *Service) recomputeBOQHeader(ctx context.Context, b *domain.BOQ) error {
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return err
	}
	b.RecomputeHeaderTotals(lines)
	return s.boqs.Update(ctx, b, nil)
}

// =====================================================================
// Submit / approve / reject
// =====================================================================

// SubmitBOQ validates the BOQ + materializes the approval chain in one
// transactional step. The hash is computed inside the function so
// the snapshot freeze and the chain materialization happen against
// the same state.
func (s *Service) SubmitBOQ(ctx context.Context, in port.SubmitBOQInput) (*domain.BOQ, []domain.ApprovalInstance, error) {
	if s.boqs == nil {
		return nil, nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, in.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, nil, err
	}
	tpl, err := s.approvalTemplates.FindByID(ctx, in.ApprovalTemplateID)
	if err != nil {
		return nil, nil, err
	}
	if !tpl.Active || tpl.PublishedAt == nil {
		return nil, nil, derrors.Validation(
			"approval_template.not_pickable",
			"approval_template must be active AND published",
		)
	}
	members, err := s.approvalTemplates.ListMembers(ctx, tpl.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(members) == 0 {
		return nil, nil, derrors.Validation(
			"approval_template.no_members",
			"approval_template has no members",
		)
	}
	hash, err := domain.ComputeSnapshotHash(b, lines)
	if err != nil {
		return nil, nil, err
	}
	// Domain validates everything (margin floor, provider assignment,
	// state transition) — fail fast if any guardrail trips.
	if err := b.Submit(lines, tpl.ID, hash); err != nil {
		return nil, nil, err
	}
	instances := make([]domain.ApprovalInstance, 0, len(members))
	for _, m := range members {
		ai, err := domain.NewApprovalInstance(b.ID, tpl.ID, m.StepNo, m.UserID, m.RoleTag)
		if err != nil {
			return nil, nil, err
		}
		instances = append(instances, *ai)
	}
	if err := s.boqs.Update(ctx, b, in.IfRevision); err != nil {
		return nil, nil, err
	}
	if err := s.approvalInstances.CreateBatch(ctx, instances); err != nil {
		return nil, nil, err
	}
	// Bump line statuses to in_approval — informational, not gating.
	for i := range lines {
		l := &lines[i]
		l.Status = domain.BOQLineStatusInApproval
		_ = s.boqLines.Update(ctx, l)
	}
	// E10 — notify every fresh-pending approver. They go to /enterprise/approvals.
	for _, inst := range instances {
		s.Notify(ctx, domain.NewNotification(
			inst.ApproverUserID,
			"boq.approval_pending",
			"boq", b.ID,
			"BOQ "+b.BOQNumber+" — your approval is pending",
			"A BOQ has been submitted with you on the approval chain. Open the inbox to act.",
			domain.NotificationSeverityInfo,
		))
	}
	// Audit: record submission. SubmitBOQInput doesn't carry the actor
	// (the JWT-derived user lands one level up); leave user_id NULL.
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "boq",
		RecordID:     b.ID.String(),
		FieldChanged: "status",
		Before:       string(domain.BOQStatusDraft),
		After:        string(domain.BOQStatusInApproval),
		Reason:       "submit_for_approval",
	})
	return b, instances, nil
}

// withApprovalLock wraps fn in a postgres advisory lock keyed on the
// approval-instance id. When s.lockPool is nil (legacy wiring), fn
// runs unwrapped — same semantics as pre-Wave-106. A concurrent caller
// observes derrors.Conflict("lock.contended", ...).
//
// Wave 106 — retrofit of the Wave 104 pkg/httpserver/advisory_lock.go
// helper. The catalog's TC-EDGE-002 "Parallel Concurrent Approvers"
// requires the same approval-instance ID can't be acted on by two
// goroutines at the same time.
func (s *Service) withApprovalLock(ctx context.Context, approvalID uuid.UUID, fn func(ctx context.Context) error) error {
	if s.lockPool == nil {
		return fn(ctx)
	}
	return httpserver.WithAdvisoryLock(ctx, s.lockPool, httpserver.LockKeyForApproval(approvalID), fn)
}

// ApproveStep handles a single approval action. Sequential templates
// require step N to be approved before step N+1 becomes actionable;
// parallel templates accept actions in any order.
//
// When the last required step approves, the BOQ flips to boq_approved
// and any prior approved version of the same boq_number is superseded.
//
// Wave 106 — wrapped in a postgres advisory lock keyed on the
// approval-instance id (TC-EDGE-002 parallel concurrent approvers).
// Lock is nil-safe when the pool isn't wired (legacy deployments).
func (s *Service) ApproveStep(ctx context.Context, in port.ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error) {
	var (
		retAI  *domain.ApprovalInstance
		retBOQ *domain.BOQ
	)
	err := s.withApprovalLock(ctx, in.InstanceID, func(ctx context.Context) error {
		ai, b, ierr := s.approveStepInner(ctx, in)
		retAI = ai
		retBOQ = b
		return ierr
	})
	return retAI, retBOQ, err
}

// approveStepInner is the original ApproveStep body — the part that
// performs the actual state transition. Extracted so the public
// ApproveStep wrapper can hold the advisory lock around it.
func (s *Service) approveStepInner(ctx context.Context, in port.ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error) {
	if s.approvalInstances == nil {
		return nil, nil, errBOQNotConfigured()
	}
	ai, err := s.approvalInstances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.boqs.FindByID(ctx, ai.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	if b.Status != domain.BOQStatusInApproval {
		return nil, nil, derrors.Conflict(
			"approval.boq_not_in_approval",
			"BOQ is no longer in_approval — action ignored",
		)
	}
	tpl, err := s.approvalTemplates.FindByID(ctx, ai.TemplateID)
	if err != nil {
		return nil, nil, err
	}
	// Sequential templates gate step N+1 on step N being approved.
	if tpl.Mode == domain.ApprovalModeSequential {
		if err := s.assertSequentialReady(ctx, b.ID, ai); err != nil {
			return nil, nil, err
		}
	}
	if err := ai.Approve(in.ActorUserID); err != nil {
		return nil, nil, err
	}
	if err := s.approvalInstances.Update(ctx, ai); err != nil {
		return nil, nil, err
	}
	// Check whether the whole chain is now complete.
	chain, err := s.approvalInstances.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, nil, err
	}
	allApproved := true
	for _, c := range chain {
		if c.Status != domain.ApprovalInstanceStatusApproved {
			allApproved = false
			break
		}
	}
	if allApproved {
		// Wave 101 — stamp the tax_profile + snapshot hash BEFORE
		// MarkApproved so the snapshot is part of the same "approved
		// state" write. Best-effort + nil-safe: when no resolver is
		// wired or no subsidiary is yet known for this BOQ (the common
		// case at approval time, since the customer PO carrying the
		// commercial_owner_subsidiary_id hasn't been received), we skip
		// silently. The IC-PO acceptance path re-stamps with the
		// correct subsidiary; the reconciliation cron flags any BOQ
		// that flips to approved without a snapshot for operator
		// review.
		s.stampBOQTaxSnapshotIfResolvable(ctx, b)
		if err := b.MarkApproved(); err != nil {
			return nil, nil, err
		}
		if err := s.boqs.Update(ctx, b, nil); err != nil {
			return nil, nil, err
		}
		// Supersede any prior approved version under the same boq_number.
		if err := s.supersedePriorVersions(ctx, b); err != nil {
			return nil, nil, err
		}
		// Bump line statuses to approved.
		lines, _ := s.boqLines.ListByBOQ(ctx, b.ID)
		for i := range lines {
			l := &lines[i]
			l.Status = domain.BOQLineStatusApproved
			_ = s.boqLines.Update(ctx, l)
		}
		// Phase 4a hook: chain complete → auto-generate the quotation
		// PDF. Best-effort; failures are logged but don't roll back
		// the approval (the operator can manually regenerate via
		// POST /quotations later).
		s.AutoGenerateQuotationOnApproval(ctx, b.ID)
		// Phase 4b hook: lock the negotiation config (TC-NEG-002) and
		// seed an inactive Negotiation row so the VP can later activate.
		// Idempotent + nil-safe: returns immediately when WithNegotiation
		// wasn't called.
		if err := s.LockConfigOnApproval(ctx, b.ID); err != nil {
			if s.log != nil {
				s.log.Warn("negotiation lock-on-approval failed",
					"boq_version_id", b.ID.String(),
					"err", err.Error(),
				)
			}
		}
		// Audit: BOQ approved (terminal positive state).
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			UserID:       in.ActorUserID,
			Module:       "enterprise",
			RecordType:   "boq",
			RecordID:     b.ID.String(),
			FieldChanged: "status",
			After:        string(domain.BOQStatusApproved),
			Reason:       "chain_complete",
		})
		// internal_transactions hook lands here too (next todo).
		// DEPRECATED (Wave 95): this call site is the legacy recognition
		// trigger. The canonical trigger is `AcceptIntercompanyPO` →
		// `recordInternalTransactionsOnICPOAccept`. Kept live to avoid
		// breaking existing reports; reconciliation cron in Wave 95b.
		if err := s.recordInternalTransactionsOnApproval(ctx, b); err != nil {
			if s.log != nil {
				s.log.Warn("internal_transactions write failed",
					"boq_version_id", b.ID.String(), "err", err.Error())
			}
		}
	}
	// Audit: per-step action.
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       in.ActorUserID,
		Module:       "enterprise",
		RecordType:   "approval_instance",
		RecordID:     ai.ID.String(),
		FieldChanged: "status",
		After:        string(ai.Status),
		Reason:       "approve_step",
	})
	return ai, b, nil
}

// RejectStep flips a pending instance to rejected and rolls up to the
// BOQ — first rejection aborts the round. Per CPQ Edge #4 (parallel
// reject) we also flip peer pending instances to superseded_reset
// so the audit trail captures that they were preempted by the
// rejection rather than just left dangling.
//
// Wave 106 — same advisory-lock wrap as ApproveStep so two concurrent
// callers can't act on the same approval-instance ID simultaneously
// (TC-EDGE-002 parallel concurrent approvers).
func (s *Service) RejectStep(ctx context.Context, in port.ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error) {
	var (
		retAI  *domain.ApprovalInstance
		retBOQ *domain.BOQ
	)
	err := s.withApprovalLock(ctx, in.InstanceID, func(ctx context.Context) error {
		ai, b, ierr := s.rejectStepInner(ctx, in)
		retAI = ai
		retBOQ = b
		return ierr
	})
	return retAI, retBOQ, err
}

func (s *Service) rejectStepInner(ctx context.Context, in port.ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error) {
	if s.approvalInstances == nil {
		return nil, nil, errBOQNotConfigured()
	}
	ai, err := s.approvalInstances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.boqs.FindByID(ctx, ai.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}
	if b.Status != domain.BOQStatusInApproval {
		return nil, nil, derrors.Conflict(
			"approval.boq_not_in_approval",
			"BOQ is no longer in_approval — action ignored",
		)
	}
	if err := ai.Reject(in.ActorUserID, domain.ApprovalReasonCode(in.ReasonCode), in.Comment); err != nil {
		return nil, nil, err
	}
	if err := s.approvalInstances.Update(ctx, ai); err != nil {
		return nil, nil, err
	}
	// Roll up to BOQ — pick the rejection reason from this step.
	if err := b.MarkRejected(domain.RejectionReasonCode(ai.ReasonCode), ai.Comment); err != nil {
		return nil, nil, err
	}
	if err := s.boqs.Update(ctx, b, nil); err != nil {
		return nil, nil, err
	}
	// Edge #4: peer pending instances become superseded_reset so the
	// audit retains the round-aborted signal.
	chain, err := s.approvalInstances.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range chain {
		if c.ID == ai.ID {
			continue
		}
		if c.Status == domain.ApprovalInstanceStatusPending {
			_ = c.SupersedeReset()
			_ = s.approvalInstances.Update(ctx, &c)
		}
	}
	return ai, b, nil
}

// ReassignStep hands a still-pending step off to a different user.
// Edge #3 / E3: when the assigned approver is unavailable (PTO, role
// change), an admin with `enterprise.approval.reassign` can swap the
// approver_user_id without breaking the chain. The previous assignee
// loses access; the new assignee gets a notification + an inbox entry.
//
// Constraints:
//   - the step must be pending (approved / rejected / superseded can't
//     be moved — the audit trail must be preserved)
//   - reason is required (records WHY in approval_instance.comment for
//     audit, prefixed with "reassign:" so it's distinguishable from a
//     reject-comment)
func (s *Service) ReassignStep(ctx context.Context, in port.ReassignStepInput) (*domain.ApprovalInstance, error) {
	if s.approvalInstances == nil {
		return nil, errBOQNotConfigured()
	}
	if in.NewApprover == uuid.Nil {
		return nil, derrors.Validation(
			"approval.reassign_user_required",
			"new approver user id is required",
		)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, derrors.Validation(
			"approval.reassign_reason_required",
			"reassign reason is required for audit",
		)
	}
	ai, err := s.approvalInstances.FindByID(ctx, in.InstanceID)
	if err != nil {
		return nil, err
	}
	if ai.Status != domain.ApprovalInstanceStatusPending {
		return nil, derrors.Conflict(
			"approval.not_pending",
			"only pending steps can be reassigned",
		)
	}
	if ai.ApproverUserID == in.NewApprover {
		return nil, derrors.Validation(
			"approval.reassign_same_user",
			"new approver is the same as the current approver",
		)
	}
	prevApprover := ai.ApproverUserID
	ai.ApproverUserID = in.NewApprover
	if ai.Comment != "" {
		ai.Comment = "reassign: " + in.Reason + " | " + ai.Comment
	} else {
		ai.Comment = "reassign: " + in.Reason
	}
	if err := s.approvalInstances.Update(ctx, ai); err != nil {
		return nil, err
	}
	// Notify new approver + the previous one.
	s.Notify(ctx, domain.NewNotification(
		in.NewApprover,
		"boq.approval_reassigned_to_me",
		"approval_instance", ai.ID,
		"Approval step reassigned to you",
		"You were assigned an approval step previously held by another user. Reason: "+in.Reason,
		domain.NotificationSeverityInfo,
	))
	s.Notify(ctx, domain.NewNotification(
		prevApprover,
		"boq.approval_reassigned_away",
		"approval_instance", ai.ID,
		"Approval step reassigned away",
		"An admin has reassigned this approval step to another user. Reason: "+in.Reason,
		domain.NotificationSeverityInfo,
	))
	return ai, nil
}

// assertSequentialReady ensures every prior step (step_no < this) is
// approved before this step can act. Used only for sequential
// templates — parallel templates skip the check.
func (s *Service) assertSequentialReady(ctx context.Context, boqID uuid.UUID, ai *domain.ApprovalInstance) error {
	chain, err := s.approvalInstances.ListByBOQ(ctx, boqID)
	if err != nil {
		return err
	}
	for _, c := range chain {
		if c.StepNo < ai.StepNo && c.Status != domain.ApprovalInstanceStatusApproved {
			return derrors.Conflict(
				"approval.step_not_ready",
				"prior step has not yet been approved",
			)
		}
	}
	return nil
}

func (s *Service) supersedePriorVersions(ctx context.Context, current *domain.BOQ) error {
	// Walk down version_no for the same boq_number; flip any prior
	// boq_approved row to superseded.
	for v := current.VersionNo - 1; v >= 1; v-- {
		// We don't index by (boq_number, version_no) on the read path
		// directly; List filter + post-filter is fine because BOQs
		// rarely have many versions.
		boqs, _, err := s.boqs.List(ctx, port.BOQListFilter{
			Search: current.BOQNumber,
			Limit:  100,
		})
		if err != nil {
			return err
		}
		for i := range boqs {
			b := &boqs[i]
			if b.BOQNumber == current.BOQNumber &&
				b.VersionNo == v &&
				b.Status == domain.BOQStatusApproved {
				_ = b.Supersede()
				_ = s.boqs.Update(ctx, b, nil)
			}
		}
	}
	return nil
}

// cascadeApprovalChainReset is the Wave 106 polish for TC-AP-* —
// when a BOQ transitions to revision_draft, any approval instance
// that's still in flight (pending) on the OLD version should flip to
// superseded_reset so the audit trail shows the chain was preempted
// by the revision rather than left dangling. Idempotent: terminal
// statuses (approved/rejected/already-superseded) skip silently.
func (s *Service) cascadeApprovalChainReset(ctx context.Context, boqID uuid.UUID, reason string) {
	if s.approvalInstances == nil {
		return
	}
	chain, err := s.approvalInstances.ListByBOQ(ctx, boqID)
	if err != nil {
		return
	}
	for i := range chain {
		c := &chain[i]
		if c.Status != domain.ApprovalInstanceStatusPending {
			continue
		}
		if err := c.SupersedeResetWithReason(reason); err != nil {
			continue
		}
		if err := s.approvalInstances.Update(ctx, c); err != nil {
			if s.log != nil {
				s.log.Warn("cascade approval chain reset failed",
					"approval_instance_id", c.ID.String(),
					"err", err.Error(),
				)
			}
			continue
		}
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:       "enterprise",
			RecordType:   "approval_instance",
			RecordID:     c.ID.String(),
			FieldChanged: "status",
			Before:       string(domain.ApprovalInstanceStatusPending),
			After:        string(domain.ApprovalInstanceStatusSupersededReset),
			Reason:       reason,
		})
	}
}

// StartRevision spawns a revision_draft from a rejected BOQ. The
// rejected row stays put (immutable) — the revision_draft is the
// editable copy. On resubmit it bumps version_no to N+1.
func (s *Service) StartRevision(ctx context.Context, boqID uuid.UUID) (*domain.BOQ, error) {
	if s.boqs == nil {
		return nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, boqID)
	if err != nil {
		return nil, err
	}
	if b.Status != domain.BOQStatusRejected {
		return nil, derrors.Conflict(
			"boq.cannot_revise",
			"only rejected BOQs can start a revision",
		)
	}
	// Clone the BOQ + its lines into a new row with version_no+1.
	highest, err := s.boqs.FindHighestVersion(ctx, b.BOQNumber)
	if err != nil {
		return nil, err
	}
	nextVersion := highest.VersionNo + 1
	clone := *b
	clone.ID = uuid.New()
	clone.VersionNo = nextVersion
	clone.Status = domain.BOQStatusRevisionDraft
	clone.SnapshotHash = ""
	clone.ApprovalTemplateID = nil
	clone.SubmittedAt = nil
	clone.ApprovedAt = nil
	clone.RejectedAt = nil
	clone.SupersededAt = nil
	clone.RejectionReasonCode = domain.RejectionReasonNone
	clone.RejectionComment = ""
	clone.Revision = 1
	now := time.Now().UTC()
	clone.CreatedAt = now
	clone.UpdatedAt = now
	if err := s.boqs.Create(ctx, &clone); err != nil {
		return nil, err
	}
	// Clone the lines too.
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		nl := l
		nl.ID = uuid.New()
		nl.BOQVersionID = clone.ID
		nl.Status = domain.BOQLineStatusHasCost
		nl.CreatedAt = now
		nl.UpdatedAt = now
		if err := s.boqLines.Create(ctx, &nl); err != nil {
			return nil, err
		}
	}
	// Phase 4b hook (Edge #1): if the rejected BOQ has an active
	// negotiation with a pending round, supersede that round so the
	// new revision starts with a clean slate. Best-effort + nil-safe.
	if err := s.SupersedeOnBOQRevision(ctx, b.ID); err != nil {
		if s.log != nil {
			s.log.Warn("negotiation supersede-on-revision failed",
				"boq_version_id", b.ID.String(),
				"err", err.Error(),
			)
		}
	}
	// Wave 106 — cascade any still-pending approval instances on the
	// OLD version to superseded_reset. Pure defense for the cases where
	// a pending instance leaked past the rejection roll-up.
	s.cascadeApprovalChainReset(ctx, b.ID, "boq.revision_started")
	return &clone, nil
}

// EditBOQAfterQuotation is the Wave 106 TC-BQ-014 "Material Edit New
// Version" path. When the operator wants to edit a BOQ that already
// has a quotation issued for it, we DON'T mutate the approved row in
// place — that would silently invalidate the customer-facing
// quotation. Instead we auto-supersede:
//
//   - clone the current BOQ + its lines into a fresh revision_draft
//     row at version_no = highest+1
//   - apply the line edits to the clone (currently a no-op stub;
//     callers can mutate via the regular UpdateBOQLine path on the
//     returned BOQ.ID)
//   - flip the OLD approved BOQ row to superseded
//   - audit-row both versions so the chain is traceable
//
// When there's NO quotation yet for the BOQ, the usecase falls back
// to the StartRevision path (which only fires on rejected BOQs) —
// callers can still go through UpdateBOQLine on the current row.
//
// Returns the new revision_draft BOQ. The caller drives line edits
// via the regular per-line mutation endpoints; this method is just
// the "spawn the new version" hook.
func (s *Service) EditBOQAfterQuotation(ctx context.Context, boqID uuid.UUID) (*domain.BOQ, error) {
	if s.boqs == nil {
		return nil, errBOQNotConfigured()
	}
	b, err := s.boqs.FindByID(ctx, boqID)
	if err != nil {
		return nil, err
	}
	// Material-edit only makes sense on already-approved BOQs (the
	// post-quotation-issuance window). For draft/revision-draft, the
	// regular UpdateBOQLine path handles edits in place.
	if b.Status != domain.BOQStatusApproved {
		return nil, derrors.Conflict(
			"boq.material_edit_not_approved",
			"material edit-via-supersede requires the BOQ to be in boq_approved state",
		)
	}
	// Check whether a quotation exists for this BOQ — if not, the
	// caller should use StartRevision after a rejection instead.
	if s.quotations != nil {
		if _, qerr := s.quotations.FindLatestForBOQ(ctx, b.ID); qerr != nil {
			if derrors.IsNotFound(qerr) {
				return nil, derrors.Conflict(
					"boq.material_edit_no_quotation",
					"no quotation issued yet — edit in place via the line PATCH endpoints",
				)
			}
			return nil, qerr
		}
	}
	// Clone the BOQ + lines into a new version (mirrors StartRevision's
	// mechanics but doesn't gate on `rejected` state).
	highest, err := s.boqs.FindHighestVersion(ctx, b.BOQNumber)
	if err != nil {
		return nil, err
	}
	nextVersion := highest.VersionNo + 1
	clone := *b
	clone.ID = uuid.New()
	clone.VersionNo = nextVersion
	clone.Status = domain.BOQStatusRevisionDraft
	clone.SnapshotHash = ""
	clone.ApprovalTemplateID = nil
	clone.SubmittedAt = nil
	clone.ApprovedAt = nil
	clone.RejectedAt = nil
	clone.SupersededAt = nil
	clone.RejectionReasonCode = domain.RejectionReasonNone
	clone.RejectionComment = ""
	clone.Revision = 1
	// Tax snapshot stays nil on the clone — the next approval re-stamps.
	clone.TaxProfileID = nil
	clone.TaxSnapshotHash = nil
	now := time.Now().UTC()
	clone.CreatedAt = now
	clone.UpdatedAt = now
	if err := s.boqs.Create(ctx, &clone); err != nil {
		return nil, err
	}
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		nl := l
		nl.ID = uuid.New()
		nl.BOQVersionID = clone.ID
		nl.Status = domain.BOQLineStatusHasCost
		nl.CreatedAt = now
		nl.UpdatedAt = now
		if err := s.boqLines.Create(ctx, &nl); err != nil {
			return nil, err
		}
	}
	// Flip the old approved row to superseded.
	if err := b.Supersede(); err != nil {
		return nil, err
	}
	if err := s.boqs.Update(ctx, b, nil); err != nil {
		return nil, err
	}
	// Wave 106 — cascade pending approvals (defensive).
	s.cascadeApprovalChainReset(ctx, b.ID, "boq.material_edit_supersede")
	// Audit-row both versions so the supersede chain is traceable.
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "boq",
		RecordID:     b.ID.String(),
		FieldChanged: "status",
		Before:       string(domain.BOQStatusApproved),
		After:        string(domain.BOQStatusSuperseded),
		Reason:       "material_edit_new_version",
	})
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "boq",
		RecordID:     clone.ID.String(),
		FieldChanged: "status",
		Before:       "",
		After:        string(domain.BOQStatusRevisionDraft),
		Reason:       "material_edit_supersede_from:" + b.ID.String(),
	})
	return &clone, nil
}

// =====================================================================
// Approval instance queries
// =====================================================================

func (s *Service) ListApprovalInstances(ctx context.Context, f port.ApprovalInstanceListFilter) ([]domain.ApprovalInstance, error) {
	if s.approvalInstances == nil {
		return nil, errBOQNotConfigured()
	}
	return s.approvalInstances.List(ctx, f)
}

// Compile-time confirmation that we use `strings` somewhere — keeps
// goimports happy when the package is extended further later.
var _ = strings.Builder{}
