// M5 r3 — SLA breach watcher.
//
// A small in-process loop that wakes on a tick, queries the SLA-breach
// view, and (round-3) just emits a structured log line per breached
// WO. Round-4 will add escalations: notify the Team Leader, auto-pair
// to the next available technician, page on-call.
//
// We keep this in the usecase package so it shares the service's
// repos and respects the same domain rules — no separate adapter.
package usecase

import (
	"context"
	"log/slog"
	"time"
)

// StartSLAWatcher runs an indefinite tick loop until ctx is cancelled.
// `interval` controls how often it scans. Returns immediately; the
// caller (cmd/field-svc/main.go) launches it in a goroutine.
//
// We log breaches at slog.Warn so they show up in production logs
// without spamming Info. Each tick logs a single summary line + per-WO
// entries when there are breaches.
func (s *Service) StartSLAWatcher(ctx context.Context, interval time.Duration, log *slog.Logger) {
	if s.reschedules == nil {
		log.Warn("sla watcher: reschedule repo not wired — watcher disabled")
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	log = log.With("component", "sla_watcher", "interval", interval.String())
	log.Info("sla watcher: starting")

	t := time.NewTicker(interval)
	defer t.Stop()

	// Tick once on start so cold-start ops sees current state without
	// waiting a full interval.
	s.runSLAScan(ctx, log)

	for {
		select {
		case <-ctx.Done():
			log.Info("sla watcher: stopped")
			return
		case <-t.C:
			s.runSLAScan(ctx, log)
		}
	}
}

func (s *Service) runSLAScan(ctx context.Context, log *slog.Logger) {
	// We cap the page so a runaway backlog doesn't flood logs. Round-4
	// will paginate + persist a per-watcher cursor.
	items, total, err := s.reschedules.ListSLABreaches(ctx, 100, 0)
	if err != nil {
		log.Error("sla watcher: scan failed", "err", err)
		return
	}
	if total == 0 {
		log.Debug("sla watcher: no breaches")
		return
	}
	log.Warn("sla watcher: breaches detected",
		"total", total, "shown", len(items))
	now := time.Now().UTC()
	for _, d := range items {
		dueOverdue := "unknown"
		if d.WO.SLADueAt != nil {
			dueOverdue = now.Sub(*d.WO.SLADueAt).Truncate(time.Minute).String()
		}
		log.Warn("sla breach",
			"wo", d.WO.WONumber,
			"status", string(d.WO.Status),
			"branch", d.BranchCode,
			"team", d.TeamName,
			"overdue", dueOverdue,
		)
	}
}
