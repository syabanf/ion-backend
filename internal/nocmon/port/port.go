// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the NOC monitoring bounded context.
//
// Same hexagonal layout as identity / crm / reseller. The HTTP handler
// depends on UseCase interfaces; the UseCase depends on repository
// interfaces; postgres adapters implement the repository interfaces.
// The domain stays oblivious to both transport and storage so the
// bounded context can be extracted into its own service
// (cmd/nocmon-svc) without touching domain rules.
//
// Cross-context ports (TopologyBuilder, WorkOrderCreator) are
// declared here so this package's only outward dependency is the
// domain. Their implementations live in cmd/nocmon-svc/main.go as
// thin adapters — keeping the boundary explicit.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
)

// =====================================================================
// Service-probe inputs
// =====================================================================

type CreateProbeInput struct {
	CustomerID        uuid.UUID
	PlanID            *uuid.UUID
	Kind              domain.ProbeKind
	Target            string
	IntervalSeconds   int
	ThresholdWarn     *float64
	ThresholdCritical *float64
}

type ProbeListFilter struct {
	CustomerID *uuid.UUID
	Kind       domain.ProbeKind
	OnlyActive bool
	Limit      int
	Offset     int
}

type SampleListFilter struct {
	ProbeID uuid.UUID
	From    *time.Time
	To      *time.Time
	Limit   int
}

// =====================================================================
// Fiber inputs
// =====================================================================

type FiberListFilter struct {
	Status     domain.FiberStatus
	CustomerID *uuid.UUID
	Limit      int
	Offset     int
}

// =====================================================================
// Fault inputs
// =====================================================================

type OpenFaultInput struct {
	Kind       domain.FaultKind
	Severity   domain.FaultSeverity
	SourceID   *uuid.UUID
	SourceKind string
}

type FaultListFilter struct {
	Status   domain.FaultStatus
	Severity domain.FaultSeverity
	Kind     domain.FaultKind
	Limit    int
	Offset   int
}

type LinkImpactInput struct {
	FaultEventID      uuid.UUID
	CustomerID        uuid.UUID
	ImpactKind        domain.ImpactKind
	ImpactStart       time.Time
	SLACreditEligible bool
}

// =====================================================================
// UseCase — what the HTTP layer depends on
// =====================================================================

type ProbeUseCase interface {
	CreateProbe(ctx context.Context, in CreateProbeInput) (*domain.ServiceProbe, error)
	GetProbe(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error)
	ListProbes(ctx context.Context, f ProbeListFilter) ([]domain.ServiceProbe, int, error)
	ListUnhealthy(ctx context.Context, limit int) ([]domain.ServiceProbe, error)
	RecordSample(ctx context.Context, probeID uuid.UUID, value float64, at time.Time) (*domain.HealthSample, error)
	ListSamples(ctx context.Context, f SampleListFilter) ([]domain.HealthSample, error)
	DeactivateProbe(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error)
}

type FiberUseCase interface {
	GetLink(ctx context.Context, id uuid.UUID) (*domain.FiberLink, error)
	ListLinks(ctx context.Context, f FiberListFilter) ([]domain.FiberLink, int, error)
	RecordAttenuation(ctx context.Context, linkID uuid.UUID, valueDB float64, at time.Time, source string) (*domain.FiberLink, error)
	ListDegraded(ctx context.Context, limit int) ([]domain.FiberLink, error)
}

type FaultUseCase interface {
	OpenFault(ctx context.Context, in OpenFaultInput) (*domain.FaultEvent, error)
	GetFault(ctx context.Context, id uuid.UUID) (*domain.FaultEvent, error)
	ListFaults(ctx context.Context, f FaultListFilter) ([]domain.FaultEvent, int, error)
	AcknowledgeFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error)
	InvestigateFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error)
	MitigateFault(ctx context.Context, id, by uuid.UUID, rootCause string) (*domain.FaultEvent, error)
	ResolveFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error)
	LinkImpact(ctx context.Context, in LinkImpactInput) (*domain.FaultImpact, error)
	ListImpact(ctx context.Context, faultID uuid.UUID) ([]domain.FaultImpact, error)
}

type TopologyUseCase interface {
	RebuildSnapshot(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error)
	GetLatest(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error)
}

type AlertWOUseCase interface {
	// ConvertFaultToWO calls the WorkOrderCreator port, stamps the
	// returned WO id back onto the fault, and promotes the fault to
	// investigating. Returns the updated fault.
	ConvertFaultToWO(ctx context.Context, faultID, byUserID uuid.UUID) (*domain.FaultEvent, error)
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type ServiceProbeRepository interface {
	Create(ctx context.Context, p *domain.ServiceProbe) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error)
	List(ctx context.Context, f ProbeListFilter) ([]domain.ServiceProbe, int, error)
	// ListDue returns active probes whose last_probed_at + interval_seconds
	// is <= asOf. Used by the cron tick — the limit caps batch size so a
	// large backlog doesn't blow the tick out.
	ListDue(ctx context.Context, asOf time.Time, limit int) ([]domain.ServiceProbe, error)
	// ListUnhealthy returns probes with last_status in ('warn','critical').
	ListUnhealthy(ctx context.Context, limit int) ([]domain.ServiceProbe, error)
	// Update persists the mutable header fields (target, thresholds,
	// interval). Excludes last_probed_at + last_status — those live
	// on the dedicated UpdateLastSample call.
	Update(ctx context.Context, p *domain.ServiceProbe) error
	UpdateLastSample(ctx context.Context, id uuid.UUID, status domain.SampleStatus, at time.Time) error
	UpdateActive(ctx context.Context, p *domain.ServiceProbe) error
}

type HealthSampleRepository interface {
	// Insert is idempotent via UNIQUE (probe_id, sampled_at) — a
	// repeat call with the same key returns nil (no-op) rather than
	// a conflict error.
	Insert(ctx context.Context, s *domain.HealthSample) error
	// ListForProbe returns recent samples newest-first; the filter's
	// From/To bracket the window (both optional).
	ListForProbe(ctx context.Context, f SampleListFilter) ([]domain.HealthSample, error)
	// CountConsecutive returns how many of the latest N samples on a
	// probe match the given status. Used by the cron anti-flap rule
	// (2+ consecutive criticals open a fault, not just one).
	CountConsecutive(ctx context.Context, probeID uuid.UUID, status domain.SampleStatus, lookback int) (int, error)
}

type FiberLinkRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FiberLink, error)
	List(ctx context.Context, f FiberListFilter) ([]domain.FiberLink, int, error)
	// ListStale returns links with last_measured_at older than the
	// given window (or never measured). Used by FiberAttenuationTick
	// to flip dead links to offline.
	ListStale(ctx context.Context, olderThan time.Time, limit int) ([]domain.FiberLink, error)
	// UpdateMeasurement persists last_measured_db + last_measured_at +
	// status atomically with an append on fiber_attenuation_history.
	UpdateMeasurement(ctx context.Context, linkID uuid.UUID, valueDB float64, at time.Time, status domain.FiberStatus, source string) (*domain.FiberLink, error)
	// MarkOffline flips a link's status to offline + updates_at. Used
	// by the daily tick for stale links.
	MarkOffline(ctx context.Context, linkID uuid.UUID, at time.Time) error
	ListDegraded(ctx context.Context, limit int) ([]domain.FiberLink, error)
}

type FaultEventRepository interface {
	Create(ctx context.Context, f *domain.FaultEvent) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FaultEvent, error)
	List(ctx context.Context, f FaultListFilter) ([]domain.FaultEvent, int, error)
	// UpdateStatus persists status + per-status timestamps + root_cause
	// + ticket_wo_id + customer_impact_count. Used by every state-
	// machine transition so the repo doesn't grow one UPDATE per state.
	UpdateStatus(ctx context.Context, f *domain.FaultEvent) error
	// ListOpenUnacked returns open faults that have not been
	// acknowledged within the supplied window. Used by FaultDigestTick.
	ListOpenUnacked(ctx context.Context, olderThan time.Time, limit int) ([]domain.FaultEvent, error)
}

type FaultImpactRepository interface {
	// Upsert is idempotent on (fault_event_id, customer_id).
	Upsert(ctx context.Context, i *domain.FaultImpact) error
	ListForFault(ctx context.Context, faultID uuid.UUID) ([]domain.FaultImpact, error)
	CountForFault(ctx context.Context, faultID uuid.UUID) (int, error)
}

type TopologySnapshotRepository interface {
	Create(ctx context.Context, s *domain.TopologySnapshot) error
	FindLatest(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error)
}

// =====================================================================
// Cross-context ports — wired in cmd/nocmon-svc/main.go as adapters
// =====================================================================

// ProbeRunner executes a real (or stubbed) probe and returns the
// measured value + classified status. One runner per ProbeKind; the
// dispatcher in the cron picks the right runner by kind. Real-mode
// runners are gated by NOC_PROBES_ENABLED=true; default deployment
// uses the stub runners shipped in adapter/probes.
type ProbeRunner interface {
	Kind() domain.ProbeKind
	Run(ctx context.Context, probe *domain.ServiceProbe) (value float64, status domain.SampleStatus, err error)
}

// TopologyBuilder is the cross-context bridge into the network
// bounded context. The default in-process adapter (wired in
// cmd/nocmon-svc/main.go) reads from internal/network/* via a thin
// reader; KEEPING THE INTERFACE LOCAL means the future split into a
// network-svc remote call only changes one file.
type TopologyBuilder interface {
	BuildSnapshot(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error)
}

// WorkOrderCreator is the cross-context bridge into the field WO
// bounded context. AlertWOService.ConvertFaultToWO calls
// CreateOutageWO with the fault id + the impacted customer set; the
// returned WO id is stamped onto fault_events.ticket_wo_id.
//
// The shipped adapter in cmd/nocmon-svc/main.go is a stub (logs +
// returns a synthetic uuid) — the real implementation lands in a
// follow-up wave once the field WO context exposes a service-to-
// service POST endpoint.
type WorkOrderCreator interface {
	CreateOutageWO(ctx context.Context, faultEventID uuid.UUID, impactedCustomerIDs []uuid.UUID) (woID uuid.UUID, err error)
}
