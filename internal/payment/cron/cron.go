// Package cron ships the payment-svc periodic workers.
//
// Wave 111 ships two workers:
//
//   - ExpireStaleIntentsWorker — every 15 minutes, flips intents that
//     have been stuck in 'pending' for longer than the configured
//     stale window (24h by default) to 'expired'. Idempotent: the
//     state machine refuses to expire anything outside 'pending', so
//     a re-run is safe.
//
//   - MatchPendingH2HStatementsWorker — once a day, picks up
//     parsed/partial H2H statements and re-runs MatchStatement so
//     newly-landed intents can bind against still-unmatched lines.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// ExpireStaleIntentsWorker scans pending intents older than the cutoff.
type ExpireStaleIntentsWorker struct {
	intents port.IntentUseCase
	log     *slog.Logger
	stale   time.Duration
	tick    time.Duration
}

func NewExpireStaleIntentsWorker(intents port.IntentUseCase, stale time.Duration, log *slog.Logger) *ExpireStaleIntentsWorker {
	if stale <= 0 {
		stale = 24 * time.Hour
	}
	return &ExpireStaleIntentsWorker{
		intents: intents,
		log:     log.With("worker", "payment_expire_stale_intents"),
		stale:   stale,
		tick:    15 * time.Minute,
	}
}

func (w *ExpireStaleIntentsWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *ExpireStaleIntentsWorker) run(ctx context.Context) {
	// First tick fires after 1 minute so the binary is healthy before
	// the background work kicks in.
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
	}
	w.RunOnce(ctx)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce is exported so tests + admin tooling can trigger a single
// pass deterministically without waiting for the ticker.
func (w *ExpireStaleIntentsWorker) RunOnce(ctx context.Context) {
	expired, err := w.intents.ExpireStaleIntents(ctx, w.stale)
	if err != nil {
		w.log.Warn("expire stale intents failed", "err", err)
		return
	}
	if expired > 0 {
		w.log.Info("expired stale intents", "count", expired,
			"stale_after", w.stale.String())
	}
}

// MatchPendingH2HStatementsWorker re-runs the matcher on statements
// still in 'parsed' / 'partial' status. Idempotent — already-matched
// lines stay matched; previously-unmatched lines get another chance
// against intents that landed since the last run.
type MatchPendingH2HStatementsWorker struct {
	h2h port.H2HUseCase
	log *slog.Logger
}

func NewMatchPendingH2HStatementsWorker(h2h port.H2HUseCase, log *slog.Logger) *MatchPendingH2HStatementsWorker {
	return &MatchPendingH2HStatementsWorker{
		h2h: h2h,
		log: log.With("worker", "payment_match_pending_h2h_statements"),
	}
}

func (w *MatchPendingH2HStatementsWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *MatchPendingH2HStatementsWorker) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	w.RunOnce(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *MatchPendingH2HStatementsWorker) RunOnce(ctx context.Context) {
	stmts, _, err := w.h2h.ListStatements(ctx, 200, 0)
	if err != nil {
		w.log.Warn("list statements failed", "err", err)
		return
	}
	matched := 0
	for _, s := range stmts {
		if s.Status != domain.H2HStatementStatusParsed && s.Status != domain.H2HStatementStatusPartial {
			continue
		}
		if _, err := w.h2h.MatchStatement(ctx, s.ID); err != nil {
			w.log.Warn("rematch failed", "statement_id", s.ID, "err", err)
			continue
		}
		matched++
	}
	if matched > 0 {
		w.log.Info("re-matched H2H statements", "count", matched)
	}
}
