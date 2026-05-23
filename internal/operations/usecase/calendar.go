// Wave 126 — CalendarService: unified calendar view + auto-sync from
// upstream sources.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CalendarService owns the operational calendar surface.
type CalendarService struct {
	repo    port.CalendarEventRepository
	sources []port.CalendarSyncSource
	log     *slog.Logger
}

// CalendarDeps groups the calendar dependencies.
type CalendarDeps struct {
	Repo    port.CalendarEventRepository
	Sources []port.CalendarSyncSource
	Log     *slog.Logger
}

// NewCalendarService builds the service.
func NewCalendarService(deps CalendarDeps) *CalendarService {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &CalendarService{
		repo:    deps.Repo,
		sources: deps.Sources,
		log:     log.With("service", "operations.calendar"),
	}
}

// CreateEventInput is the create-payload shape (manual entries).
type CreateEventInput struct {
	EventKind   domain.EventKind
	Title       string
	Description string
	Scope       domain.EventScope
	ScopeID     *uuid.UUID
	AllDay      bool
	StartsAt    time.Time
	EndsAt      *time.Time
	ColorHex    string
	Metadata    map[string]any
	CreatedBy   uuid.UUID
}

// CreateEvent persists a manual entry.
func (s *CalendarService) CreateEvent(ctx context.Context, in CreateEventInput) (*domain.CalendarEvent, error) {
	if s == nil || s.repo == nil {
		return nil, derrors.Internal("operations.calendar.no_repo", "calendar repo not wired")
	}
	if in.Title == "" {
		return nil, derrors.Validation("operations.calendar.title_required", "title is required")
	}
	now := time.Now().UTC()
	e := &domain.CalendarEvent{
		ID:          uuid.New(),
		EventKind:   domain.NormalizeEventKind(string(in.EventKind)),
		EventSource: domain.SourceCustom,
		Title:       in.Title,
		Description: in.Description,
		Scope:       domain.NormalizeScope(string(in.Scope)),
		ScopeID:     in.ScopeID,
		AllDay:      in.AllDay,
		StartsAt:    in.StartsAt,
		EndsAt:      in.EndsAt,
		ColorHex:    in.ColorHex,
		Metadata:    in.Metadata,
		CreatedBy:   &in.CreatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Create(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// ListInRange returns calendar events overlapping [from, to] scoped to
// the requested audience (or 'global' if nil).
func (s *CalendarService) ListInRange(ctx context.Context, from, to time.Time, scope domain.EventScope, scopeID *uuid.UUID, limit int) ([]domain.CalendarEvent, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	if scope == "" {
		scope = domain.ScopeGlobal
	}
	return s.repo.ListInRange(ctx, from, to, scope, scopeID, limit)
}

// ListUpcoming returns the next N events ordered by start time.
func (s *CalendarService) ListUpcoming(ctx context.Context, daysAhead int) ([]domain.CalendarEvent, error) {
	from := time.Now().UTC()
	if daysAhead <= 0 {
		daysAhead = 30
	}
	to := from.Add(time.Duration(daysAhead) * 24 * time.Hour)
	return s.ListInRange(ctx, from, to, domain.ScopeGlobal, nil, 500)
}

// AutoSync pulls rows from each registered source and upserts them
// into operations.calendar_events. Idempotent via the
// (event_source, source_id) unique index. Returns the count synced.
func (s *CalendarService) AutoSync(ctx context.Context) (int, error) {
	if s == nil || s.repo == nil || len(s.sources) == 0 {
		return 0, nil
	}
	from := time.Now().UTC().Add(-7 * 24 * time.Hour)
	to := time.Now().UTC().Add(60 * 24 * time.Hour)
	synced := 0
	for _, src := range s.sources {
		events, err := src.List(ctx, from, to, 500)
		if err != nil {
			s.log.Warn("calendar source pull failed", "source", src.Source(), "err", err)
			continue
		}
		for i := range events {
			e := events[i]
			if e.ID == uuid.Nil {
				e.ID = uuid.New()
			}
			e.EventSource = src.Source()
			if err := s.repo.UpsertBySource(ctx, &e); err != nil {
				s.log.Warn("calendar upsert failed", "source", src.Source(), "err", err)
				continue
			}
			synced++
		}
	}
	if synced > 0 {
		s.log.Info("calendar auto-sync tick", "synced", synced)
	}
	return synced, nil
}
