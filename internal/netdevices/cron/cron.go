// Package cron holds the background tickers for the netdevices
// bounded context.
//
// Three tickers:
//   - FirmwareComplianceScanDaily: daily fleet scan against recommended
//     firmware (calls ComplianceService.RunScan).
//   - RMAExpiryScanWeekly: weekly walk over RMAs untouched for 90+ days
//     (calls RMAService.ExpireOld).
//   - StaleHealthSnapshotScan: hourly walk over active devices whose
//     last_seen_at is > 24h. Wave 113 logs only; Wave 113.5 will fire
//     a nocmon fault_event via a bridge port.
//
// Same in-process tick model as warehouse-svc + partnership-svc.
// Idempotent enough that a missed run rolls forward to the next tick.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/netdevices/usecase"
)

// FirmwareComplianceScanDaily wraps ComplianceService for the cron loop.
type FirmwareComplianceScanDaily struct {
	svc *usecase.ComplianceService
	log *slog.Logger
}

func NewFirmwareComplianceScanDaily(svc *usecase.ComplianceService, log *slog.Logger) *FirmwareComplianceScanDaily {
	return &FirmwareComplianceScanDaily{svc: svc, log: log}
}

// RunOnce runs a single all-fleet scan. Errors are logged but don't
// stop the ticker — the next tick retries.
func (c *FirmwareComplianceScanDaily) RunOnce(ctx context.Context) {
	run, err := c.svc.RunScan(ctx, "all")
	if err != nil {
		if c.log != nil {
			c.log.Error("netdev firmware compliance scan failed", "err", err)
		}
		return
	}
	if c.log != nil && run != nil {
		c.log.Info("netdev firmware compliance scan",
			"total", run.TotalDevices,
			"compliant", run.Compliant,
			"non_compliant", run.NonCompliant,
			"critical_pending", run.CriticalPending)
	}
}

// Start kicks off the daily loop. First tick is immediate so a freshly
// booted service has a non-empty compliance report before the 24h mark.
func (c *FirmwareComplianceScanDaily) Start(ctx context.Context) {
	const interval = 24 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	go func() {
		c.RunOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.RunOnce(ctx)
			}
		}
	}()
}

// RMAExpiryScanWeekly flips RMAs untouched for 90+ days to expired.
type RMAExpiryScanWeekly struct {
	svc *usecase.RMAService
	log *slog.Logger
}

func NewRMAExpiryScanWeekly(svc *usecase.RMAService, log *slog.Logger) *RMAExpiryScanWeekly {
	return &RMAExpiryScanWeekly{svc: svc, log: log}
}

func (c *RMAExpiryScanWeekly) RunOnce(ctx context.Context) {
	expired, err := c.svc.ExpireOld(ctx, time.Now().UTC())
	if err != nil {
		if c.log != nil {
			c.log.Error("netdev RMA expiry scan failed", "err", err)
		}
		return
	}
	if c.log != nil && expired > 0 {
		c.log.Info("netdev RMA expiry scan", "expired", expired)
	}
}

// Start kicks off the weekly loop. First tick is immediate.
func (c *RMAExpiryScanWeekly) Start(ctx context.Context) {
	const interval = 7 * 24 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	go func() {
		c.RunOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.RunOnce(ctx)
			}
		}
	}()
}

// StaleHealthSnapshotScan walks active devices whose last_seen_at is
// older than the threshold and logs them. Wave 113 logs only; Wave
// 113.5 will fire a nocmon fault_event via a bridge port. The cron
// keeps a thin SQL surface here because the scan is read-only — no
// usecase to wire.
type StaleHealthSnapshotScan struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	Threshold time.Duration // default 24h
}

func NewStaleHealthSnapshotScan(pool *pgxpool.Pool, log *slog.Logger) *StaleHealthSnapshotScan {
	return &StaleHealthSnapshotScan{pool: pool, log: log, Threshold: 24 * time.Hour}
}

// RunOnce counts stale active devices and logs the summary. Designed to
// be safe-to-skip when the pool isn't wired (test mode).
func (c *StaleHealthSnapshotScan) RunOnce(ctx context.Context) {
	if c.pool == nil {
		return
	}
	cutoff := time.Now().UTC().Add(-c.Threshold)
	var stale int
	err := c.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM netdev.devices
		WHERE status IN ('active','degraded')
		  AND (last_seen_at IS NULL OR last_seen_at < $1)
	`, cutoff).Scan(&stale)
	if err != nil {
		if c.log != nil {
			c.log.Error("netdev stale health scan failed", "err", err)
		}
		return
	}
	if c.log != nil {
		c.log.Info("netdev stale health scan", "stale_devices", stale, "threshold_hours", int(c.Threshold/time.Hour))
	}
}

// Start kicks off an hourly tick. First tick is immediate.
func (c *StaleHealthSnapshotScan) Start(ctx context.Context) {
	const interval = 1 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	go func() {
		c.RunOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.RunOnce(ctx)
			}
		}
	}()
}
