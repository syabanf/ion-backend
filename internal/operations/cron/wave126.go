// Wave 126 — additional cron workers:
//
//   - MaintenanceLeadTimeTick — every 30 minutes; finds events within
//     their lead_time_notify_hours window with pending affected-customer
//     rows and dispatches notifications.
//   - MaintenanceOverrunDetectTick — every 15 minutes; sweeps
//     in-progress events past scheduled_end + tolerance.
//   - AnnouncementDispatcherTick — every minute; finds pending
//     announcements and dispatches them via the AnnouncementService.
//   - CalendarAutoSyncTick — every 5 minutes; pulls from registered
//     calendar sources and upserts into operations.calendar_events.
//   - CrossModuleSLAAggregateTick — hourly; calls AggregateAll.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/operations/usecase"
)

// MaintenanceLeadTimeTick — periodic dispatch loop.
type MaintenanceLeadTimeTick struct {
	svc  *usecase.MaintenanceService
	log  *slog.Logger
	tick time.Duration
}

// NewMaintenanceLeadTimeTick — default 30-minute cadence.
func NewMaintenanceLeadTimeTick(svc *usecase.MaintenanceService, log *slog.Logger) *MaintenanceLeadTimeTick {
	if log == nil {
		log = slog.Default()
	}
	return &MaintenanceLeadTimeTick{
		svc:  svc,
		log:  log.With("worker", "operations_maintenance_lead_time"),
		tick: 30 * time.Minute,
	}
}

// Start launches the goroutine.
func (w *MaintenanceLeadTimeTick) Start(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	go w.run(ctx)
}

func (w *MaintenanceLeadTimeTick) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(45 * time.Second):
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

// RunOnce performs a single sweep. Exported for tests.
func (w *MaintenanceLeadTimeTick) RunOnce(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	n, err := w.svc.NotifyLeadTime(ctx)
	if err != nil {
		w.log.Warn("maintenance lead-time tick failed", "err", err)
		return
	}
	if n > 0 {
		w.log.Info("maintenance lead-time tick", "dispatched", n)
	}
}

// MaintenanceOverrunDetectTick — periodic overrun sweep.
type MaintenanceOverrunDetectTick struct {
	svc  *usecase.MaintenanceService
	log  *slog.Logger
	tick time.Duration
}

// NewMaintenanceOverrunDetectTick — default 15-minute cadence.
func NewMaintenanceOverrunDetectTick(svc *usecase.MaintenanceService, log *slog.Logger) *MaintenanceOverrunDetectTick {
	if log == nil {
		log = slog.Default()
	}
	return &MaintenanceOverrunDetectTick{
		svc:  svc,
		log:  log.With("worker", "operations_maintenance_overrun"),
		tick: 15 * time.Minute,
	}
}

// Start launches the goroutine.
func (w *MaintenanceOverrunDetectTick) Start(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	go w.run(ctx)
}

func (w *MaintenanceOverrunDetectTick) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
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

// RunOnce performs a single overrun sweep.
func (w *MaintenanceOverrunDetectTick) RunOnce(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	if _, err := w.svc.DetectOverrun(ctx); err != nil {
		w.log.Warn("overrun detect tick failed", "err", err)
	}
}

// AnnouncementDispatcherTick — fast pickup loop for pending announcements.
type AnnouncementDispatcherTick struct {
	svc  *usecase.AnnouncementService
	log  *slog.Logger
	tick time.Duration
}

// NewAnnouncementDispatcherTick — default 1-minute cadence.
func NewAnnouncementDispatcherTick(svc *usecase.AnnouncementService, log *slog.Logger) *AnnouncementDispatcherTick {
	if log == nil {
		log = slog.Default()
	}
	return &AnnouncementDispatcherTick{
		svc:  svc,
		log:  log.With("worker", "operations_announcement_dispatcher"),
		tick: time.Minute,
	}
}

// Start launches the goroutine.
func (w *AnnouncementDispatcherTick) Start(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	go w.run(ctx)
}

func (w *AnnouncementDispatcherTick) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
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

// RunOnce dispatches pending announcements.
func (w *AnnouncementDispatcherTick) RunOnce(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	if _, err := w.svc.DispatchPending(ctx); err != nil {
		w.log.Warn("announcement dispatcher tick failed", "err", err)
	}
}

// CalendarAutoSyncTick — auto-sync the calendar from upstream sources.
type CalendarAutoSyncTick struct {
	svc  *usecase.CalendarService
	log  *slog.Logger
	tick time.Duration
}

// NewCalendarAutoSyncTick — default 5-minute cadence.
func NewCalendarAutoSyncTick(svc *usecase.CalendarService, log *slog.Logger) *CalendarAutoSyncTick {
	if log == nil {
		log = slog.Default()
	}
	return &CalendarAutoSyncTick{
		svc:  svc,
		log:  log.With("worker", "operations_calendar_auto_sync"),
		tick: 5 * time.Minute,
	}
}

// Start launches the goroutine.
func (w *CalendarAutoSyncTick) Start(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	go w.run(ctx)
}

func (w *CalendarAutoSyncTick) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
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

// RunOnce performs a single auto-sync.
func (w *CalendarAutoSyncTick) RunOnce(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	if _, err := w.svc.AutoSync(ctx); err != nil {
		w.log.Warn("calendar auto-sync tick failed", "err", err)
	}
}

// CrossModuleSLAAggregateTick — hourly aggregation across modules.
type CrossModuleSLAAggregateTick struct {
	svc  *usecase.CrossModuleSLAService
	log  *slog.Logger
	tick time.Duration
}

// NewCrossModuleSLAAggregateTick — default 1-hour cadence.
func NewCrossModuleSLAAggregateTick(svc *usecase.CrossModuleSLAService, log *slog.Logger) *CrossModuleSLAAggregateTick {
	if log == nil {
		log = slog.Default()
	}
	return &CrossModuleSLAAggregateTick{
		svc:  svc,
		log:  log.With("worker", "operations_cross_module_sla"),
		tick: time.Hour,
	}
}

// Start launches the goroutine.
func (w *CrossModuleSLAAggregateTick) Start(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	go w.run(ctx)
}

func (w *CrossModuleSLAAggregateTick) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
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

// RunOnce aggregates all modules once.
func (w *CrossModuleSLAAggregateTick) RunOnce(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	if _, err := w.svc.AggregateAll(ctx); err != nil {
		w.log.Warn("cross-module sla tick failed", "err", err)
	}
}
