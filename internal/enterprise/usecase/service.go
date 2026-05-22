// Package usecase wires the enterprise bounded context together.
//
// Service depends only on the port interfaces, never on the postgres
// adapters directly — that's what lets the bounded context move to
// its own service binary later without touching domain rules.
package usecase

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Service is the enterprise UseCase implementation.
//
// Audit writes go through a pkg/audit.Writer (defaults to Nop when
// unwired). Each mutating use-case method calls audit.SafeWrite with a
// canonical entry so the admin audit viewer renders the change without
// the call site needing to know about the audit_logs table.
type Service struct {
	pricebooks port.PricebookRepository
	lines      port.PricebookLineRepository
	opps       port.OpportunityRepository

	// Phase 3 — optional, nil-safe so Phase 2 wiring keeps compiling.
	// Methods on the BOQ surface return clear "not configured" errors
	// when these are nil; cmd/enterprise-svc/main.go always wires them.
	slaTemplates       port.SLATemplateRepository
	approvalTemplates  port.ApprovalTemplateRepository
	approvalInstances  port.ApprovalInstanceRepository
	boqs               port.BOQRepository
	boqLines           port.BOQLineRepository
	// Phase 4a — quotation repo, also optional; the auto-generate hook
	// in ApproveStep is a no-op when this is nil.
	quotations port.QuotationRepository

	// Phase 4b — Negotiation repos. All four nil-safe; methods on the
	// negotiation surface return errNegotiationNotConfigured when any
	// one is missing. cmd/enterprise-svc/main.go wires all four together.
	negotiationConfigs   port.NegotiationConfigRepository
	negotiations         port.NegotiationRepository
	negotiationRounds    port.NegotiationRoundRepository
	negotiationApprovals port.NegotiationRoundApprovalRepository

	// Phase 5 — Finance + EWO repos. Nil-safe; methods surface
	// errFinanceNotConfigured when missing. AutoCreateOnQuotationAccept
	// short-circuits when these are nil so the quotation-accept path
	// still works in a Phase 5-less deployment.
	invoices        port.InvoiceRepository
	invoicePayments port.InvoicePaymentRepository
	ewos            port.EWORepository

	// Pre-launch E10 — notifications. Nil-safe; Notify() is a no-op
	// when missing, so every upstream caller can call s.Notify(...)
	// regardless of deployment topology.
	notifications     port.NotificationRepository
	notificationPrefs port.NotificationPrefRepository

	// Audit writer — append-only log of mutating actions. Falls back
	// to a Nop if WithAudit isn't called.
	audit audit.Writer

	// Sub-company revenue ledger (PRD §7.3). Optional — when nil the
	// BOQ approval hook silently skips recording.
	internalTxs port.InternalTransactionRepository

	// Pre-launch E5 — termin / invoice plan repo.
	invoicePlans port.InvoicePlanRepository
	// Pre-launch E7 — PO document storage.
	poDocuments port.PODocumentRepository
	// Pre-launch E8 — payment proof attachments.
	paymentProofs port.PaymentProofRepository
	// Pre-launch E9 — EWO checklist items.
	ewoChecklist port.EWOChecklistRepository
	// Polish — reusable seed templates.
	ewoChecklistTemplates port.EWOChecklistTemplateRepository
	// Pre-launch E11 — projects / sites / services.
	projects         port.ProjectRepository
	projectSites     port.ProjectSiteRepository
	enterpriseSvcs   port.EnterpriseServiceRepository
	// Pre-launch E12 — RFQ.
	rfqs port.RFQRepository

	log *slog.Logger
}

func NewService(
	pricebooks port.PricebookRepository,
	lines port.PricebookLineRepository,
	opps port.OpportunityRepository,
	log *slog.Logger,
) *Service {
	return &Service{
		pricebooks: pricebooks, lines: lines, opps: opps,
		log:   log,
		audit: audit.Nop{}, // default no-op — wired via WithAudit
	}
}

// WithAudit attaches the audit writer. Every mutating use-case method
// fires `audit.SafeWrite` when this is wired; without it the calls are
// silent no-ops.
func (s *Service) WithAudit(w audit.Writer) *Service {
	if w != nil {
		s.audit = w
	}
	return s
}

// WithInternalTransactions attaches the sub-company revenue ledger
// repo. Called on BOQ approval to record per-line sell vs cost; nil-
// safe (the approval hook short-circuits when missing).
func (s *Service) WithInternalTransactions(r port.InternalTransactionRepository) *Service {
	s.internalTxs = r
	return s
}

// WithBOQ attaches the Phase 3 driven ports (BOQ + lines + approval +
// SLA templates). Separate builder so a deployment that doesn't want
// the BOQ surface yet can leave it off; the methods then surface
// errBOQNotConfigured cleanly via the HTTP layer.
func (s *Service) WithBOQ(
	sla port.SLATemplateRepository,
	apt port.ApprovalTemplateRepository,
	api port.ApprovalInstanceRepository,
	boqs port.BOQRepository,
	lines port.BOQLineRepository,
) *Service {
	s.slaTemplates = sla
	s.approvalTemplates = apt
	s.approvalInstances = api
	s.boqs = boqs
	s.boqLines = lines
	return s
}

var _ port.UseCase = (*Service)(nil)

// errBOQNotConfigured is the canonical response when the Phase 3
// surface is called without WithBOQ. Surfaces as a clear 500 with
// a typed code so the FE can flag wiring issues.
func errBOQNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "boq.not_configured",
		"BOQ surface is not configured for this service", nil)
}

// =====================================================================
// Pricebooks
// =====================================================================

func (s *Service) ListPricebooks(ctx context.Context, f port.PricebookListFilter) ([]domain.Pricebook, int, error) {
	return s.pricebooks.List(ctx, f)
}

func (s *Service) GetPricebook(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error) {
	return s.pricebooks.FindByID(ctx, id)
}

func (s *Service) CreatePricebook(ctx context.Context, in port.CreatePricebookInput) (*domain.Pricebook, error) {
	from, err := parseDateRequired(in.EffectiveFrom, "effective_from")
	if err != nil {
		return nil, err
	}
	var to *time.Time
	if in.EffectiveTo != "" {
		t, err := parseDateRequired(in.EffectiveTo, "effective_to")
		if err != nil {
			return nil, err
		}
		to = &t
	}
	p, err := domain.NewPricebook(in.Code, in.Name, from, to)
	if err != nil {
		return nil, err
	}
	if c := strings.ToUpper(strings.TrimSpace(in.Currency)); c != "" {
		p.Currency = c
	}
	p.HoldingCompanyID = in.HoldingCompanyID
	p.Notes = in.Notes
	p.CreatedBy = in.CreatedBy

	// Overlap check (CPQ TC-PB-002). We only reject overlaps with
	// non-superseded rows — superseded windows are historical.
	overlaps, err := s.pricebooks.FindOverlapping(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(overlaps) > 0 {
		return nil, derrors.Conflict(
			"pricebook.pricebook_overlap",
			"an active pricebook with the same code already covers this effective window",
		)
	}

	if err := s.pricebooks.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) UpdatePricebook(ctx context.Context, in port.UpdatePricebookInput) (*domain.Pricebook, error) {
	p, err := s.pricebooks.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// Only draft pricebooks are mutable. Published / Superseded are
	// immutable per CPQ TC-PB-007/008.
	if p.Status != domain.PricebookStatusDraft {
		return nil, derrors.Conflict(
			"pricebook.cannot_edit_published",
			"published or superseded pricebooks are immutable — clone to a new draft to revise",
		)
	}
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.EffectiveFrom != nil {
		t, err := parseDateRequired(*in.EffectiveFrom, "effective_from")
		if err != nil {
			return nil, err
		}
		p.EffectiveFrom = t
	}
	if in.EffectiveTo != nil {
		if *in.EffectiveTo == "" {
			p.EffectiveTo = nil
		} else {
			t, err := parseDateRequired(*in.EffectiveTo, "effective_to")
			if err != nil {
				return nil, err
			}
			p.EffectiveTo = &t
		}
	}
	if in.Notes != nil {
		p.Notes = *in.Notes
	}
	if err := s.pricebooks.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) PublishPricebook(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error) {
	p, err := s.pricebooks.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// Re-check overlap with currently-published rows before flipping —
	// the draft might have been created when no one was published, but
	// someone else might have published a competitor in the meantime.
	overlaps, err := s.pricebooks.FindOverlapping(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, other := range overlaps {
		if other.Status == domain.PricebookStatusPublished {
			return nil, derrors.Conflict(
				"pricebook.pricebook_overlap",
				"another published pricebook with the same code overlaps the effective window",
			)
		}
	}
	if err := p.Publish(); err != nil {
		return nil, err
	}
	if err := s.pricebooks.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// =====================================================================
// Pricebook lines
// =====================================================================

func (s *Service) ListPricebookLines(ctx context.Context, pricebookID uuid.UUID) ([]domain.PricebookLine, error) {
	return s.lines.ListByPricebook(ctx, pricebookID)
}

func (s *Service) CreatePricebookLine(ctx context.Context, in port.CreatePricebookLineInput) (*domain.PricebookLine, error) {
	// Editing lines on a published pricebook is blocked — the line
	// belongs to an immutable version. Caller must create a new
	// pricebook (draft) first.
	pb, err := s.pricebooks.FindByID(ctx, in.PricebookID)
	if err != nil {
		return nil, err
	}
	if pb.Status != domain.PricebookStatusDraft {
		return nil, derrors.Conflict(
			"pricebook_line.parent_not_draft",
			"cannot add lines to a non-draft pricebook",
		)
	}
	l, err := domain.NewPricebookLine(
		in.PricebookID, in.SKU, in.Name,
		in.BasePrice, in.DefaultMarginPct, in.MinMarginPct, in.MaxDiscountPct,
	)
	if err != nil {
		return nil, err
	}
	l.Category = in.Category
	l.Description = in.Description
	if u := strings.TrimSpace(in.Unit); u != "" {
		l.Unit = u
	}
	l.AllowedProviderCompanyIDs = in.AllowedProviderCompanyIDs
	if l.AllowedProviderCompanyIDs == nil {
		l.AllowedProviderCompanyIDs = []uuid.UUID{}
	}
	l.OwnerRole = in.OwnerRole
	l.SortOrder = in.SortOrder
	if err := s.lines.Create(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) UpdatePricebookLine(ctx context.Context, in port.UpdatePricebookLineInput) (*domain.PricebookLine, error) {
	l, err := s.lines.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// Block edits when the parent pricebook is no longer draft.
	pb, err := s.pricebooks.FindByID(ctx, l.PricebookID)
	if err != nil {
		return nil, err
	}
	if pb.Status != domain.PricebookStatusDraft {
		return nil, derrors.Conflict(
			"pricebook_line.parent_not_draft",
			"cannot edit lines on a non-draft pricebook",
		)
	}
	if in.Name != nil {
		l.Name = *in.Name
	}
	if in.Category != nil {
		l.Category = *in.Category
	}
	if in.Description != nil {
		l.Description = *in.Description
	}
	if in.Unit != nil && strings.TrimSpace(*in.Unit) != "" {
		l.Unit = *in.Unit
	}
	if in.BasePrice != nil {
		l.BasePrice = *in.BasePrice
	}
	if in.DefaultMarginPct != nil {
		l.DefaultMarginPct = *in.DefaultMarginPct
	}
	if in.MinMarginPct != nil {
		l.MinMarginPct = *in.MinMarginPct
	}
	if in.MaxDiscountPct != nil {
		l.MaxDiscountPct = *in.MaxDiscountPct
	}
	if in.AllowedProviderCompanyIDs != nil {
		l.AllowedProviderCompanyIDs = *in.AllowedProviderCompanyIDs
	}
	if in.OwnerRole != nil {
		l.OwnerRole = *in.OwnerRole
	}
	if in.SortOrder != nil {
		l.SortOrder = *in.SortOrder
	}
	if in.Active != nil {
		l.Active = *in.Active
	}
	// Re-validate invariants because we've potentially mutated several
	// fields in concert (e.g., min and default margin both changed in
	// the same PATCH).
	if l.MinMarginPct > l.DefaultMarginPct {
		return nil, derrors.Validation(
			"pricebook_line.min_margin_exceeds_default",
			"min_margin_pct must not exceed default_margin_pct",
		)
	}
	if l.BasePrice < 0 {
		return nil, derrors.Validation(
			"pricebook_line.base_price_negative",
			"base_price must be >= 0",
		)
	}
	if err := s.lines.Update(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) DeletePricebookLine(ctx context.Context, id uuid.UUID) error {
	l, err := s.lines.FindByID(ctx, id)
	if err != nil {
		return err
	}
	pb, err := s.pricebooks.FindByID(ctx, l.PricebookID)
	if err != nil {
		return err
	}
	if pb.Status != domain.PricebookStatusDraft {
		return derrors.Conflict(
			"pricebook_line.parent_not_draft",
			"cannot delete lines on a non-draft pricebook",
		)
	}
	return s.lines.Delete(ctx, id)
}

// =====================================================================
// Opportunities
// =====================================================================

func (s *Service) ListOpportunities(ctx context.Context, f port.OpportunityListFilter) ([]domain.Opportunity, int, error) {
	return s.opps.List(ctx, f)
}

func (s *Service) GetOpportunity(ctx context.Context, id uuid.UUID) (*domain.Opportunity, error) {
	return s.opps.FindByID(ctx, id)
}

func (s *Service) CreateOpportunity(ctx context.Context, in port.CreateOpportunityInput) (*domain.Opportunity, error) {
	o, err := domain.NewOpportunity(in.AccountName)
	if err != nil {
		return nil, err
	}
	o.OpportunityNumber = domain.GenerateOpportunityNumber(time.Now())
	o.AccountIndustry = in.AccountIndustry
	o.AccountSize = in.AccountSize
	o.PICName = in.PICName
	o.PICTitle = in.PICTitle
	o.PICPhone = in.PICPhone
	o.PICEmail = in.PICEmail
	o.OwnerUserID = in.OwnerUserID
	o.BranchID = in.BranchID
	o.EstimatedValue = in.EstimatedValue
	if c := strings.ToUpper(strings.TrimSpace(in.Currency)); c != "" {
		o.Currency = c
	}
	if in.ExpectedCloseAt != nil && *in.ExpectedCloseAt != "" {
		t, err := parseDateRequired(*in.ExpectedCloseAt, "expected_close_at")
		if err != nil {
			return nil, err
		}
		o.ExpectedCloseAt = &t
	}
	if src := strings.TrimSpace(in.Source); src != "" {
		o.Source = domain.OpportunitySource(src)
	}
	o.ReferrerCustomerID = in.ReferrerCustomerID
	o.CustomerID = in.CustomerID
	o.Notes = in.Notes
	if err := s.opps.Create(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) UpdateOpportunity(ctx context.Context, in port.UpdateOpportunityInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// Terminal opportunities (Won / Lost) are read-only at this layer.
	// Reopening requires a domain-level transition we don't expose at MVP.
	if o.Stage == domain.OpportunityStageWon || o.Stage == domain.OpportunityStageLost {
		return nil, derrors.Conflict(
			"opportunity.terminal",
			"opportunities in won/lost cannot be edited",
		)
	}
	if in.AccountName != nil {
		o.AccountName = *in.AccountName
	}
	if in.AccountIndustry != nil {
		o.AccountIndustry = *in.AccountIndustry
	}
	if in.AccountSize != nil {
		o.AccountSize = *in.AccountSize
	}
	if in.PICName != nil {
		o.PICName = *in.PICName
	}
	if in.PICTitle != nil {
		o.PICTitle = *in.PICTitle
	}
	if in.PICPhone != nil {
		o.PICPhone = *in.PICPhone
	}
	if in.PICEmail != nil {
		o.PICEmail = *in.PICEmail
	}
	if in.OwnerUserID != nil {
		o.OwnerUserID = in.OwnerUserID
	}
	if in.BranchID != nil {
		o.BranchID = in.BranchID
	}
	if in.EstimatedValue != nil {
		o.EstimatedValue = *in.EstimatedValue
	}
	if in.ExpectedCloseAt != nil {
		if *in.ExpectedCloseAt == "" {
			o.ExpectedCloseAt = nil
		} else {
			t, err := parseDateRequired(*in.ExpectedCloseAt, "expected_close_at")
			if err != nil {
				return nil, err
			}
			o.ExpectedCloseAt = &t
		}
	}
	if in.Notes != nil {
		o.Notes = *in.Notes
	}
	// Any material edit counts as activity — keeps the auto-Lost
	// scheduler honest.
	o.TouchActivity()
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) AdvanceStage(ctx context.Context, in port.AdvanceStageInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	switch in.TargetStage {
	case "warm":
		err = o.AdvanceToWarm()
	case "hot":
		err = o.AdvanceToHot()
	case "won":
		err = o.MarkWon(in.POReference)
	default:
		return nil, derrors.Validation(
			"opportunity.invalid_target_stage",
			"target_stage must be one of: warm, hot, won",
		)
	}
	if err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) MarkLost(ctx context.Context, in port.MarkLostInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := o.MarkLost(domain.LostReasonCode(in.ReasonCode), in.Reason, false); err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) CompletePreBOQ(ctx context.Context, in port.CompletePreBOQInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := o.CompletePreBOQ(in.Snapshot); err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) PinPricebook(ctx context.Context, in port.PinPricebookInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	pb, err := s.pricebooks.FindByID(ctx, in.PricebookID)
	if err != nil {
		return nil, err
	}
	// CPQ TC-PB-010: only published versions are pinnable. Draft and
	// superseded should be invisible to the Opportunity picker.
	if pb.Status != domain.PricebookStatusPublished {
		return nil, derrors.Validation(
			"pricebook.not_published",
			"only published pricebooks can be pinned to an opportunity",
		)
	}
	if err := o.PinPricebook(in.PricebookID); err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	return o, nil
}

// RunAutoLostSweep is the cron entry point. It scans for non-terminal
// opportunities whose stage SLA window has expired, flips them to
// Lost with `auto_lost=true` and reason_code='stage_timeout', and
// returns the list of IDs touched. Idempotent: a fresh sweep won't
// touch already-Lost rows.
//
// CPQ TC-OP-007 + TC-SM-OPP-007 — same logic, same boundary semantics.
func (s *Service) RunAutoLostSweep(ctx context.Context) ([]uuid.UUID, error) {
	candidates, err := s.opps.FindExpiredAutoLostCandidates(ctx)
	if err != nil {
		return nil, err
	}
	touched := []uuid.UUID{}
	for i := range candidates {
		o := &candidates[i]
		// The SQL already filtered by stage + age, but defend against
		// drift by re-checking via the domain method.
		if !o.IsAutoLostExpired(time.Now()) {
			continue
		}
		if err := o.MarkLost(domain.LostReasonStageTimeout, "Auto-Lost — stage SLA window expired", true); err != nil {
			// Skip rows the domain rejects (e.g. already terminal due to
			// a race) — they'll naturally fall out of the candidate set
			// on the next sweep.
			if s.log != nil {
				s.log.Warn("auto-lost skip",
					"opportunity_id", o.ID.String(),
					"reason", err.Error(),
				)
			}
			continue
		}
		if err := s.opps.Update(ctx, o, nil); err != nil {
			if s.log != nil {
				s.log.Warn("auto-lost update failed",
					"opportunity_id", o.ID.String(),
					"err", err.Error(),
				)
			}
			continue
		}
		touched = append(touched, o.ID)
	}
	return touched, nil
}

// =====================================================================
// Helpers
// =====================================================================

func parseDateRequired(s, field string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, derrors.Validation(
			"pricebook."+field+"_required",
			field+" is required",
		)
	}
	// Accept either YYYY-MM-DD or RFC 3339 (when clients send full
	// timestamps). The DB column is DATE so either flavor is fine
	// after parse.
	t, err := time.Parse("2006-01-02", s)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, derrors.Validation(
			"pricebook."+field+"_invalid",
			field+" must be a date (YYYY-MM-DD)",
		)
	}
	return t, nil
}
