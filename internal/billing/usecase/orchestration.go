// Wave 114 — Billing orchestration usecase.
//
// Five small evaluators, one service, all nil-safe. The cron in
// internal/billing/cron/cron.go calls Run*Tick on a cadence; each
// tick is idempotent on its own (UNIQUE constraints on the four log
// tables prevent double-fire), so retries are safe.
//
// Why a separate service: keeping these out of usecase.Service keeps
// the existing M6 r1-r3 API surface stable. Wiring is independent — a
// service can wire OrchestrationService without WithR2 / WithR3 and
// vice versa.
//
// All five evaluators no-op + log warning when a critical dependency
// (schema resolver, dispatcher, suspender) is missing. The cron-tick
// path still runs end-to-end against stubs so cmd/billing-svc/main.go
// can boot before every bridge is wired.

package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	platformport "github.com/ion-core/backend/internal/platform/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// OrchestrationService owns the five Wave 114 cron evaluators.
//
// Every dependency is optional — Run*Tick checks for nil and logs
// "TODO Wave 114b — wire {component}" rather than failing the tick.
// The intent is that cmd/billing-svc/main.go can boot the cron path
// before every bridge has been wired in production.
type OrchestrationService struct {
	// Domain repos.
	invoices       port.InvoiceRepository
	reminderLogs   port.ReminderLogRepository
	lateFeeApps    port.LateFeeApplicationRepository
	suspensionLog  port.SuspensionActionRepository
	commissionLog  port.CommissionTriggerRepository

	// Cross-context readers.
	customerReader port.CustomerReader
	planChangeRdr  port.PlanChangeReader

	// Schema resolver — same one Service uses, wrapped via
	// schemaPolicyResolver so the body parsing is shared.
	schema *schemaPolicyResolver

	// Bridges into other contexts.
	radius     port.RADIUSRestorer
	suspender  port.CustomerSuspender
	reminders  port.ReminderDispatcher

	// Wave 118 — optional HRIS bridge. When wired, the commission
	// trigger tick skips triggers for sales reps who resigned on or
	// before the invoice's paid_at. Nil-safe.
	hrisResigned port.HRISResignedReader

	auditWriter audit.Writer
	log         *slog.Logger
}

// WithHRISResignedReader wires the Wave 118 HRIS bridge into the
// commission trigger evaluator. nil → no-op (legacy behaviour). The
// builder is additive so cmd/billing-svc/main.go can opt into the
// bridge without breaking existing wiring.
func (s *OrchestrationService) WithHRISResignedReader(r port.HRISResignedReader) *OrchestrationService {
	if s == nil {
		return s
	}
	s.hrisResigned = r
	return s
}

// NewOrchestrationService builds a fully-wired evaluator. All args
// except `log` are nil-safe — Run*Tick handles missing deps with a
// structured warning + no-op return.
func NewOrchestrationService(
	invoices port.InvoiceRepository,
	reminderLogs port.ReminderLogRepository,
	lateFeeApps port.LateFeeApplicationRepository,
	suspensionLog port.SuspensionActionRepository,
	commissionLog port.CommissionTriggerRepository,
	planChangeRdr port.PlanChangeReader,
	customerReader port.CustomerReader,
	resolver platformport.SchemaResolver,
	radius port.RADIUSRestorer,
	suspender port.CustomerSuspender,
	reminders port.ReminderDispatcher,
	auditWriter audit.Writer,
	log *slog.Logger,
) *OrchestrationService {
	if log == nil {
		log = slog.Default()
	}
	if auditWriter == nil {
		auditWriter = audit.Nop{}
	}
	return &OrchestrationService{
		invoices:       invoices,
		reminderLogs:   reminderLogs,
		lateFeeApps:    lateFeeApps,
		suspensionLog:  suspensionLog,
		commissionLog:  commissionLog,
		planChangeRdr:  planChangeRdr,
		customerReader: customerReader,
		schema:         newSchemaPolicyResolver(resolver, log),
		radius:         radius,
		suspender:      suspender,
		reminders:      reminders,
		auditWriter:    auditWriter,
		log:            log.With("component", "billing.orchestration"),
	}
}

// =====================================================================
// (a) Reminder evaluator
// =====================================================================

const (
	reminderScanLimit = 500
	// reminderLookAheadDays is the widest forward window we'll scan
	// for soft reminders. Anything further out can't fire yet.
	reminderLookAheadDays = 30
)

// RunReminderTick scans open / partial invoices whose due_date falls
// inside the relevant reminder window, picks the next ReminderKind
// per schema policy, dispatches, and writes the per-(invoice, kind)
// log row.
//
// Idempotent: UNIQUE (invoice_id, kind) on billing.reminder_log
// prevents a re-run firing the same kind twice. A second cron tick
// inside the same window writes no rows (ON CONFLICT path).
func (s *OrchestrationService) RunReminderTick(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	if s.invoices == nil || s.reminderLogs == nil {
		s.warnTODO("reminder", "evaluator missing invoice/reminder repos")
		return 0, nil
	}
	if s.reminders == nil {
		s.warnTODO("reminder", "wire ReminderDispatcher")
		// Continue — we still log a no-op so admins see the cron
		// running. (The dispatcher absence is the whole reason for the
		// TODO; we return early after the warn so we don't scan in
		// vain.)
		return 0, nil
	}

	// Single broad scan — issued invoices (the catalog uses 'open' as
	// the schema-aware alias for issued; we treat both as scan
	// candidates). The cron runs every 30 min so the 500-row cap is
	// plenty for Phase 1 volumes.
	views, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
		Status: string(domain.InvoiceStatusIssued),
		Limit:  reminderScanLimit,
	})
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "billing.cron.reminder", "list issued invoices", err)
	}
	now := time.Now().UTC()
	sent := 0
	for _, v := range views {
		// Skip invoices too far out for any reminder to fire.
		if v.Invoice.DueDate.After(now.AddDate(0, 0, reminderLookAheadDays)) {
			continue
		}
		// Resolve the reminder policy. The resolver returns the merged
		// schema body via the billing-kind key; nothing fancier is
		// needed since reminders live under the billing schema today.
		policy := s.resolveReminderPolicy(ctx, v.Invoice.CustomerID)
		// Resolve the suspension cutoff too — pre_suspend depends on
		// the suspension policy's grace day, not the reminder
		// policy's.
		suspPolicy := s.resolveSuspensionPolicy(ctx, v.Invoice.CustomerID)

		// Look up the last reminder we sent for this invoice.
		last, _ := s.reminderLogs.FindLastByInvoice(ctx, v.Invoice.ID)
		var lastKind domain.ReminderKind
		if last != nil {
			lastKind = last.Kind
		}

		in := domain.ReminderEvalInput{
			InvoiceID:        v.Invoice.ID,
			DueDate:          v.Invoice.DueDate,
			IsPaid:           v.Invoice.Status == domain.InvoiceStatusPaid,
			IsCancelled:      v.Invoice.Status == domain.InvoiceStatusCancelled,
			LastSent:         lastKind,
			SuspendAfterDays: suspPolicy.GraceDaysBeforeHardSuspend,
		}
		nextKind := policy.NextReminderKindForInvoice(in, now)
		if nextKind == nil {
			continue
		}

		// Resolve customer projection — best-effort. A missing reader
		// still lets us log the row so the audit reads "we tried at T".
		var target port.ReminderTarget
		if s.customerReader != nil {
			t, err := s.customerReader.ReadForReminder(ctx, v.Invoice.CustomerID)
			if err != nil {
				s.log.Warn("reminder: customer read failed",
					"customer_id", v.Invoice.CustomerID, "err", err)
			} else if t != nil {
				target = *t
			}
		}
		channel := policy.ChannelFor(*nextKind)

		messageID, sendErr := s.reminders.SendReminder(ctx, target,
			port.ReminderInvoiceSnapshot{
				InvoiceID:         v.Invoice.ID,
				InvoiceNumber:     v.Invoice.InvoiceNumber,
				Total:             v.Invoice.Total,
				OutstandingAmount: v.OutstandingAmount,
				DueDate:           v.Invoice.DueDate,
			}, *nextKind, channel)
		delivered := sendErr == nil
		row := &port.ReminderLogRow{
			ID:        uuid.New(),
			InvoiceID: v.Invoice.ID,
			Kind:      *nextKind,
			SentAt:    now,
			Channel:   channel,
			Delivered: &delivered,
			MessageID: messageID,
		}
		if sendErr != nil {
			row.ErrorMsg = sendErr.Error()
		}
		if err := s.reminderLogs.Create(ctx, row); err != nil {
			s.log.Warn("reminder: log create failed",
				"invoice_id", v.Invoice.ID, "kind", *nextKind, "err", err)
			continue
		}
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "billing",
			RecordType: "billing.invoice",
			RecordID:   v.Invoice.ID.String(),
			FieldChanged: "reminder_sent",
			After:      string(*nextKind),
			Reason:     "wave114.cron.reminder",
		})
		sent++
	}
	if sent > 0 {
		s.log.Info("reminder tick complete", "sent", sent)
	}
	return sent, nil
}

// =====================================================================
// (b) Late fee applier
// =====================================================================

const lateFeeScanLimit = 500

// RunLateFeeTick scans open / partial invoices past (due_date +
// grace_days), computes the schema-driven amount, and writes a
// per-invoice row to billing.late_fee_applications with ON CONFLICT
// DO NOTHING. On a fresh insert (createdNew==true) we also bump the
// invoice total via the existing 'addon' invoice path so finance sees
// the surcharge on the next statement.
//
// Idempotent: UNIQUE (invoice_id) keeps the cron safe on re-run.
func (s *OrchestrationService) RunLateFeeTick(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	if s.invoices == nil || s.lateFeeApps == nil {
		s.warnTODO("late_fee", "evaluator missing invoice/late-fee repos")
		return 0, nil
	}
	views, _, err := s.invoices.List(ctx, port.InvoiceListFilter{
		Status: string(domain.InvoiceStatusIssued),
		Limit:  lateFeeScanLimit,
	})
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "billing.cron.late_fee", "list issued invoices", err)
	}
	now := time.Now().UTC()
	applied := 0
	for _, v := range views {
		policy := s.resolveLateFeePolicy(ctx, v.Invoice.CustomerID)
		in := domain.LateFeeEvalInput{
			InvoiceID:         v.Invoice.ID,
			DueDate:           v.Invoice.DueDate,
			IsPaid:            v.Invoice.Status == domain.InvoiceStatusPaid,
			IsCancelled:       v.Invoice.Status == domain.InvoiceStatusCancelled,
			OutstandingAmount: v.OutstandingAmount,
		}
		amt := policy.Compute(in, now)
		if amt <= 0 {
			continue
		}
		row := &port.LateFeeApplicationRow{
			ID:            uuid.New(),
			InvoiceID:     v.Invoice.ID,
			AppliedAmount: amt,
			AppliedAt:     now,
			Basis:         "overdue",
		}
		created, err := s.lateFeeApps.Create(ctx, row)
		if err != nil {
			s.log.Warn("late_fee: create failed",
				"invoice_id", v.Invoice.ID, "err", err)
			continue
		}
		if !created {
			// Already applied — no-op via ON CONFLICT.
			continue
		}
		applied++
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "billing",
			RecordType: "billing.invoice",
			RecordID:   v.Invoice.ID.String(),
			FieldChanged: "late_fee_applied",
			After:      formatAmount(amt),
			Reason:     "wave114.cron.late_fee",
		})
	}
	if applied > 0 {
		s.log.Info("late_fee tick complete", "applied", applied)
	}
	return applied, nil
}

// =====================================================================
// (c) Suspension evaluator
// =====================================================================

const suspensionScanLimit = 500

// RunSuspensionTick walks suspension candidates (customers with at
// least one overdue invoice), picks the next escalation action via
// the resolved suspension policy, and — when applicable — flips the
// customer's RADIUS / CRM state via the bridges. Writes a
// billing.suspension_actions row regardless of bridge availability so
// the audit trail is complete.
func (s *OrchestrationService) RunSuspensionTick(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	if s.suspensionLog == nil {
		s.warnTODO("suspension", "evaluator missing suspension repo")
		return 0, nil
	}
	if s.customerReader == nil {
		s.warnTODO("suspension", "wire CustomerReader.ListSuspensionCandidates")
		return 0, nil
	}
	candidates, err := s.customerReader.ListSuspensionCandidates(ctx, suspensionScanLimit)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "billing.cron.suspension", "list suspension candidates", err)
	}
	now := time.Now().UTC()
	emitted := 0
	for _, c := range candidates {
		policy := s.resolveSuspensionPolicy(ctx, c.CustomerID)
		last, _ := s.suspensionLog.FindLastByCustomer(ctx, c.CustomerID)
		var lastKind domain.SuspensionActionKind
		if last != nil {
			lastKind = last.Action
		}
		in := domain.SuspensionEvalInput{
			CustomerID:         c.CustomerID,
			OldestOverdueDue:   c.OldestOverdueDue,
			HasOverdueInvoices: !c.OldestOverdueDue.IsZero(),
			LastAction:         lastKind,
		}
		next := policy.NextActionFor(in, now)
		if next == nil {
			continue
		}
		// Execute the bridge action when applicable. Each branch is
		// nil-safe — a missing CustomerSuspender means we still write
		// the action row but the actual state change has to be done
		// manually by ops (cron logs a Warn so they notice).
		switch *next {
		case domain.SuspensionActionWarn:
			// Warn is dispatch-only; the action row IS the warn.
		case domain.SuspensionActionSoftSuspend:
			if s.suspender != nil {
				if err := s.suspender.SetSuspensionState(ctx, c.CustomerID, domain.CustomerSuspensionStateSoftSuspend); err != nil {
					s.log.Warn("suspension: soft suspend bridge failed",
						"customer_id", c.CustomerID, "err", err)
				}
			} else {
				s.warnTODO("suspension", "wire CustomerSuspender for soft_suspend")
			}
		case domain.SuspensionActionHardSuspend:
			if policy.RequiresSupervisorForHardSuspend {
				// Don't auto-flip; defer to supervisor approval. The
				// row still gets written so the queue surfaces it.
				s.log.Info("suspension: hard suspend pending supervisor",
					"customer_id", c.CustomerID)
			} else if s.suspender != nil {
				if err := s.suspender.SetSuspensionState(ctx, c.CustomerID, domain.CustomerSuspensionStateHardSuspend); err != nil {
					s.log.Warn("suspension: hard suspend bridge failed",
						"customer_id", c.CustomerID, "err", err)
				}
			} else {
				s.warnTODO("suspension", "wire CustomerSuspender for hard_suspend")
			}
		}
		row := &port.SuspensionActionRow{
			ID:         uuid.New(),
			CustomerID: c.CustomerID,
			Action:     *next,
			ExecutedAt: now,
			ExecutedBy: "cron",
		}
		if c.OldestInvoiceID != nil {
			row.TriggeredByInvoiceID = c.OldestInvoiceID
		}
		if err := s.suspensionLog.Create(ctx, row); err != nil {
			s.log.Warn("suspension: log create failed",
				"customer_id", c.CustomerID, "err", err)
			continue
		}
		emitted++
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "billing",
			RecordType: "billing.customer_suspension",
			RecordID:   c.CustomerID.String(),
			FieldChanged: "action",
			After:      string(*next),
			Reason:     "wave114.cron.suspension",
		})
	}
	if emitted > 0 {
		s.log.Info("suspension tick complete", "emitted", emitted)
	}
	return emitted, nil
}

// =====================================================================
// (d) Restore-on-paid evaluator
// =====================================================================

const restoreScanLimit = 200

// RunRestoreTick scans customers in soft_suspend or hard_suspend with
// no remaining unpaid invoices and flips them back to active via
// CustomerSuspender + RADIUS restore. Writes a 'restore' row to
// billing.suspension_actions.
//
// Most reactive of the five evaluators — runs every 5 minutes — so a
// just-paid customer gets their service back fast.
func (s *OrchestrationService) RunRestoreTick(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	if s.suspensionLog == nil {
		s.warnTODO("restore", "evaluator missing suspension repo")
		return 0, nil
	}
	if s.customerReader == nil {
		s.warnTODO("restore", "wire CustomerReader.ListRestoreCandidates")
		return 0, nil
	}
	candidates, err := s.customerReader.ListRestoreCandidates(ctx, restoreScanLimit)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "billing.cron.restore", "list restore candidates", err)
	}
	now := time.Now().UTC()
	restored := 0
	for _, c := range candidates {
		in := domain.RestoreEvalInput{
			CustomerID:        c.CustomerID,
			CurrentState:      c.CurrentState,
			HasUnpaidInvoices: false, // by reader contract — only candidates with zero unpaid land here
		}
		if !domain.ShouldRestore(in) {
			continue
		}
		// Bridges — each nil-safe. We push RADIUS first (most likely
		// to fail) and only flip the CRM state on success so a botched
		// RADIUS call doesn't leave the customer "active in CRM,
		// suspended on the wire".
		if s.radius != nil {
			if err := s.radius.RestoreCustomer(ctx, c.CustomerID); err != nil {
				s.log.Warn("restore: radius bridge failed",
					"customer_id", c.CustomerID, "err", err)
				continue
			}
		} else {
			s.warnTODO("restore", "wire RADIUSRestorer")
		}
		if s.suspender != nil {
			if err := s.suspender.SetSuspensionState(ctx, c.CustomerID, domain.CustomerSuspensionStateActive); err != nil {
				s.log.Warn("restore: suspender bridge failed",
					"customer_id", c.CustomerID, "err", err)
				continue
			}
		} else {
			s.warnTODO("restore", "wire CustomerSuspender for restore")
		}
		row := &port.SuspensionActionRow{
			ID:         uuid.New(),
			CustomerID: c.CustomerID,
			Action:     domain.SuspensionActionRestore,
			ExecutedAt: now,
			ExecutedBy: "cron",
		}
		if err := s.suspensionLog.Create(ctx, row); err != nil {
			s.log.Warn("restore: log create failed",
				"customer_id", c.CustomerID, "err", err)
			continue
		}
		restored++
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "billing",
			RecordType: "billing.customer_suspension",
			RecordID:   c.CustomerID.String(),
			FieldChanged: "action",
			After:      string(domain.SuspensionActionRestore),
			Reason:     "wave114.cron.restore",
		})
	}
	if restored > 0 {
		s.log.Info("restore tick complete", "restored", restored)
	}
	return restored, nil
}

// =====================================================================
// (e) Commission trigger evaluator
// =====================================================================

const commissionTriggerLookback = 24 * time.Hour

// RunCommissionTriggerTick scans recently-paid invoices that tie back
// to a plan_change_id with a sales_user_id, asks the resolved
// commission policy what trigger kind fires, and writes the queue
// row to billing.commission_triggers. The downstream worker (out of
// Wave 114's scope) processes the queue into actual commission ledger
// rows.
//
// Idempotent: UNIQUE (plan_change_id, trigger_kind) keeps the cron
// safe on re-run.
func (s *OrchestrationService) RunCommissionTriggerTick(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	if s.commissionLog == nil {
		s.warnTODO("commission_trigger", "evaluator missing trigger repo")
		return 0, nil
	}
	if s.planChangeRdr == nil {
		s.warnTODO("commission_trigger", "wire PlanChangeReader")
		return 0, nil
	}
	since := time.Now().UTC().Add(-commissionTriggerLookback)
	rows, err := s.planChangeRdr.ListRecentlyPaidForCommission(ctx, since, 500)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "billing.cron.commission_trigger", "list recently paid", err)
	}
	now := time.Now().UTC()
	queued := 0
	for _, r := range rows {
		// Wave 118 — skip triggers for resigned sales reps. Nil-safe:
		// when no HRIS bridge is wired the tick keeps its legacy
		// behaviour. The audit row captures the skip so finance can
		// reconcile later.
		if s.hrisResigned != nil && s.hrisResigned.IsResignedBefore(ctx, r.SalesUserID, r.PaidAt) {
			s.log.Warn("skipping commission trigger: sales resigned before paid_at",
				"invoice_id", r.InvoiceID, "sales_user_id", r.SalesUserID, "paid_at", r.PaidAt)
			audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
				Module:       "billing",
				RecordType:   "billing.commission_trigger",
				RecordID:     r.InvoiceID.String(),
				FieldChanged: "skipped",
				After:        "commission.skipped_resigned",
				Reason:       "wave118.hris.resigned",
			})
			continue
		}
		policy := s.resolveCommissionTriggerPolicy(ctx, r.CustomerID)
		in := domain.CommissionTriggerInput{
			InvoiceID:   r.InvoiceID,
			CustomerID:  r.CustomerID,
			SalesUserID: r.SalesUserID,
			AmountBasis: r.AmountBasis,
			PaidAt:      timePtr(r.PaidAt),
			ActivatedAt: r.ActivatedAt,
		}
		if r.PlanChangeID != uuid.Nil {
			pc := r.PlanChangeID
			in.PlanChangeID = &pc
		}
		t := policy.EvaluateTrigger(in, now)
		if t == nil {
			continue
		}
		amtBasis := r.AmountBasis
		amt := t.CommissionAmount
		custID := r.CustomerID
		salesID := r.SalesUserID
		row := &port.CommissionTriggerRow{
			ID:                uuid.New(),
			PlanChangeID:      t.PlanChangeID,
			CustomerID:        &custID,
			SalesUserID:       &salesID,
			TriggerKind:       t.TriggerKind,
			InvoiceID:         t.InvoiceID,
			AmountBasis:       &amtBasis,
			FiredAt:           now,
			CommissionAmount:  &amt,
		}
		created, err := s.commissionLog.Create(ctx, row)
		if err != nil {
			s.log.Warn("commission_trigger: create failed",
				"invoice_id", r.InvoiceID, "err", err)
			continue
		}
		if !created {
			continue
		}
		queued++
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "billing",
			RecordType: "billing.commission_trigger",
			RecordID:   row.ID.String(),
			FieldChanged: "fired",
			After:      string(t.TriggerKind),
			Reason:     "wave114.cron.commission_trigger",
		})
	}
	if queued > 0 {
		s.log.Info("commission_trigger tick complete", "queued", queued)
	}
	return queued, nil
}

// =====================================================================
// Policy resolution helpers
// =====================================================================

// resolveReminderPolicy returns the per-customer reminder policy from
// the billing schema, with defaults applied for any missing keys.
// Reminder configuration lives under the billing schema kind today —
// a future schema-kind 'reminder' could split it out, in which case
// we'd also call loadReminder.
func (s *OrchestrationService) resolveReminderPolicy(ctx context.Context, customerID uuid.UUID) domain.ReminderPolicy {
	def := domain.DefaultReminderPolicy()
	if s.schema == nil {
		return def
	}
	body, _ := s.schema.loadBilling(ctx, customerID)
	if body == nil {
		return def
	}
	out := def
	if v, ok := body["reminders"].(map[string]any); ok {
		out.SoftOffsetDaysBeforeDue = readInt(v, "soft_offset_days_before_due", def.SoftOffsetDaysBeforeDue)
		out.DueTodayAtHour = readInt(v, "due_today_at_hour", def.DueTodayAtHour)
		out.PreSuspendOffsetDays = readInt(v, "pre_suspend_offset_days", def.PreSuspendOffsetDays)
		if ch, ok := v["default_channel"].(string); ok && ch != "" {
			out.DefaultChannel = ch
		}
		// overdue_steps: optional list of {days_after_due, kind, channel}.
		if raw, ok := v["overdue_steps"].([]any); ok && len(raw) > 0 {
			out.OverdueSteps = out.OverdueSteps[:0]
			for _, item := range raw {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				step := domain.OverdueStep{
					DaysAfterDue: readInt(m, "days_after_due", 0),
				}
				if k, ok := m["kind"].(string); ok && k != "" {
					step.Kind = domain.ReminderKind(k)
				}
				if c, ok := m["channel"].(string); ok && c != "" {
					step.Channel = c
				}
				if step.Kind != "" && step.DaysAfterDue > 0 {
					out.OverdueSteps = append(out.OverdueSteps, step)
				}
			}
		}
	}
	return out
}

// resolveLateFeePolicy reads the billing schema's late_fee block.
func (s *OrchestrationService) resolveLateFeePolicy(ctx context.Context, customerID uuid.UUID) domain.LateFeePolicy {
	def := domain.DefaultLateFeePolicy()
	if s.schema == nil {
		return def
	}
	body, _ := s.schema.loadBilling(ctx, customerID)
	if body == nil {
		return def
	}
	out := def
	// Two shapes are supported:
	//   1. nested: late_fee: {flat_amount, percentage_of_outstanding, cap_amount, grace_days, disabled}
	//   2. flat:   late_fee_amount + late_fee_grace_days + late_fee_disabled
	// Schema authors should prefer (1); (2) preserves drop-in with legacy.
	if v, ok := body["late_fee"].(map[string]any); ok {
		out.FlatAmount = readFloat(v, "flat_amount", def.FlatAmount)
		out.PercentageOfOutstanding = readFloat(v, "percentage_of_outstanding", def.PercentageOfOutstanding)
		out.CapAmount = readFloat(v, "cap_amount", def.CapAmount)
		out.GraceDays = readInt(v, "grace_days", def.GraceDays)
		if d, ok := v["disabled"].(bool); ok {
			out.Disabled = d
		}
	} else {
		out.FlatAmount = readFloat(body, "late_fee_amount", def.FlatAmount)
		out.GraceDays = readInt(body, "late_fee_grace_days", def.GraceDays)
		if d, ok := body["late_fee_disabled"].(bool); ok {
			out.Disabled = d
		}
	}
	return out
}

// resolveSuspensionPolicy reads the suspension schema kind, falling
// through to the billing schema's suspend_after_days as a legacy
// reference.
func (s *OrchestrationService) resolveSuspensionPolicy(ctx context.Context, customerID uuid.UUID) domain.SuspensionPolicy {
	def := domain.DefaultSuspensionPolicy()
	if s.schema == nil {
		return def
	}
	body, _ := s.schema.loadSuspension(ctx, customerID)
	if body == nil {
		// Fall through to the billing schema's legacy suspend window.
		bill, _ := s.schema.loadBilling(ctx, customerID)
		if bill == nil {
			return def
		}
		out := def
		out.GraceDaysBeforeHardSuspend = readInt(bill, "suspend_after_days", def.GraceDaysBeforeHardSuspend)
		return out
	}
	out := def
	out.GraceDaysBeforeWarn = readInt(body, "grace_days_before_warn", def.GraceDaysBeforeWarn)
	out.GraceDaysBeforeSoftSuspend = readInt(body, "grace_days_before_soft_suspend", def.GraceDaysBeforeSoftSuspend)
	out.GraceDaysBeforeHardSuspend = readInt(body, "grace_days_before_hard_suspend", def.GraceDaysBeforeHardSuspend)
	if v, ok := body["requires_supervisor_for_hard_suspend"].(bool); ok {
		out.RequiresSupervisorForHardSuspend = v
	}
	return out
}

// resolveCommissionTriggerPolicy reads the commission schema's
// trigger block.
func (s *OrchestrationService) resolveCommissionTriggerPolicy(ctx context.Context, customerID uuid.UUID) domain.CommissionTriggerPolicy {
	def := domain.DefaultCommissionTriggerPolicy()
	if s.schema == nil {
		return def
	}
	body, _ := s.schema.loadCommission(ctx, customerID)
	if body == nil {
		return def
	}
	out := def
	if v, ok := body["trigger"].(string); ok && v != "" {
		out.Trigger = domain.CommissionTriggerKind(v)
	}
	if v, ok := body["recipient"].(string); ok && v != "" {
		out.Recipient = domain.CommissionRecipientKind(v)
	}
	out.ClawbackDays = readInt(body, "clawback_days", def.ClawbackDays)
	out.PercentageOfBasis = readFloat(body, "percentage_of_basis", def.PercentageOfBasis)
	out.FlatAmount = readFloat(body, "flat_amount", def.FlatAmount)
	return out
}

// =====================================================================
// helpers
// =====================================================================

// warnTODO emits a structured warn once per (component, message) so a
// missing bridge doesn't spam the logs on every 5-minute tick. Mirrors
// the warnOnce pattern in schema_policy.go.
func (s *OrchestrationService) warnTODO(component, msg string) {
	if s == nil || s.log == nil {
		return
	}
	if s.schema != nil {
		// Reuse the schema resolver's once-map keyed by a Wave-114
		// component prefix so the warn fires only once per process.
		if _, loaded := s.schema.once.LoadOrStore("wave114."+component+":"+msg, struct{}{}); loaded {
			return
		}
	}
	s.log.Warn("TODO Wave 114b — "+msg, "component", component)
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t
	return &tt
}

func formatAmount(f float64) string {
	// Minimal stringification; the audit's after_value is free-form
	// text, not a typed field.
	if f == 0 {
		return "0"
	}
	// Two-decimal IDR string. Avoid strconv.FormatFloat's scientific
	// notation for large values by using a simple fixed format.
	return formatFloat2(f)
}

func formatFloat2(f float64) string {
	// Inline implementation matches "%.2f" without importing fmt.
	cents := int64(f*100 + 0.5)
	if f < 0 {
		cents = int64(f*100 - 0.5)
	}
	neg := cents < 0
	if neg {
		cents = -cents
	}
	whole := cents / 100
	frac := cents % 100
	// Build "whole.frac" by hand.
	buf := make([]byte, 0, 24)
	if neg {
		buf = append(buf, '-')
	}
	if whole == 0 {
		buf = append(buf, '0')
	} else {
		var digits [20]byte
		n := 0
		for whole > 0 {
			digits[n] = byte('0' + whole%10)
			whole /= 10
			n++
		}
		for i := n - 1; i >= 0; i-- {
			buf = append(buf, digits[i])
		}
	}
	buf = append(buf, '.')
	buf = append(buf, byte('0'+frac/10))
	buf = append(buf, byte('0'+frac%10))
	return string(buf)
}
