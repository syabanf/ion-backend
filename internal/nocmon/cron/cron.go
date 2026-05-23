// Package cron ships the periodic workers for the NOC monitoring
// bounded context:
//
//   - ProbeRunnerTick       — every 60s; runs due probes in parallel,
//     records samples, and auto-opens a fault when a probe transitions
//     to Critical for 2+ consecutive samples (anti-flap).
//
//   - FiberAttenuationTick  — once a day; sweeps stale fiber_links
//     and flips them to 'offline'. The SNMP poller integration ships
//     in a follow-up wave.
//
//   - FaultDigestTick       — hourly; finds open faults with no ack
//     and emits a nocmon.fault.unacked audit row for ops review.
//
// Each tick is idempotent: re-running it within the same minute
// produces the same outcome (the underlying repos guard duplicates
// via UNIQUE indexes + state-machine refusals).
package cron

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/audit"
)

// runnerConcurrency caps the number of probe runners that fire in
// parallel per tick. 50 is well below pgxpool defaults (25 conns × 2
// services) so we never starve the pool. Sized as a balance between
// "finish the working set inside one minute" and "don't overwhelm
// network egress" — the swap-in to real ICMP probes can tune this
// without changing the tick frequency.
const runnerConcurrency = 50

// probeBatchSize caps how many due probes one tick will pick up.
// At 1k probes × interval=60s that's well below the cap; backfill
// scenarios (catch-up after downtime) self-throttle across ticks.
const probeBatchSize = 500

// fiberOfflineWindow matches domain.fiberOfflineWindow. We re-declare
// it here as a const so the cron tick doesn't import a private const
// from domain — keeps the boundary clean.
const fiberOfflineWindow = 24 * time.Hour

// unackedFaultThreshold is how long an open fault can sit without
// being acknowledged before the digest tick emits a flagging audit
// row. 30 minutes is the operations contract from the PRD.
const unackedFaultThreshold = 30 * time.Minute

// FaultOpener is the narrow slice of the FaultService that the
// runner tick needs — keeps the cron decoupled from the full
// usecase surface (easier to test with a fake).
type FaultOpener interface {
	OpenFault(ctx context.Context, in port.OpenFaultInput) (*domain.FaultEvent, error)
}

// Runner owns the lifetime of all three nocmon cron workers. Start()
// spawns one goroutine per worker; Stop = cancel the supplied ctx.
type Runner struct {
	pool        *pgxpool.Pool
	log         *slog.Logger
	probes      port.ServiceProbeRepository
	samples     port.HealthSampleRepository
	links       port.FiberLinkRepository
	faults      port.FaultEventRepository
	faultOpener FaultOpener
	runners     map[domain.ProbeKind]port.ProbeRunner
	audit       audit.Writer
	enabled     bool // when false, ProbeRunnerTick skips the actual runner call
}

// New constructs a Runner. The probe-runner dispatch table is keyed
// by domain.ProbeKind; callers wire the per-kind runners via
// WithProbeRunners. Audit writer is required for the digest tick;
// pass audit.Nop{} if you don't want the rows.
func New(
	pool *pgxpool.Pool,
	log *slog.Logger,
	probes port.ServiceProbeRepository,
	samples port.HealthSampleRepository,
	links port.FiberLinkRepository,
	faults port.FaultEventRepository,
	faultOpener FaultOpener,
	w audit.Writer,
) *Runner {
	if w == nil {
		w = audit.Nop{}
	}
	return &Runner{
		pool:        pool,
		log:         log.With("component", "nocmon.cron"),
		probes:      probes,
		samples:     samples,
		links:       links,
		faults:      faults,
		faultOpener: faultOpener,
		runners:     map[domain.ProbeKind]port.ProbeRunner{},
		audit:       w,
	}
}

// WithProbeRunners registers a per-kind probe runner. Call once per
// runner before Start(). The cron dispatches by kind; missing kinds
// are skipped with a warning so a partial registration doesn't kill
// the whole tick.
func (r *Runner) WithProbeRunners(runners []port.ProbeRunner) *Runner {
	for _, rn := range runners {
		r.runners[rn.Kind()] = rn
	}
	return r
}

// WithEnabled flips between stub-mode (the default) and real-mode.
// Today both routes use the same DefaultRunners (stubs); the future
// real-mode swap-in here is a one-line change.
func (r *Runner) WithEnabled(enabled bool) *Runner {
	r.enabled = enabled
	return r
}

// Start spawns one goroutine per worker. The context controls
// lifetime — cancel it (e.g. on SIGTERM) and they exit between ticks.
func (r *Runner) Start(ctx context.Context) {
	go r.runProbeTick(ctx)
	go r.runFiberAttenuationTick(ctx)
	go r.runFaultDigestTick(ctx)
}

// =====================================================================
// Probe runner tick — every 60s
// =====================================================================

func (r *Runner) runProbeTick(ctx context.Context) {
	// First tick fires ~2 minutes after boot so migrations + DI are
	// settled. After that, every 60s.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	r.tickProbes(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickProbes(ctx)
		}
	}
}

// tickProbes runs the working set in parallel with a concurrency cap.
// After every sample it checks the anti-flap rule (2+ consecutive
// criticals on the same probe → open a probe_critical fault). The
// FaultOpener is best-effort; a failure there logs but doesn't fail
// the sample (the dashboard still has the data).
func (r *Runner) tickProbes(ctx context.Context) {
	now := time.Now().UTC()
	due, err := r.probes.ListDue(ctx, now, probeBatchSize)
	if err != nil {
		r.log.Warn("probe_tick: list_due failed", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	r.log.Info("probe_tick: running", "count", len(due), "enabled", r.enabled)

	sem := make(chan struct{}, runnerConcurrency)
	var wg sync.WaitGroup
	for i := range due {
		probe := due[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.runOneProbe(ctx, &probe)
		}()
	}
	wg.Wait()
}

func (r *Runner) runOneProbe(ctx context.Context, p *domain.ServiceProbe) {
	runner, ok := r.runners[p.ProbeKind]
	if !ok {
		r.log.Warn("probe_tick: no runner for kind", "kind", p.ProbeKind, "probe_id", p.ID)
		return
	}
	value, status, err := runner.Run(ctx, p)
	if err != nil {
		r.log.Warn("probe_tick: runner failed", "probe_id", p.ID, "err", err)
		return
	}
	now := time.Now().UTC()
	v := value
	sample := &domain.HealthSample{
		ID:        uuid.New(),
		ProbeID:   p.ID,
		SampledAt: now,
		Value:     &v,
		Status:    status,
	}
	if err := r.samples.Insert(ctx, sample); err != nil {
		r.log.Warn("probe_tick: insert sample failed", "probe_id", p.ID, "err", err)
		return
	}
	if err := r.probes.UpdateLastSample(ctx, p.ID, status, now); err != nil {
		r.log.Warn("probe_tick: update last sample failed", "probe_id", p.ID, "err", err)
	}

	// Anti-flap: only open a fault when this is the SECOND consecutive
	// critical sample. CountConsecutive includes the just-inserted
	// sample (we just wrote it).
	if status == domain.SampleStatusCritical && r.faultOpener != nil {
		streak, err := r.samples.CountConsecutive(ctx, p.ID, domain.SampleStatusCritical, 3)
		if err != nil {
			r.log.Warn("probe_tick: count consecutive failed", "probe_id", p.ID, "err", err)
			return
		}
		if streak >= 2 {
			// Use the probe id as source_id so the dashboard can pivot
			// from a fault row to the probe samples view.
			pid := p.ID
			if _, err := r.faultOpener.OpenFault(ctx, port.OpenFaultInput{
				Kind:       domain.FaultKindProbeCritical,
				Severity:   domain.FaultSeverityHigh,
				SourceID:   &pid,
				SourceKind: "probe",
			}); err != nil {
				r.log.Warn("probe_tick: open fault failed", "probe_id", p.ID, "err", err)
			}
		}
	}
}

// =====================================================================
// Fiber attenuation tick — daily
// =====================================================================

func (r *Runner) runFiberAttenuationTick(ctx context.Context) {
	// First tick at ~5 minutes after boot, then every 24h. Real SNMP
	// polling lands in a follow-up wave; this tick just sweeps stale
	// links so the dashboard count doesn't lie about "links never
	// measured".
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	r.tickFiberAttenuation(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickFiberAttenuation(ctx)
		}
	}
}

func (r *Runner) tickFiberAttenuation(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-fiberOfflineWindow)
	stale, err := r.links.ListStale(ctx, cutoff, 500)
	if err != nil {
		r.log.Warn("fiber_tick: list_stale failed", "err", err)
		return
	}
	flipped := 0
	for _, l := range stale {
		// Skip links that never had a measurement — we'd be flipping
		// "unknown" → "offline" which is misleading.
		if l.LastMeasuredAt == nil {
			continue
		}
		if err := r.links.MarkOffline(ctx, l.ID, time.Now().UTC()); err != nil {
			r.log.Warn("fiber_tick: mark_offline failed", "link_id", l.ID, "err", err)
			continue
		}
		flipped++
	}
	if flipped > 0 {
		r.log.Info("fiber_tick: marked offline", "count", flipped)
	}
}

// =====================================================================
// Fault digest tick — hourly
// =====================================================================

func (r *Runner) runFaultDigestTick(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(7 * time.Minute):
	}
	r.tickFaultDigest(ctx)
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickFaultDigest(ctx)
		}
	}
}

// tickFaultDigest writes one audit row per open-unacked fault. The
// rows surface in the ops digest the next day so on-call has a
// review queue; we don't auto-escalate here (escalation is the
// War Room module's job in a future wave).
func (r *Runner) tickFaultDigest(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-unackedFaultThreshold)
	stale, err := r.faults.ListOpenUnacked(ctx, cutoff, 200)
	if err != nil {
		r.log.Warn("fault_digest: list_unacked failed", "err", err)
		return
	}
	for _, f := range stale {
		audit.SafeWrite(ctx, r.audit, audit.Entry{
			Module:     "nocmon",
			RecordType: "nocmon.fault.unacked",
			RecordID:   f.ID.String(),
			Reason:     "fault.unacked age=" + time.Since(f.StartedAt).Truncate(time.Minute).String(),
		})
	}
	if len(stale) > 0 {
		r.log.Info("fault_digest: flagged unacked", "count", len(stale))
	}
}
