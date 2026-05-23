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
	"github.com/jackc/pgx/v5/pgxpool"

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

	// Wave 95 — Customer PO + Intercompany PO. All three nil-safe; the
	// customer_po / intercompany_po use-case methods return
	// errCustomerPONotConfigured / errIntercompanyPONotConfigured when
	// the corresponding repos aren't wired. cmd/enterprise-svc/main.go
	// wires both together.
	customerPOs      port.CustomerPORepository
	intercompanyPOs  port.IntercompanyPORepository
	intercompanyPair port.IntercompanyPairRepository

	// Wave 96 — EWO schedule history repo. Optional; ScheduleEWO /
	// RescheduleEWO methods return errEWOSchedulingNotConfigured when
	// it's nil.
	ewoScheduleHistory port.EWOScheduleHistoryRepository

	// Wave 101 — tax_profile resolver (bridges to internal/tax). Nil-safe:
	// when missing, the BOQ approval hook + invoice generation
	// silently skip the snapshot chain (Non-PKP fallback) so legacy
	// deployments keep approving.
	taxResolver port.TaxResolver

	// Wave 106 — Pre-BOQ required-field admin config. Nil-safe; when
	// missing, CompletePreBOQ falls back to the legacy "any non-empty
	// JSON" behaviour. Wired via WithPreBOQRequiredFields.
	preBOQRequiredFields port.PreBOQRequiredFieldRepository

	// Wave 106 — pgx pool needed for postgres advisory locks on the
	// approval-decision critical sections (TC-EDGE-002 parallel approval).
	// Nil-safe: when missing, the critical section runs unwrapped (the
	// existing behaviour pre-Wave 106). Wired via WithLockPool.
	lockPool *pgxpool.Pool

	// Wave 107 — vendor metrics updater (bridges to internal/vendormgmt).
	// Nil-safe: when missing, the IC-PO-accept hook skips the increment;
	// the daily metrics deriver still recomputes from the completion log.
	vendorMetrics port.VendorMetricsUpdater

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

// WithCustomerPOs wires the Wave 95 customer-PO repo. Nil-safe; the
// surface returns errCustomerPONotConfigured when missing.
func (s *Service) WithCustomerPOs(repo port.CustomerPORepository) *Service {
	s.customerPOs = repo
	return s
}

// WithIntercompanyPOs wires the Wave 95 IC-PO + pair repos together —
// they're mutually dependent (AcceptCustomerPO consults the pair table
// to decide whether to auto-issue the IC-PO it just drafted), so a
// single setter keeps the wiring honest.
func (s *Service) WithIntercompanyPOs(
	icRepo port.IntercompanyPORepository,
	pairRepo port.IntercompanyPairRepository,
) *Service {
	s.intercompanyPOs = icRepo
	s.intercompanyPair = pairRepo
	return s
}

// WithEWOScheduling attaches the Wave 96 reschedule-history repo. The
// scheduling/reschedule/start usecases short-circuit with
// errEWOSchedulingNotConfigured when this is nil so a Phase 1-less
// deployment keeps building.
func (s *Service) WithEWOScheduling(history port.EWOScheduleHistoryRepository) *Service {
	s.ewoScheduleHistory = history
	return s
}

// WithTaxResolver attaches the Wave 101 cross-context tax resolver.
// Nil-safe — the BOQ-approval hook + invoice-time faktur decision
// short-circuit when this is missing, which keeps Phase-2/3/4
// deployments compiling without the tax bounded context wired.
func (s *Service) WithTaxResolver(r port.TaxResolver) *Service {
	s.taxResolver = r
	return s
}

// WithVendorMetrics wires the Wave 107 cross-context vendor metrics
// updater. Nil-safe — when missing, the IC-PO-accept hook skips the
// provider counter increment; the daily metrics deriver still rebuilds
// from the completion log on its own cadence.
func (s *Service) WithVendorMetrics(u port.VendorMetricsUpdater) *Service {
	s.vendorMetrics = u
	return s
}

// WithPreBOQRequiredFields wires the Wave 106 Pre-BOQ structured
// validator config (TC-OP-009). When nil, CompletePreBOQ falls back to
// the legacy "any non-empty JSON" semantics so existing deployments
// without migration 0071 applied keep working.
func (s *Service) WithPreBOQRequiredFields(r port.PreBOQRequiredFieldRepository) *Service {
	s.preBOQRequiredFields = r
	return s
}

// WithLockPool attaches the pgx pool used for postgres advisory locks
// on the approval-decision critical sections (Wave 106 retrofit of the
// Wave 104 advisory_lock helper). Nil-safe: when missing, ApproveStep
// / RejectStep run unwrapped (pre-Wave 106 behaviour), so existing
// deployments without a pool reference keep working.
func (s *Service) WithLockPool(pool *pgxpool.Pool) *Service {
	s.lockPool = pool
	return s
}

// Wave 95 — BOQ-approval recognition for `internal_transactions` is
// staying live (see `recordInternalTransactionsOnApproval`) to avoid
// breaking existing dashboards. The new IC-PO-accept recognition path
// (`recordInternalTransactionsOnICPOAccept`) is the canonical trigger
// per the Wave 91 audit's Wave 94 acceptance criteria: TC-VF-001 / 002
// require recognition at IC-PO accept, not BOQ approval. A reconciliation
// cron in Wave 95b will detect + report double-counting; once that
// signal is stable, the BOQ-approval call site can be removed in a
// dedicated breaking-change wave. See `internal_transaction.go`.

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

// ListPricebookLinesSorted is the Wave 106 entry point for the
// provider-priority sorted picker (TC-PB-010). `sort="priority"`
// orders by (priority_score DESC, sku ASC); other values fall back
// to the default (sort_order, name) so existing FE call sites
// without a sort param keep working.
func (s *Service) ListPricebookLinesSorted(ctx context.Context, pricebookID uuid.UUID, sort string) ([]domain.PricebookLine, error) {
	return s.lines.ListByPricebookSorted(ctx, pricebookID, sort)
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
	// Wave 106 — provider-priority badge. 0 = unranked.
	l.PriorityScore = in.PriorityScore
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
	if in.PriorityScore != nil {
		// Wave 106 — provider-priority badge mutation. We accept any
		// non-negative integer; the DB column has no upper bound so the
		// FE can rank with arbitrary spacing.
		if *in.PriorityScore < 0 {
			return nil, derrors.Validation(
				"pricebook_line.priority_score_negative",
				"priority_score must be >= 0",
			)
		}
		l.PriorityScore = *in.PriorityScore
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
		// Wave 106 — TC-OP-010: require an RFQ before promoting to Warm.
		// Nil-safe: when the rfqs repo isn't wired the check is skipped
		// (existing test deployments without pre-launch still work).
		if s.rfqs != nil {
			rfqs, _, lerr := s.rfqs.List(ctx, "", &o.ID, nil, 1, 0)
			if lerr != nil {
				return nil, lerr
			}
			if len(rfqs) == 0 {
				return nil, derrors.Validation(
					"opportunity.warm_requires_rfq",
					"cannot advance to warm without an RFQ raised on this opportunity",
				)
			}
		}
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
	// Wave 106 — TC-OP-009 structured validator. When the admin config
	// table is wired (migration 0071 applied + WithPreBOQRequiredFields
	// builder called) we enforce that every required=TRUE field is
	// present in the snapshot. Nil-safe — falls back to the legacy
	// "any non-empty JSON" check via the domain method below.
	if s.preBOQRequiredFields != nil {
		fields, lerr := s.preBOQRequiredFields.ListAll(ctx)
		if lerr == nil && len(fields) > 0 {
			if verr := domain.ValidatePreBOQSnapshot(in.Snapshot, fields); verr != nil {
				return nil, verr
			}
		}
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

// MarkOpportunityAutoLost is the Wave 106 single-row auto-Lost path.
// Used by the OpportunityAutoLostWatcher cron when iterating expired
// candidates ID-by-ID. Idempotent on already-Lost rows (returns the
// existing entity without a state-machine error).
//
// CPQ TC-OP-007 + TC-SM-OPP-007 — same boundary semantics as
// RunAutoLostSweep but per-id so the cron can audit each one
// individually.
func (s *Service) MarkOpportunityAutoLost(ctx context.Context, id uuid.UUID) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if o.Stage == domain.OpportunityStageLost || o.Stage == domain.OpportunityStageWon {
		return o, nil // idempotent
	}
	if !o.IsAutoLostExpired(time.Now()) {
		// Drift defense — refuse to flip if the SLA window hasn't
		// actually elapsed yet (cron drift / clock skew).
		return nil, derrors.Conflict(
			"opportunity.auto_lost_window_not_expired",
			"opportunity's auto-Lost SLA window has not elapsed",
		)
	}
	if err := o.MarkLost(domain.LostReasonStageTimeout, "Auto-Lost — stage SLA window expired", true); err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, nil); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "opportunity",
		RecordID:     o.ID.String(),
		FieldChanged: "stage",
		Before:       "non_terminal",
		After:        string(domain.OpportunityStageLost),
		Reason:       "auto_lost_watcher",
	})
	return o, nil
}

// ReassignOpportunity is the Wave 106 TC-OP-011 endpoint. Rotates the
// owner with a categorical audit trail (`prev_owner_id` → `new_owner_id`).
// Domain rejects same-owner re-assigns and terminal stages; the
// usecase wraps with audit + optimistic-concurrency.
func (s *Service) ReassignOpportunity(ctx context.Context, in port.ReassignOpportunityInput) (*domain.Opportunity, error) {
	o, err := s.opps.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	prev, err := o.Reassign(in.NewOwnerID)
	if err != nil {
		return nil, err
	}
	if err := s.opps.Update(ctx, o, in.IfRevision); err != nil {
		return nil, err
	}
	prevStr := ""
	if prev != nil {
		prevStr = prev.String()
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       in.ByUserID,
		Module:       "enterprise",
		RecordType:   "opportunity",
		RecordID:     o.ID.String(),
		FieldChanged: "owner_user_id",
		Before:       prevStr,
		After:        in.NewOwnerID.String(),
		Reason:       "opportunity.owner_reassigned",
	})
	// Notify both old (if any) + new owner so the inbox carries the
	// rotation. Best-effort; Notify is nil-safe.
	if prev != nil {
		s.Notify(ctx, domain.NewNotification(
			*prev,
			"opportunity.reassigned_away",
			"opportunity", o.ID,
			"Opportunity reassigned away",
			"You are no longer the owner of opportunity "+o.OpportunityNumber+".",
			domain.NotificationSeverityInfo,
		))
	}
	s.Notify(ctx, domain.NewNotification(
		in.NewOwnerID,
		"opportunity.reassigned_to_me",
		"opportunity", o.ID,
		"Opportunity reassigned to you",
		"You are the new owner of opportunity "+o.OpportunityNumber+".",
		domain.NotificationSeverityInfo,
	))
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
