package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/audit"
)

// FiberService implements port.FiberUseCase. RecordAttenuation is the
// load-bearing method — it routes a measurement through the domain
// classifier, persists both the time-series append AND the
// denormalized header status in one transaction, and emits an audit
// row when the status transitions.
type FiberService struct {
	links port.FiberLinkRepository
	audit audit.Writer
}

func NewFiberService(links port.FiberLinkRepository, w audit.Writer) *FiberService {
	if w == nil {
		w = audit.Nop{}
	}
	return &FiberService{links: links, audit: w}
}

var _ port.FiberUseCase = (*FiberService)(nil)

func (s *FiberService) GetLink(ctx context.Context, id uuid.UUID) (*domain.FiberLink, error) {
	return s.links.FindByID(ctx, id)
}

func (s *FiberService) ListLinks(ctx context.Context, f port.FiberListFilter) ([]domain.FiberLink, int, error) {
	return s.links.List(ctx, f)
}

// RecordAttenuation runs the measurement through EvaluateAttenuation
// and persists the result. Status transitions emit an audit row so
// the NOC postmortem timeline shows when a link first crossed a
// threshold — important for TC-FAM-005 (linking signal trend to
// asset maintenance history).
func (s *FiberService) RecordAttenuation(ctx context.Context, linkID uuid.UUID, valueDB float64, at time.Time, source string) (*domain.FiberLink, error) {
	l, err := s.links.FindByID(ctx, linkID)
	if err != nil {
		return nil, err
	}
	atUTC := at.UTC()
	newStatus := l.EvaluateAttenuation(valueDB, atUTC)
	prevStatus := l.Status

	updated, err := s.links.UpdateMeasurement(ctx, linkID, valueDB, atUTC, newStatus, source)
	if err != nil {
		return nil, err
	}

	if prevStatus != newStatus {
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:       "nocmon",
			RecordType:   "nocmon.fiber",
			RecordID:     linkID.String(),
			FieldChanged: "status",
			Before:       string(prevStatus),
			After:        string(newStatus),
			Reason:       "fiber.attenuation source=" + source,
		})
	}
	return updated, nil
}

func (s *FiberService) ListDegraded(ctx context.Context, limit int) ([]domain.FiberLink, error) {
	return s.links.ListDegraded(ctx, limit)
}
