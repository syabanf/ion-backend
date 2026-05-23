// Wave 126 — AnnouncementService: dispatcher state machine + recipient
// resolution + notifyx fan-out.
//
// The Wave 65 handler creates rows with `sent_at = NULL` and never sends
// them. This service is what the new dispatcher cron drives.
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

// AnnouncementService orchestrates create + dispatch + read-receipt.
type AnnouncementService struct {
	repo       port.AnnouncementRepository
	recipients port.AnnouncementRecipientRepository
	audience   port.AudienceResolver
	dispatcher port.AnnouncementDispatcher
	maxRecipients int
	log        *slog.Logger
}

// AnnouncementDeps groups the dependencies. Optional ports may be nil
// (the service degrades to no-ops on those code paths).
type AnnouncementDeps struct {
	Repo          port.AnnouncementRepository
	Recipients    port.AnnouncementRecipientRepository
	Audience      port.AudienceResolver
	Dispatcher    port.AnnouncementDispatcher
	MaxRecipients int
	Log           *slog.Logger
}

// NewAnnouncementService builds the service.
func NewAnnouncementService(deps AnnouncementDeps) *AnnouncementService {
	max := deps.MaxRecipients
	if max <= 0 {
		max = 1000
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &AnnouncementService{
		repo:          deps.Repo,
		recipients:    deps.Recipients,
		audience:      deps.Audience,
		dispatcher:    deps.Dispatcher,
		maxRecipients: max,
		log:           log.With("service", "operations.announcement"),
	}
}

// CreateAnnouncementInput is the create-payload shape.
type CreateAnnouncementInput struct {
	Title          string
	Body           string
	Severity       domain.AnnouncementSeverity
	TargetAudience domain.AnnouncementTargetAudience
	Targeting      map[string]any
	Channels       []string
	ScheduledAt    *time.Time
	ExpiresAt      *time.Time
	CreatedBy      uuid.UUID
}

// Create persists a new announcement in 'pending' state.
func (s *AnnouncementService) Create(ctx context.Context, in CreateAnnouncementInput) (*domain.Announcement, error) {
	if s == nil || s.repo == nil {
		return nil, derrors.Internal("operations.announcement.no_repo", "announcement repo not wired")
	}
	if in.Title == "" || in.Body == "" {
		return nil, derrors.Validation("operations.announcement.required", "title and body are required")
	}
	now := time.Now().UTC()
	a := &domain.Announcement{
		ID:             uuid.New(),
		Title:          in.Title,
		Body:           in.Body,
		Severity:       domain.NormalizeSeverity(string(in.Severity)),
		TargetAudience: domain.NormalizeAudience(string(in.TargetAudience)),
		Channels:       in.Channels,
		ScheduledAt:    in.ScheduledAt,
		ExpiresAt:      in.ExpiresAt,
		DispatchStatus: domain.DispatchPending,
		CreatedBy:      &in.CreatedBy,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if len(a.Channels) == 0 {
		// Default channel by severity
		switch a.Severity {
		case domain.AnnouncementUrgent:
			a.Channels = []string{"push", "email", "sms"}
		case domain.AnnouncementImportant:
			a.Channels = []string{"push", "email"}
		default:
			a.Channels = []string{"push"}
		}
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Dispatch is the cron entry point — picks up pending announcements,
// resolves recipients, fans out via the dispatcher port, updates the
// per-row + per-recipient status. Returns the count of announcements
// dispatched in this tick.
func (s *AnnouncementService) DispatchPending(ctx context.Context) (int, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	now := time.Now().UTC()
	pending, err := s.repo.ListPending(ctx, now, 20)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, a := range pending {
		ann := a // capture
		if err := s.dispatchOne(ctx, &ann); err != nil {
			s.log.Warn("dispatch failed", "announcement_id", ann.ID, "err", err)
			continue
		}
		count++
	}
	return count, nil
}

// DispatchOne dispatches a single announcement by ID (handler entry).
func (s *AnnouncementService) DispatchOne(ctx context.Context, announcementID uuid.UUID) (*domain.Announcement, error) {
	if s == nil || s.repo == nil {
		return nil, derrors.Internal("operations.announcement.no_repo", "announcement repo not wired")
	}
	a, err := s.repo.FindByID(ctx, announcementID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, derrors.NotFound("operations.announcement.not_found", "announcement not found")
	}
	if a.DispatchStatus != domain.DispatchPending && a.DispatchStatus != domain.DispatchFailed {
		return nil, derrors.Conflict("operations.announcement.bad_state",
			"announcement is already "+string(a.DispatchStatus))
	}
	if err := s.dispatchOne(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *AnnouncementService) dispatchOne(ctx context.Context, a *domain.Announcement) error {
	if a == nil {
		return nil
	}
	now := time.Now().UTC()
	a.MarkDispatching(now)
	if err := s.repo.Update(ctx, a); err != nil {
		return err
	}
	// Resolve recipients.
	var userIDs []uuid.UUID
	if s.audience != nil {
		ids, err := s.audience.Resolve(ctx, a.TargetAudience, nil, s.maxRecipients)
		if err == nil {
			userIDs = ids
		}
	}
	if len(userIDs) == 0 {
		// Nothing to send; mark dispatched-with-zero so the cron stops
		// retrying. Could also mark failed; we choose dispatched with
		// SentCount=0 so the audit shows the resolution result.
		a.MarkAllRecipientsDelivered(now, 0)
		return s.repo.Update(ctx, a)
	}
	// Insert recipient rows first so MarkDelivered has something to write.
	rows := make([]domain.AnnouncementRecipient, 0, len(userIDs))
	for _, uid := range userIDs {
		rows = append(rows, domain.AnnouncementRecipient{
			ID:             uuid.New(),
			AnnouncementID: a.ID,
			UserID:         uid,
		})
	}
	if s.recipients != nil {
		if _, err := s.recipients.CreateBatch(ctx, rows); err != nil {
			s.log.Warn("recipient batch failed", "err", err)
		}
	}
	delivered := 0
	for _, row := range rows {
		if s.dispatcher == nil {
			break
		}
		channel, err := s.dispatcher.Dispatch(ctx, a, row.UserID)
		if err != nil {
			continue
		}
		if s.recipients != nil {
			if err := s.recipients.MarkDelivered(ctx, row.ID, channel, time.Now().UTC()); err == nil {
				delivered++
			}
		} else {
			delivered++
		}
	}
	if delivered == len(rows) {
		a.MarkAllRecipientsDelivered(now, delivered)
	} else {
		a.MarkSomeFailed(now, delivered, len(rows))
	}
	return s.repo.Update(ctx, a)
}

// ListMyInbox returns the announcements delivered to the caller.
func (s *AnnouncementService) ListMyInbox(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit int) ([]port.AnnouncementInboxEntry, error) {
	if s == nil || s.recipients == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	return s.recipients.ListMyInbox(ctx, userID, unreadOnly, limit)
}

// MarkRead stamps the recipient row's read_at for the (announcement, user) pair.
func (s *AnnouncementService) MarkRead(ctx context.Context, announcementID, userID uuid.UUID) error {
	if s == nil || s.recipients == nil {
		return derrors.Internal("operations.announcement.no_recipients", "recipient repo not wired")
	}
	return s.recipients.MarkRead(ctx, announcementID, userID, time.Now().UTC())
}

// ListRecipients returns the recipient roster for an announcement (audit/ops view).
func (s *AnnouncementService) ListRecipients(ctx context.Context, announcementID uuid.UUID) ([]domain.AnnouncementRecipient, error) {
	if s == nil || s.recipients == nil {
		return nil, nil
	}
	return s.recipients.ListByAnnouncement(ctx, announcementID)
}
