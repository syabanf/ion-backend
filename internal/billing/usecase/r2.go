// Package usecase — M6 r2 additions.
//
// The scheduler is a single "tick" function that performs four passes
// in order:
//
//	1. Recurring invoices  — for each active order, generate the
//	   current month's invoice if no billing_cycle row exists yet.
//	2. Late fees           — for each issued invoice past the grace
//	   window, add a single penalty line (idempotent via reference_id
//	   on the line item).
//	3. Suspensions         — customers with any invoice overdue by
//	   `suspend_after_days` flip to 'suspended' + RADIUS suspend.
//	4. Restorations        — customers in 'suspended' whose invoices
//	   are all paid flip back to 'active' + RADIUS restore.
//	5. Terminations        — customers suspended longer than
//	   `terminate_after_suspended_days` flip to 'terminated' + a
//	   termination WO is queued (round-3: actual WO creation; r2
//	   stamps the customer and lets ops follow up manually).
//
// The tick is idempotent on the same day: re-running creates no
// duplicate cycles or commissions. Errors per customer don't abort the
// whole run — they're collected in TickReport.Errors.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	platformport "github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithR2 attaches the M6 r2 repos + gateways. Nil-safe: r1 callers
// keep working; cmd/billing-svc/main.go always sets these.
func (s *Service) WithR2(
	policies port.PolicyRepository,
	cycles port.CycleRepository,
	commissions port.CommissionRepository,
	crm port.CRMGateway,
	network port.NetworkGateway,
	log *slog.Logger,
) *Service {
	s.policies = policies
	s.cycles = cycles
	s.commissions = commissions
	s.crm = crm
	s.network = network
	s.log = log
	return s
}

// WithSchemaResolver attaches the platform Schema System v1 resolver so
// the billing tick / commission calc read their config from per-customer
// schemas instead of the global billing.policies row. Nil-safe — if
// resolver is nil (e.g. tests, or platform-svc unreachable at boot),
// all schema lookups fall through to the legacy billing.policies path.
//
// Must be called AFTER WithR2 because the fallback path still reads
// the legacy policy via s.policies.Get.
func (s *Service) WithSchemaResolver(resolver platformport.SchemaResolver) *Service {
	s.schemaPolicy = newSchemaPolicyResolver(resolver, s.log)
	return s
}

// =====================================================================
// Policy CRUD
// =====================================================================

func (s *Service) GetPolicy(ctx context.Context) (*domain.Policy, error) {
	if s.policies == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r2_not_wired", "policy repo not configured")
	}
	return s.policies.Get(ctx)
}

func (s *Service) UpdatePolicy(ctx context.Context, in port.UpdatePolicyInput) (*domain.Policy, error) {
	if s.policies == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r2_not_wired", "policy repo not configured")
	}
	return s.policies.Update(ctx, in)
}

// =====================================================================
// Cycle / Commission read-through
// =====================================================================

func (s *Service) ListBillingCycles(ctx context.Context, f port.CycleFilter) ([]domain.BillingCycle, int, error) {
	if s.cycles == nil {
		return nil, 0, derrors.New(derrors.KindInternal, "billing.r2_not_wired", "cycle repo not configured")
	}
	return s.cycles.List(ctx, f)
}

func (s *Service) ListCommissions(ctx context.Context, f port.CommissionFilter) ([]domain.CommissionRecord, error) {
	if s.commissions == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r2_not_wired", "commission repo not configured")
	}
	return s.commissions.List(ctx, f)
}

// =====================================================================
// The tick
// =====================================================================

// RunBillingTick performs one scheduler pass at `now`. The caller can
// be the background ticker goroutine OR a manual /cycles/run endpoint.
// The latter lets ops + tests force the pass without waiting on the cron.
func (s *Service) RunBillingTick(ctx context.Context, now time.Time) (*port.TickReport, error) {
	if s.policies == nil || s.cycles == nil || s.crm == nil {
		return nil, derrors.New(derrors.KindInternal, "billing.r2_not_wired",
			"scheduler not configured — call WithR2 in cmd")
	}
	rep := &port.TickReport{StartedAt: now}

	policy, err := s.policies.Get(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.tickRecurring(ctx, now, rep); err != nil {
		rep.Errors = append(rep.Errors, "recurring: "+err.Error())
	}
	if err := s.tickLateFees(ctx, now, policy, rep); err != nil {
		rep.Errors = append(rep.Errors, "late_fees: "+err.Error())
	}
	if err := s.tickSuspendRestore(ctx, now, policy, rep); err != nil {
		rep.Errors = append(rep.Errors, "suspend_restore: "+err.Error())
	}
	if err := s.tickTerminations(ctx, now, policy, rep); err != nil {
		rep.Errors = append(rep.Errors, "terminations: "+err.Error())
	}

	rep.CompletedAt = time.Now().UTC()
	if s.log != nil {
		s.log.Info("billing tick complete",
			"recurring", rep.RecurringGenerated,
			"late_fees", rep.LateFeesApplied,
			"suspended", rep.CustomersSuspended,
			"restored", rep.CustomersRestored,
			"terminations", rep.TerminationsTriggered,
			"errors", len(rep.Errors),
		)
	}
	return rep, nil
}

// --- Pass 1: recurring invoices --------------------------------------
//
// Cadence: per-customer anniversary. The anniversary day is the
// day-of-month of activated_at (the moment NOC first verified the BAST).
// The current period is [anchor(now), anchor(now)+1 month). When
// activated_at is unset (legacy data) we fall back to the calendar month
// boundary so we don't lose pre-existing customers.

// anniversaryPeriod computes (period_start, period_end) anchored on
// activatedAt's day-of-month. The result always contains `now`.
// Month-end overflow (e.g. activated on the 31st) is clamped to the
// last day of the target month.
func anniversaryPeriod(now time.Time, activatedAt *time.Time) (time.Time, time.Time) {
	if activatedAt == nil {
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0).Add(-24 * time.Hour)
		return start, end
	}
	day := activatedAt.Day()
	candidate := anchor(now.Year(), int(now.Month()), day)
	if candidate.After(now) {
		// Anniversary hasn't hit this calendar month yet; the active
		// period is the previous month's anchor → this one's anchor.
		candidate = anchor(now.Year(), int(now.Month())-1, day)
	}
	next := anchor(candidate.Year(), int(candidate.Month())+1, day)
	end := next.Add(-24 * time.Hour)
	return candidate, end
}

// anchor returns time.Date(year, month, day) clamped so a day=31 in a
// 30-day month becomes the 30th, etc.
func anchor(year, month, day int) time.Time {
	// Normalise negative / >12 months.
	t := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	// Days-in-month: the day before the next month's first.
	maxDay := t.AddDate(0, 1, -1).Day()
	if day > maxDay {
		day = maxDay
	}
	if day < 1 {
		day = 1
	}
	return time.Date(t.Year(), t.Month(), day, 0, 0, 0, 0, time.UTC)
}

func (s *Service) tickRecurring(ctx context.Context, now time.Time, rep *port.TickReport) error {
	orders, err := s.crm.ActiveOrdersForRecurring(ctx)
	if err != nil {
		return err
	}

	for _, o := range orders {
		// Customers must have activated; skip pending-install rows.
		if o.ActivatedAt != nil && o.ActivatedAt.After(now) {
			rep.RecurringSkipped++
			continue
		}

		periodStart, periodEnd := anniversaryPeriod(now, o.ActivatedAt)

		// Idempotency: skip if we already generated a cycle for this
		// (customer, period_start).
		exists, err := s.cycles.ExistsForPeriod(ctx, o.CustomerID, periodStart)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("cycle exists check %s: %v", o.CustomerID, err))
			continue
		}
		if exists {
			continue
		}

		// Build the recurring invoice via the existing CreateInvoice path.
		due := periodStart.AddDate(0, 0, 7) // 7-day net terms
		oid := o.OrderID
		inv, err := s.CreateInvoice(ctx, port.CreateInvoiceInput{
			CustomerID:  o.CustomerID,
			OrderID:     &oid,
			InvoiceType: domain.InvoiceTypeRecurring,
			PPNRate:     11.0,
			DueDate:     due,
			IssueImmediately: true,
			Lines: []port.LineItemInput{
				{
					Description: fmt.Sprintf("Monthly service — %s", periodStart.Format("Jan 2006")),
					ItemType:    "mrc",
					Quantity:    1,
					UnitPrice:   o.MonthlyPrice,
				},
			},
		})
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("create recurring inv %s: %v", o.OrderID, err))
			_ = s.cycles.Create(ctx, &domain.BillingCycle{
				ID: uuid.New(), CustomerID: o.CustomerID, OrderID: o.OrderID,
				PeriodStart: periodStart, PeriodEnd: periodEnd,
				Status: domain.CycleStatusFailed, Notes: err.Error(),
				CreatedAt: now,
			})
			continue
		}
		invoiceID := inv.Invoice.ID
		_ = s.cycles.Create(ctx, &domain.BillingCycle{
			ID: uuid.New(), CustomerID: o.CustomerID, OrderID: o.OrderID,
			PeriodStart: periodStart, PeriodEnd: periodEnd,
			InvoiceID: &invoiceID, Status: domain.CycleStatusGenerated,
			CreatedAt: now,
		})
		rep.RecurringGenerated++
	}
	return nil
}

// --- Pass 2: late fees ----------------------------------------------

// We add a single fixed late fee per invoice once it's overdue by
// grace_days. Idempotency: the invoice's existing lines are checked
// for an existing 'penalty' line with our reference text before we
// add another. The penalty pushes the invoice's total via the
// existing line-item ingestion path (note: round-2 doesn't recompute
// totals on existing invoices — late fees are issued as *new*
// invoices of type 'addon' so the recompute is simpler).
func (s *Service) tickLateFees(ctx context.Context, now time.Time, policy *domain.Policy, rep *port.TickReport) error {
	// Find every issued/overdue invoice. We use the broadest grace
	// (the global policy's grace) as a first-pass filter, then
	// re-check each invoice against the customer-specific resolved
	// policy below — a customer override might tighten or relax the
	// grace.
	views, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
		Status: string(domain.InvoiceStatusIssued),
		Limit:  500,
	})
	if err != nil {
		return err
	}
	for _, v := range views {
		// Resolve the billing schema per-customer so an operator
		// override is respected. Falls back to legacy policy when the
		// resolver isn't wired or the kind/key is missing.
		custPolicy := s.resolvedBillingPolicy(ctx, v.Invoice.CustomerID, policy)
		cutoff := now.AddDate(0, 0, -custPolicy.LateFeeGraceDays)
		if !v.Invoice.DueDate.Before(cutoff) {
			continue
		}
		// Already has a late fee invoice referencing this one?
		hasLateFee, err := s.hasLateFeeFor(ctx, v.Invoice.OrderID)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("late-fee check %s: %v", v.Invoice.ID, err))
			continue
		}
		if hasLateFee {
			continue
		}
		// Mint a separate 'addon' invoice for the penalty.
		due := now.AddDate(0, 0, 7)
		var orderID *uuid.UUID
		if v.Invoice.OrderID != nil {
			oid := *v.Invoice.OrderID
			orderID = &oid
		}
		if _, err := s.CreateInvoice(ctx, port.CreateInvoiceInput{
			CustomerID:  v.Invoice.CustomerID,
			OrderID:     orderID,
			InvoiceType: domain.InvoiceTypeAddon,
			PPNRate:     11.0,
			DueDate:     due,
			Notes:       fmt.Sprintf("Late fee for %s", v.Invoice.InvoiceNumber),
			IssueImmediately: true,
			Lines: []port.LineItemInput{
				{
					Description: fmt.Sprintf("Late fee — %s", v.Invoice.InvoiceNumber),
					ItemType:    "penalty",
					Quantity:    1,
					UnitPrice:   custPolicy.LateFeeAmount,
				},
			},
		}); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("late-fee invoice %s: %v", v.Invoice.ID, err))
			continue
		}
		rep.LateFeesApplied++
	}
	return nil
}

// resolvedBillingPolicy is the per-customer policy view. When the
// schema resolver is wired and a billing schema is published (and
// optionally overridden for the customer) the relevant fields swap
// out; missing pieces fall back to the legacy policy supplied by the
// caller. Safe to call with customerID == uuid.Nil for tick passes
// without a customer (resolves DEFAULT schema).
func (s *Service) resolvedBillingPolicy(
	ctx context.Context, customerID uuid.UUID, legacy *domain.Policy,
) *domain.Policy {
	if s.schemaPolicy == nil {
		return legacy
	}
	return s.schemaPolicy.resolvedPolicyFor(ctx, customerID, legacy)
}

// resolvedSuspensionPolicy reads the suspension-kind schema for the
// customer and overlays the suspend_after_days /
// terminate_after_suspended_days keys on top of the legacy policy.
// Falls back transparently when the resolver isn't wired or the
// keys are missing.
func (s *Service) resolvedSuspensionPolicy(
	ctx context.Context, customerID uuid.UUID, legacy *domain.Policy,
) *domain.Policy {
	if s.schemaPolicy == nil {
		return legacy
	}
	body, _ := s.schemaPolicy.loadSuspension(ctx, customerID)
	if body == nil {
		// No suspension schema → fall through to the billing-kind
		// resolve (it carries the same keys for backward compat).
		return s.schemaPolicy.resolvedPolicyFor(ctx, customerID, legacy)
	}
	out := *legacy
	out.SuspendAfterDays = readInt(body, "suspend_after_days", legacy.SuspendAfterDays)
	out.TerminateAfterSuspendedDays = readInt(body, "terminate_after_suspended_days",
		legacy.TerminateAfterSuspendedDays)
	return &out
}

// hasLateFeeFor checks whether we've already issued a 'penalty' invoice
// for this customer/order. The check is conservative: round-2 dedupes
// per-order so a single late month doesn't generate two penalty
// invoices. Round-3 will track per-period.
func (s *Service) hasLateFeeFor(ctx context.Context, orderID *uuid.UUID) (bool, error) {
	if orderID == nil {
		return false, nil
	}
	views, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
		OrderID:     orderID,
		InvoiceType: string(domain.InvoiceTypeAddon),
		Limit:       50,
	})
	if err != nil {
		return false, err
	}
	for _, v := range views {
		// Any addon invoice with a 'penalty' line counts.
		for _, l := range v.Lines {
			if l.ItemType == "penalty" {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- Pass 3: suspend / restore --------------------------------------

func (s *Service) tickSuspendRestore(ctx context.Context, now time.Time, policy *domain.Policy, rep *port.TickReport) error {
	orders, err := s.crm.ActiveOrdersForRecurring(ctx)
	if err != nil {
		return err
	}

	// Collect candidates to suspend: any active customer with at least
	// one issued/overdue invoice past the suspend window. The cutoff is
	// resolved per-customer so an override (e.g. enterprise customer
	// with a 30-day suspend window) takes effect without redeploying.
	for _, o := range orders {
		if o.CustomerStatus != "active" {
			continue
		}
		custPolicy := s.resolvedSuspensionPolicy(ctx, o.CustomerID, policy)
		suspendCutoff := now.AddDate(0, 0, -custPolicy.SuspendAfterDays)
		overdueViews, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
			CustomerID: &o.CustomerID,
			Status:     string(domain.InvoiceStatusIssued),
			Limit:      100,
		})
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("invoice scan %s: %v", o.CustomerID, err))
			continue
		}
		shouldSuspend := false
		for _, v := range overdueViews {
			if v.Invoice.DueDate.Before(suspendCutoff) {
				shouldSuspend = true
				break
			}
		}
		if !shouldSuspend {
			continue
		}
		reason := "auto-suspend: unpaid invoice past " + fmt.Sprintf("%d", custPolicy.SuspendAfterDays) + " days"
		if err := s.crm.SetCustomerStatus(ctx, o.CustomerID, "suspended", reason); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("suspend %s: %v", o.CustomerID, err))
			continue
		}
		if s.network != nil {
			if err := s.network.SuspendCustomer(ctx, o.CustomerID, reason); err != nil {
				// Non-fatal — RADIUS resync can happen later.
				rep.Errors = append(rep.Errors, fmt.Sprintf("radius suspend %s: %v", o.CustomerID, err))
			}
		}
		rep.CustomersSuspended++
	}

	// Restorations: customers in 'suspended' with no overdue invoices.
	suspended, err := s.crm.SuspendedCustomers(ctx)
	if err != nil {
		return err
	}
	for _, sc := range suspended {
		overdueViews, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
			CustomerID: &sc.CustomerID,
			Status:     string(domain.InvoiceStatusIssued),
			Limit:      100,
		})
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("restore scan %s: %v", sc.CustomerID, err))
			continue
		}
		stillOverdue := false
		for _, v := range overdueViews {
			if v.Invoice.DueDate.Before(now) {
				stillOverdue = true
				break
			}
		}
		if stillOverdue {
			continue
		}
		if err := s.crm.SetCustomerStatus(ctx, sc.CustomerID, "active", ""); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("restore %s: %v", sc.CustomerID, err))
			continue
		}
		if s.network != nil {
			if err := s.network.RestoreCustomer(ctx, sc.CustomerID); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("radius restore %s: %v", sc.CustomerID, err))
			}
		}
		rep.CustomersRestored++
	}

	return nil
}

// --- Pass 4: auto-termination ---------------------------------------
//
// Delegates to runAutoTermination (r3). When r3 isn't wired the call is
// a no-op so r2-only deployments stay functional.
func (s *Service) tickTerminations(ctx context.Context, now time.Time, policy *domain.Policy, rep *port.TickReport) error {
	return s.runAutoTermination(ctx, now, policy, rep)
}

// =====================================================================
// Commission calculation on first payment
//
// Hook: after every successful payment that fully pays an invoice,
// look at the order — if it has no commission_records yet AND the
// invoice that was just paid is the order's OTC, compute + persist
// the 5-party split. The OTC-only condition keeps us from double-
// allocating commissions on recurring invoices (which only fund the
// company bucket).
//
// We call this from RecordPayment in service.go; the existing flow
// continues to work for r1 callers without WithR2 wired.
// =====================================================================

func (s *Service) maybeApplyCommission(ctx context.Context, invoice *port.InvoiceView, payment *domain.Payment) {
	if s.commissions == nil || s.crm == nil {
		return
	}
	if invoice.Invoice.OrderID == nil {
		return
	}
	if invoice.Invoice.InvoiceType != domain.InvoiceTypeOTC {
		return
	}
	if invoice.Invoice.Status != domain.InvoiceStatusPaid {
		return
	}
	orderID := *invoice.Invoice.OrderID

	// Idempotency: don't double-apply.
	if exists, _ := s.commissions.ExistsForOrder(ctx, orderID); exists {
		return
	}

	order, err := s.crm.OrderWithCustomer(ctx, orderID)
	if err != nil || order == nil {
		s.logErr("commission: order lookup failed", err)
		return
	}
	if order.MonthlyPrice <= 0 || order.SalesID == nil {
		return // no commission base or no sales user
	}

	// Walk org chart for the manager.
	managerID, _ := s.crm.ManagerOfSales(ctx, *order.SalesID)
	salesBranch, _ := s.crm.SalesBranchOf(ctx, *order.SalesID)

	// Cross-branch detection: infrastructure_branch only when the
	// installation branch differs from the sales branch. For round-2
	// we use the order's branch_id (denormalised at convert time) vs
	// the sales user's branch. When they match, the 10% infrastructure
	// share folds into 'company'.
	isCrossBranch := false
	if order.OrderBranchID != nil && salesBranch != nil && *order.OrderBranchID != *salesBranch {
		isCrossBranch = true
	}

	// Resolve the commission schema for this customer (or DEFAULT if
	// nothing specific). Falls back to the hardcoded
	// DefaultCommissionPercents map when the resolver is nil or no
	// commission schema is published.
	split := s.resolveCommissionSplit(ctx, invoice.Invoice.CustomerID)

	rows := buildCommissionRowsWithSplit(
		order, payment, invoice,
		managerID, salesBranch, isCrossBranch, split,
	)
	for _, rec := range rows {
		if err := s.commissions.Create(ctx, &rec); err != nil {
			s.logErr("commission: create failed", err)
		}
	}
}

// resolveCommissionSplit fetches the per-customer 5-party split. When
// the resolver isn't wired or no commission schema is published the
// split mirrors DefaultCommissionPercents 1:1, so historical
// behaviour is preserved.
func (s *Service) resolveCommissionSplit(ctx context.Context, customerID uuid.UUID) commissionSplit {
	if s.schemaPolicy == nil {
		return commissionSplit{
			RepPct:         domain.DefaultCommissionPercents[domain.PartySalesPerson],
			MgrPct:         domain.DefaultCommissionPercents[domain.PartySalesManager],
			BranchSalesPct: domain.DefaultCommissionPercents[domain.PartySalesBranch],
			BranchInfraPct: domain.DefaultCommissionPercents[domain.PartyInfrastructureBranch],
			HoldingPct:     domain.DefaultCommissionPercents[domain.PartyCompany],
		}
	}
	return s.schemaPolicy.resolvedCommissionFor(ctx, customerID)
}

// buildCommissionRowsWithSplit is the schema-driven variant. Pure
// func, no I/O — easy to unit-test. `split` carries the 5-party
// percentages (percent-shape; the schema_policy resolver normalises
// fraction-shape bodies). The same cross-branch fold rule applies:
// when the install is same-branch, the infra share collapses into
// the holding bucket.
func buildCommissionRowsWithSplit(
	order *port.RecurringOrder,
	payment *domain.Payment,
	invoice *port.InvoiceView,
	managerID *uuid.UUID,
	salesBranch *uuid.UUID,
	crossBranch bool,
	split commissionSplit,
) []domain.CommissionRecord {
	base := order.MonthlyPrice
	now := time.Now().UTC()
	invoiceID := invoice.Invoice.ID
	paymentID := payment.ID

	infraPct := split.BranchInfraPct
	companyPct := split.HoldingPct
	if !crossBranch {
		companyPct += infraPct
		infraPct = 0
	}

	mk := func(party domain.PartyType, pct float64, userID, branchID *uuid.UUID, notes string) domain.CommissionRecord {
		amount := round2(base * pct / 100.0)
		return domain.CommissionRecord{
			ID:         uuid.New(),
			OrderID:    order.OrderID,
			CustomerID: order.CustomerID,
			InvoiceID:  &invoiceID,
			PaymentID:  &paymentID,
			PartyType:  party,
			UserID:     userID,
			BranchID:   branchID,
			Amount:     amount,
			Percentage: pct,
			BaseAmount: base,
			Notes:      notes,
			CreatedAt:  now,
		}
	}

	rows := []domain.CommissionRecord{
		mk(domain.PartySalesPerson, split.RepPct, order.SalesID, nil, ""),
	}
	if managerID != nil {
		rows = append(rows, mk(domain.PartySalesManager,
			split.MgrPct, managerID, nil, "via reports_to walk"))
	}
	if salesBranch != nil {
		rows = append(rows, mk(domain.PartySalesBranch,
			split.BranchSalesPct, nil, salesBranch, ""))
	}
	if crossBranch && order.OrderBranchID != nil {
		rows = append(rows, mk(domain.PartyInfrastructureBranch, infraPct,
			nil, order.OrderBranchID, "cross-branch installation"))
	}
	companyNotes := "residual"
	if !crossBranch {
		companyNotes = "residual + infra (same-branch)"
	}
	rows = append(rows, mk(domain.PartyCompany, companyPct, nil, nil, companyNotes))
	return rows
}

// round2 mirrors invoice domain helpers.
func round2(f float64) float64 {
	if f >= 0 {
		return float64(int64(f*100+0.5)) / 100
	}
	return float64(int64(f*100-0.5)) / 100
}

func (s *Service) logErr(msg string, err error) {
	if s.log != nil && err != nil {
		s.log.Error(msg, "err", err)
	}
}
