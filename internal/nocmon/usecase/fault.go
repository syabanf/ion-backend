package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/audit"
)

// slaWindowHours is the default SLA window used by LinkImpact to
// decide credit eligibility. Broadband product spec = 4h. Pinned
// here rather than threaded through the API so the per-customer SLA
// policy lives in one place; a future per-customer override would
// land as a new field on the LinkImpact input.
const slaWindowHours = 4.0

// FaultService implements port.FaultUseCase. Every state-machine
// transition is driven through the domain methods on FaultEvent, then
// a single UpdateStatus call persists. Every transition emits an
// audit row so the dashboard timeline + the postmortem export stay
// consistent.
type FaultService struct {
	faults  port.FaultEventRepository
	impacts port.FaultImpactRepository
	audit   audit.Writer
}

func NewFaultService(faults port.FaultEventRepository, impacts port.FaultImpactRepository, w audit.Writer) *FaultService {
	if w == nil {
		w = audit.Nop{}
	}
	return &FaultService{faults: faults, impacts: impacts, audit: w}
}

var _ port.FaultUseCase = (*FaultService)(nil)

func (s *FaultService) OpenFault(ctx context.Context, in port.OpenFaultInput) (*domain.FaultEvent, error) {
	f, err := domain.NewFaultEvent(in.Kind, in.Severity, in.SourceID, in.SourceKind)
	if err != nil {
		return nil, err
	}
	if err := s.faults.Create(ctx, f); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "nocmon",
		RecordType:   "nocmon.fault",
		RecordID:     f.ID.String(),
		FieldChanged: "status",
		After:        string(f.Status),
		Reason:       "fault.opened kind=" + string(f.Kind) + " severity=" + string(f.Severity),
	})
	return f, nil
}

func (s *FaultService) GetFault(ctx context.Context, id uuid.UUID) (*domain.FaultEvent, error) {
	return s.faults.FindByID(ctx, id)
}

func (s *FaultService) ListFaults(ctx context.Context, f port.FaultListFilter) ([]domain.FaultEvent, int, error) {
	return s.faults.List(ctx, f)
}

func (s *FaultService) AcknowledgeFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error) {
	return s.transition(ctx, id, "acknowledge", func(f *domain.FaultEvent, now time.Time) error {
		return f.Acknowledge(by, now)
	})
}

func (s *FaultService) InvestigateFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error) {
	return s.transition(ctx, id, "investigate", func(f *domain.FaultEvent, now time.Time) error {
		return f.Investigate(by, now)
	})
}

func (s *FaultService) MitigateFault(ctx context.Context, id, by uuid.UUID, rootCause string) (*domain.FaultEvent, error) {
	return s.transition(ctx, id, "mitigate", func(f *domain.FaultEvent, now time.Time) error {
		return f.Mitigate(by, now, rootCause)
	})
}

func (s *FaultService) ResolveFault(ctx context.Context, id, by uuid.UUID) (*domain.FaultEvent, error) {
	return s.transition(ctx, id, "resolve", func(f *domain.FaultEvent, now time.Time) error {
		return f.Resolve(by, now)
	})
}

// LinkImpact upserts one (fault, customer) row + bumps the
// denormalized customer_impact_count on the fault header. The count
// stays consistent on idempotent re-runs because Upsert returns
// without spawning a new row for an existing (fault, customer) pair —
// we recount via CountForFault to keep the header trustworthy.
func (s *FaultService) LinkImpact(ctx context.Context, in port.LinkImpactInput) (*domain.FaultImpact, error) {
	f, err := s.faults.FindByID(ctx, in.FaultEventID)
	if err != nil {
		return nil, err
	}

	slaEligible := in.SLACreditEligible
	// If the caller didn't pre-compute eligibility AND the impact
	// already has an end, derive it from the duration so the typical
	// "close out an outage" flow doesn't have to know the policy.
	if !slaEligible && !in.ImpactStart.IsZero() {
		durHours := time.Since(in.ImpactStart).Hours()
		slaEligible = domain.ComputeSLACreditEligible(durHours, slaWindowHours)
	}

	impact, err := domain.NewFaultImpact(in.FaultEventID, in.CustomerID, in.ImpactKind, in.ImpactStart, slaEligible)
	if err != nil {
		return nil, err
	}
	if err := s.impacts.Upsert(ctx, impact); err != nil {
		return nil, err
	}

	// Re-count and persist on the fault header for cheap list reads.
	count, err := s.impacts.CountForFault(ctx, in.FaultEventID)
	if err == nil {
		f.CustomerImpactCount = count
		f.UpdatedAt = time.Now().UTC()
		_ = s.faults.UpdateStatus(ctx, f) // best-effort denorm
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "nocmon",
		RecordType: "nocmon.fault_impact",
		RecordID:   in.FaultEventID.String() + ":" + in.CustomerID.String(),
		After:      string(in.ImpactKind),
		Reason:     "impact.linked sla_eligible=" + boolStr(slaEligible),
	})
	return impact, nil
}

func (s *FaultService) ListImpact(ctx context.Context, faultID uuid.UUID) ([]domain.FaultImpact, error) {
	return s.impacts.ListForFault(ctx, faultID)
}

// transition is the shared driver for every state-machine method.
// It loads, calls the domain transition (which validates the move),
// persists, and emits one audit row per move. Failures from the
// domain bubble up unchanged so the HTTP layer surfaces 409 vs 404
// vs 422 correctly.
func (s *FaultService) transition(ctx context.Context, id uuid.UUID, op string, apply func(*domain.FaultEvent, time.Time) error) (*domain.FaultEvent, error) {
	f, err := s.faults.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	prev := f.Status
	now := time.Now().UTC()
	if err := apply(f, now); err != nil {
		return nil, err
	}
	if err := s.faults.UpdateStatus(ctx, f); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "nocmon",
		RecordType:   "nocmon.fault",
		RecordID:     f.ID.String(),
		FieldChanged: "status",
		Before:       string(prev),
		After:        string(f.Status),
		Reason:       "fault." + op,
	})
	return f, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
