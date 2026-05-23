// Wave 118 — SyncService wraps the HRIS gateway end-to-end.

package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/hris/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SyncService is the orchestrator for "go fetch the latest from HRIS".
// Composes EmployeeService.Upsert + EventService.IngestEvents inside one
// outer flow so a missing gateway is a clean no-op (the cron still runs,
// just emits zero rows + a warnTODO).
type SyncService struct {
	gateway   port.HRISGateway
	employee  *EmployeeService
	event     *EventService
	auditWriter audit.Writer
	log       *slog.Logger

	// lastSyncAt is the wall clock of the last successful FetchEmployees
	// call. The next sync passes this through as the `since` cursor so
	// the gateway can return a delta. Initialized to time.Time{} which
	// the stub interprets as "return everything".
	lastSyncAt time.Time
}

// SyncServiceOpts is the option bag for NewSyncService. gateway may be nil —
// the cron still runs but every tick reports zero rows + warnTODO.
type SyncServiceOpts struct {
	Gateway     port.HRISGateway
	Employee    *EmployeeService
	Event       *EventService
	AuditWriter audit.Writer
	Log         *slog.Logger
}

// NewSyncService builds a SyncService.
func NewSyncService(opts SyncServiceOpts) *SyncService {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	auditW := opts.AuditWriter
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &SyncService{
		gateway:     opts.Gateway,
		employee:    opts.Employee,
		event:       opts.Event,
		auditWriter: auditW,
		log:         log.With("component", "hris.sync"),
	}
}

// SyncResult is the per-tick summary returned to the HTTP layer + cron.
type SyncResult struct {
	EmployeesUpserted int
	EventsIngested    int
	EventsProcessed   int
	StartedAt         time.Time
	FinishedAt        time.Time
	Err               string
}

// RunFullSync pulls the gateway's latest delta, upserts each employee,
// ingests each event, then drains the queue. Idempotent on (employee_no)
// and (event.id) — re-running the sync produces zero new rows on the
// second pass.
func (s *SyncService) RunFullSync(ctx context.Context) (*SyncResult, error) {
	if s == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "hris.sync", "sync service not configured", nil)
	}
	res := &SyncResult{StartedAt: time.Now().UTC()}

	if s.gateway == nil {
		s.log.Warn("TODO Wave 118 — HRISGateway not wired; sync no-ops")
		res.FinishedAt = time.Now().UTC()
		return res, nil
	}

	// 1. Employees.
	employees, err := s.gateway.FetchEmployees(ctx, s.lastSyncAt)
	if err != nil {
		res.Err = err.Error()
		res.FinishedAt = time.Now().UTC()
		return res, derrors.Wrap(derrors.KindUnavailable, "hris.sync.fetch_employees", "fetch employees from gateway", err)
	}
	for _, rec := range employees {
		if s.employee != nil {
			if _, uerr := s.employee.Upsert(ctx, rec); uerr != nil {
				s.log.Warn("upsert employee failed during sync",
					"employee_no", rec.EmployeeNo, "err", uerr)
				continue
			}
			res.EmployeesUpserted++
		}
	}

	// 2. Events.
	events, err := s.gateway.FetchEvents(ctx, s.lastSyncAt)
	if err != nil {
		res.Err = err.Error()
		res.FinishedAt = time.Now().UTC()
		return res, derrors.Wrap(derrors.KindUnavailable, "hris.sync.fetch_events", "fetch events from gateway", err)
	}
	if s.event != nil && len(events) > 0 {
		n, ierr := s.event.IngestEvents(ctx, events)
		if ierr != nil {
			s.log.Warn("ingest events failed during sync", "err", ierr)
		} else {
			res.EventsIngested = n
		}
	}

	// 3. Drain.
	if s.event != nil {
		drained, derr := s.event.ProcessPending(ctx, 100)
		if derr != nil {
			s.log.Warn("drain events failed during sync", "err", derr)
		} else {
			res.EventsProcessed = drained
		}
	}

	s.lastSyncAt = res.StartedAt
	res.FinishedAt = time.Now().UTC()

	audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
		Module:     "hris",
		RecordType: "hris.sync_run",
		RecordID:   res.StartedAt.Format(time.RFC3339Nano),
		After:      "ok",
		Reason:     "wave118.sync.full",
	})
	s.log.Info("hris sync complete",
		"employees_upserted", res.EmployeesUpserted,
		"events_ingested", res.EventsIngested,
		"events_processed", res.EventsProcessed,
		"duration_ms", res.FinishedAt.Sub(res.StartedAt).Milliseconds())
	return res, nil
}
