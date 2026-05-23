// Package cron is the customer-service bounded context's cron runner.
//
// Loops:
//   - MentionReminderTick   — every 4 hours, find unread mentions
//     older than 24h, re-dispatch notification (rate-limited via the
//     mention's own read_at timestamp; a successful read clears the
//     loop entirely).
//   - AutoCloseResolvedTick — daily, find resolved tickets older than
//     14 days and auto-close them with an actor='system' event.
//   - SLABreachEvaluator    — every 5 minutes (Wave 124), sweep
//     in-flight tickets and flip sla_breached_* flags / dispatch
//     warning notifications. Idempotent.
//   - CSATFollowupTick      — daily (Wave 124), re-send CSAT invites
//     for tickets resolved 24+h ago without a response. Each ticket
//     gets a single re-invite.
//
// Safe to start with nil services — a missing TicketService /
// MentionService / SLAService / CSATService just means the loop spins
// but no-ops.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/internal/cs/usecase"
)

// Runner owns the CS cron goroutines.
type Runner struct {
	log *slog.Logger

	tickets       *usecase.TicketService
	mentions      *usecase.MentionService
	autoCloseRepo port.AutoCloseRepository

	// Wave 124 add-ons. nil-safe.
	sla  *usecase.SLAService
	csat *usecase.CSATService

	// Wave 126 add-on. nil-safe.
	dashboard *usecase.CSDashboardService

	mentionInterval    time.Duration
	mentionAgeCutoff   time.Duration
	autoCloseInterval  time.Duration
	autoCloseCutoff    time.Duration
	slaInterval        time.Duration
	csatFollowupTick   time.Duration
	csatFollowupCutoff time.Duration
	csatFollowupSpan   time.Duration
	dashboardInterval  time.Duration
}

// New builds a Runner with defaults: mention reminder every 4h
// (cutoff 24h), auto-close every 24h (cutoff 14 days).
func New(log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		log:                log.With("component", "cs.cron"),
		mentionInterval:    4 * time.Hour,
		mentionAgeCutoff:   24 * time.Hour,
		autoCloseInterval:  24 * time.Hour,
		autoCloseCutoff:    14 * 24 * time.Hour,
		slaInterval:        5 * time.Minute,
		csatFollowupTick:   24 * time.Hour,
		csatFollowupCutoff: 24 * time.Hour,
		csatFollowupSpan:   7 * 24 * time.Hour,
		dashboardInterval:  15 * time.Minute,
	}
}

// WithDashboardService registers the dashboard precompute service.
// nil → dashboard precompute loop no-ops.
func (r *Runner) WithDashboardService(svc *usecase.CSDashboardService) *Runner {
	r.dashboard = svc
	return r
}

// WithDashboardInterval overrides the dashboard precompute cadence.
func (r *Runner) WithDashboardInterval(d time.Duration) *Runner {
	if d > 0 {
		r.dashboardInterval = d
	}
	return r
}

// WithTicketService registers the ticket service. nil → auto-close loop no-ops.
func (r *Runner) WithTicketService(svc *usecase.TicketService) *Runner {
	r.tickets = svc
	return r
}

// WithMentionService registers the mention service. nil → reminder loop no-ops.
func (r *Runner) WithMentionService(svc *usecase.MentionService) *Runner {
	r.mentions = svc
	return r
}

// WithAutoCloseRepo registers the auto-close repository (typically
// the same ticket repo from postgres).
func (r *Runner) WithAutoCloseRepo(repo port.AutoCloseRepository) *Runner {
	r.autoCloseRepo = repo
	return r
}

// WithMentionInterval overrides the reminder cadence.
func (r *Runner) WithMentionInterval(d time.Duration) *Runner {
	if d > 0 {
		r.mentionInterval = d
	}
	return r
}

// WithAutoCloseInterval overrides the auto-close cadence.
func (r *Runner) WithAutoCloseInterval(d time.Duration) *Runner {
	if d > 0 {
		r.autoCloseInterval = d
	}
	return r
}

// WithSLAService registers the SLA evaluator. nil → SLA loop no-ops.
func (r *Runner) WithSLAService(svc *usecase.SLAService) *Runner {
	r.sla = svc
	return r
}

// WithCSATService registers the CSAT followup loop. nil → CSAT loop no-ops.
func (r *Runner) WithCSATService(svc *usecase.CSATService) *Runner {
	r.csat = svc
	return r
}

// WithSLAInterval overrides the SLA breach-evaluator cadence.
func (r *Runner) WithSLAInterval(d time.Duration) *Runner {
	if d > 0 {
		r.slaInterval = d
	}
	return r
}

// Start spawns the cron goroutines. The context drives shutdown —
// cancel it and the goroutines exit between ticks.
func (r *Runner) Start(ctx context.Context) {
	if r.mentions != nil {
		go r.runMentionReminderLoop(ctx)
	}
	if r.tickets != nil && r.autoCloseRepo != nil {
		go r.runAutoCloseLoop(ctx)
	}
	if r.sla != nil {
		go r.runSLABreachLoop(ctx)
	}
	if r.csat != nil {
		go r.runCSATFollowupLoop(ctx)
	}
	if r.dashboard != nil {
		go r.runDashboardPrecomputeLoop(ctx)
	}
}

func (r *Runner) runMentionReminderLoop(ctx context.Context) {
	// 90-second startup offset to avoid migration / boot races.
	if !sleep(ctx, 90*time.Second) {
		return
	}
	r.tickMentionReminder(ctx)
	t := time.NewTicker(r.mentionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickMentionReminder(ctx)
		}
	}
}

func (r *Runner) tickMentionReminder(ctx context.Context) {
	if r.mentions == nil {
		return
	}
	cutoff := time.Now().UTC().Add(-r.mentionAgeCutoff)
	n, err := r.mentions.ReDispatchUnreadOlderThan(ctx, cutoff, 200)
	if err != nil {
		r.log.Warn("cs mention reminder tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("cs mention reminder tick", "redispatched", n, "cutoff", cutoff)
	}
}

func (r *Runner) runAutoCloseLoop(ctx context.Context) {
	if !sleep(ctx, 120*time.Second) {
		return
	}
	r.tickAutoClose(ctx)
	t := time.NewTicker(r.autoCloseInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickAutoClose(ctx)
		}
	}
}

func (r *Runner) tickAutoClose(ctx context.Context) {
	if r.tickets == nil || r.autoCloseRepo == nil {
		return
	}
	cutoff := time.Now().UTC().Add(-r.autoCloseCutoff)
	n, err := r.tickets.AutoCloseResolved(ctx, r.autoCloseRepo, cutoff, 200)
	if err != nil {
		r.log.Warn("cs auto-close tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("cs auto-close tick", "closed", n, "cutoff", cutoff)
	}
}

// =====================================================================
// Wave 124 — SLA breach evaluator + CSAT followup loops
// =====================================================================

func (r *Runner) runSLABreachLoop(ctx context.Context) {
	// Stagger the first tick by 75s — gives the DB pool + matrix seed
	// a chance to settle.
	if !sleep(ctx, 75*time.Second) {
		return
	}
	r.tickSLABreach(ctx)
	t := time.NewTicker(r.slaInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickSLABreach(ctx)
		}
	}
}

func (r *Runner) tickSLABreach(ctx context.Context) {
	if r.sla == nil {
		return
	}
	report, err := r.sla.EvaluateBreaches(ctx)
	if err != nil {
		r.log.Warn("cs sla breach tick failed", "err", err)
		return
	}
	if report.NewFirstResponseBreach > 0 || report.NewResolveBreach > 0 || report.WarningsDispatched > 0 {
		r.log.Info("cs sla breach tick",
			"evaluated", report.Evaluated,
			"first_response_breaches", report.NewFirstResponseBreach,
			"resolve_breaches", report.NewResolveBreach,
			"warnings", report.WarningsDispatched,
		)
	}
}

func (r *Runner) runCSATFollowupLoop(ctx context.Context) {
	if !sleep(ctx, 5*time.Minute) {
		return
	}
	r.tickCSATFollowup(ctx)
	t := time.NewTicker(r.csatFollowupTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickCSATFollowup(ctx)
		}
	}
}

func (r *Runner) tickCSATFollowup(ctx context.Context) {
	if r.csat == nil {
		return
	}
	now := time.Now().UTC()
	resolvedBefore := now.Add(-r.csatFollowupCutoff)
	resolvedSince := now.Add(-r.csatFollowupSpan)
	sent, err := r.csat.FollowupTick(ctx, resolvedSince, resolvedBefore, 200)
	if err != nil {
		r.log.Warn("cs csat followup tick failed", "err", err)
		return
	}
	if sent > 0 {
		r.log.Info("cs csat followup tick", "sent", sent, "since", resolvedSince, "before", resolvedBefore)
	}
}

// =====================================================================
// Wave 126 — dashboard aggregation precompute loop
// =====================================================================

func (r *Runner) runDashboardPrecomputeLoop(ctx context.Context) {
	if !sleep(ctx, 3*time.Minute) {
		return
	}
	r.tickDashboardPrecompute(ctx)
	t := time.NewTicker(r.dashboardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickDashboardPrecompute(ctx)
		}
	}
}

func (r *Runner) tickDashboardPrecompute(ctx context.Context) {
	if r.dashboard == nil {
		return
	}
	n, err := r.dashboard.PrecomputeTick(ctx)
	if err != nil {
		r.log.Warn("cs dashboard precompute tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("cs dashboard precompute tick", "rows", n)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
