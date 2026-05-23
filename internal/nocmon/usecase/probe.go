// Package usecase wires the NOC monitoring bounded context together.
//
// Each service depends only on its port interfaces, never on postgres
// directly — keeping the bounded context extractable into a
// standalone microservice without touching domain rules.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/audit"
)

// ProbeService implements port.ProbeUseCase. RecordSample is the
// load-bearing method — it auto-classifies status via the domain
// Evaluate method, appends to the partitioned samples table, and
// denormalizes last_status onto the probe header.
type ProbeService struct {
	probes  port.ServiceProbeRepository
	samples port.HealthSampleRepository
	audit   audit.Writer
}

func NewProbeService(probes port.ServiceProbeRepository, samples port.HealthSampleRepository, w audit.Writer) *ProbeService {
	if w == nil {
		w = audit.Nop{}
	}
	return &ProbeService{probes: probes, samples: samples, audit: w}
}

var _ port.ProbeUseCase = (*ProbeService)(nil)

func (s *ProbeService) CreateProbe(ctx context.Context, in port.CreateProbeInput) (*domain.ServiceProbe, error) {
	p, err := domain.NewServiceProbe(in.CustomerID, in.Kind, in.Target, in.IntervalSeconds, in.ThresholdWarn, in.ThresholdCritical)
	if err != nil {
		return nil, err
	}
	p.PlanID = in.PlanID
	if err := s.probes.Create(ctx, p); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "nocmon",
		RecordType: "nocmon.probe",
		RecordID:   p.ID.String(),
		After:      string(p.ProbeKind),
		Reason:     "probe.created customer_id=" + p.CustomerID.String(),
	})
	return p, nil
}

func (s *ProbeService) GetProbe(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error) {
	return s.probes.FindByID(ctx, id)
}

func (s *ProbeService) ListProbes(ctx context.Context, f port.ProbeListFilter) ([]domain.ServiceProbe, int, error) {
	return s.probes.List(ctx, f)
}

func (s *ProbeService) ListUnhealthy(ctx context.Context, limit int) ([]domain.ServiceProbe, error) {
	return s.probes.ListUnhealthy(ctx, limit)
}

// RecordSample is the single ingestion path for probe samples.
// Workflow:
//  1. Load the probe so we can call Evaluate on the freshly-measured
//     value (the domain owns the warn/critical thresholds).
//  2. Insert the sample. Idempotent on (probe_id, sampled_at).
//  3. Stamp last_probed_at + last_status on the probe header for
//     cheap dashboard reads.
//
// We deliberately do NOT auto-open a fault here — the cron runner is
// the single place that decides "this transition warrants a fault"
// (with anti-flap). Keeping this method side-effect-free for the
// fault graph means manual /sample calls (used by tests + by NOC
// operators replaying a measurement) don't accidentally page anyone.
func (s *ProbeService) RecordSample(ctx context.Context, probeID uuid.UUID, value float64, at time.Time) (*domain.HealthSample, error) {
	p, err := s.probes.FindByID(ctx, probeID)
	if err != nil {
		return nil, err
	}
	status := p.Evaluate(value)
	atUTC := at.UTC()
	v := value
	sample := &domain.HealthSample{
		ID:        uuid.New(),
		ProbeID:   p.ID,
		SampledAt: atUTC,
		Value:     &v,
		Status:    status,
	}
	if err := s.samples.Insert(ctx, sample); err != nil {
		return nil, err
	}
	if err := s.probes.UpdateLastSample(ctx, p.ID, status, atUTC); err != nil {
		return nil, err
	}
	return sample, nil
}

func (s *ProbeService) ListSamples(ctx context.Context, f port.SampleListFilter) ([]domain.HealthSample, error) {
	return s.samples.ListForProbe(ctx, f)
}

func (s *ProbeService) DeactivateProbe(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error) {
	p, err := s.probes.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Deactivate(time.Now().UTC())
	if err := s.probes.UpdateActive(ctx, p); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "nocmon",
		RecordType: "nocmon.probe",
		RecordID:   p.ID.String(),
		After:      "deactivated",
		Reason:     "probe.deactivated",
	})
	return p, nil
}
