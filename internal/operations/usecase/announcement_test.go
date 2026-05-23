package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
)

type stubAnnRepo struct {
	rows map[uuid.UUID]*domain.Announcement
}

func newStubAnnRepo() *stubAnnRepo {
	return &stubAnnRepo{rows: map[uuid.UUID]*domain.Announcement{}}
}

func (s *stubAnnRepo) Create(_ context.Context, a *domain.Announcement) error {
	s.rows[a.ID] = a
	return nil
}
func (s *stubAnnRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Announcement, error) {
	if a, ok := s.rows[id]; ok {
		return a, nil
	}
	return nil, nil
}
func (s *stubAnnRepo) ListPending(_ context.Context, _ time.Time, _ int) ([]domain.Announcement, error) {
	out := []domain.Announcement{}
	for _, a := range s.rows {
		if a.DispatchStatus == domain.DispatchPending {
			out = append(out, *a)
		}
	}
	return out, nil
}
func (s *stubAnnRepo) Update(_ context.Context, a *domain.Announcement) error {
	s.rows[a.ID] = a
	return nil
}

type stubRecipientRepo struct {
	rows     []domain.AnnouncementRecipient
	delivered map[uuid.UUID]bool
}

func newStubRecipientRepo() *stubRecipientRepo {
	return &stubRecipientRepo{delivered: map[uuid.UUID]bool{}}
}

func (s *stubRecipientRepo) CreateBatch(_ context.Context, rows []domain.AnnouncementRecipient) (int, error) {
	s.rows = append(s.rows, rows...)
	return len(rows), nil
}
func (s *stubRecipientRepo) ListByAnnouncement(_ context.Context, _ uuid.UUID) ([]domain.AnnouncementRecipient, error) {
	return s.rows, nil
}
func (s *stubRecipientRepo) ListMyInbox(_ context.Context, _ uuid.UUID, _ bool, _ int) ([]port.AnnouncementInboxEntry, error) {
	return nil, nil
}
func (s *stubRecipientRepo) MarkDelivered(_ context.Context, id uuid.UUID, _ string, _ time.Time) error {
	s.delivered[id] = true
	return nil
}
func (s *stubRecipientRepo) MarkRead(_ context.Context, _, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (s *stubRecipientRepo) CountDelivered(_ context.Context, _ uuid.UUID) (int, int, error) {
	return len(s.delivered), len(s.rows), nil
}

type stubAudienceResolver struct {
	users []uuid.UUID
}

func (s *stubAudienceResolver) Resolve(_ context.Context, _ domain.AnnouncementTargetAudience, _ map[string]any, _ int) ([]uuid.UUID, error) {
	return s.users, nil
}

type stubAnnDispatcher struct {
	failFor map[uuid.UUID]bool
	called  int
}

func (s *stubAnnDispatcher) Dispatch(_ context.Context, _ *domain.Announcement, userID uuid.UUID) (string, error) {
	s.called++
	if s.failFor != nil && s.failFor[userID] {
		return "", errors.New("dispatch failed")
	}
	return "push", nil
}

// =====================================================================

func TestAnnouncement_Create_DefaultsBySeverity(t *testing.T) {
	repo := newStubAnnRepo()
	svc := NewAnnouncementService(AnnouncementDeps{Repo: repo})

	a, err := svc.Create(context.Background(), CreateAnnouncementInput{
		Title:    "Outage",
		Body:     "Network event",
		Severity: domain.AnnouncementUrgent,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(a.Channels) != 3 {
		t.Errorf("urgent should default to 3 channels, got %v", a.Channels)
	}
	if a.DispatchStatus != domain.DispatchPending {
		t.Errorf("new announcement should be pending, got %s", a.DispatchStatus)
	}
}

func TestAnnouncement_Create_RequiresTitleBody(t *testing.T) {
	repo := newStubAnnRepo()
	svc := NewAnnouncementService(AnnouncementDeps{Repo: repo})
	if _, err := svc.Create(context.Background(), CreateAnnouncementInput{}); err == nil {
		t.Fatalf("expected validation error for empty title/body")
	}
}

func TestAnnouncement_Dispatch_AllDelivered(t *testing.T) {
	repo := newStubAnnRepo()
	rep := newStubRecipientRepo()
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	aud := &stubAudienceResolver{users: users}
	disp := &stubAnnDispatcher{}
	svc := NewAnnouncementService(AnnouncementDeps{
		Repo: repo, Recipients: rep, Audience: aud, Dispatcher: disp,
	})

	a, _ := svc.Create(context.Background(), CreateAnnouncementInput{
		Title: "All hands", Body: "Sync now", Severity: domain.AnnouncementInfo,
	})

	res, err := svc.DispatchOne(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.DispatchStatus != domain.DispatchDispatched {
		t.Errorf("expected dispatched, got %s", res.DispatchStatus)
	}
	if res.SentCount != 3 {
		t.Errorf("expected SentCount 3, got %d", res.SentCount)
	}
	if disp.called != 3 {
		t.Errorf("dispatcher called %d times, want 3", disp.called)
	}
}

func TestAnnouncement_Dispatch_Partial(t *testing.T) {
	repo := newStubAnnRepo()
	rep := newStubRecipientRepo()
	u1 := uuid.New()
	u2 := uuid.New()
	u3 := uuid.New()
	users := []uuid.UUID{u1, u2, u3}
	aud := &stubAudienceResolver{users: users}
	disp := &stubAnnDispatcher{failFor: map[uuid.UUID]bool{u2: true}}
	svc := NewAnnouncementService(AnnouncementDeps{
		Repo: repo, Recipients: rep, Audience: aud, Dispatcher: disp,
	})

	a, _ := svc.Create(context.Background(), CreateAnnouncementInput{
		Title: "Maintenance", Body: "FYI", Severity: domain.AnnouncementImportant,
	})
	res, err := svc.DispatchOne(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.DispatchStatus != domain.DispatchPartial {
		t.Errorf("expected partial, got %s", res.DispatchStatus)
	}
	if res.SentCount != 2 {
		t.Errorf("expected SentCount 2, got %d", res.SentCount)
	}
}

func TestAnnouncement_Dispatch_ConflictOnAlreadyDispatched(t *testing.T) {
	repo := newStubAnnRepo()
	rep := newStubRecipientRepo()
	aud := &stubAudienceResolver{users: []uuid.UUID{uuid.New()}}
	disp := &stubAnnDispatcher{}
	svc := NewAnnouncementService(AnnouncementDeps{
		Repo: repo, Recipients: rep, Audience: aud, Dispatcher: disp,
	})

	a, _ := svc.Create(context.Background(), CreateAnnouncementInput{
		Title: "X", Body: "Y", Severity: domain.AnnouncementInfo,
	})
	_, _ = svc.DispatchOne(context.Background(), a.ID)
	// second dispatch should be rejected
	if _, err := svc.DispatchOne(context.Background(), a.ID); err == nil {
		t.Fatalf("expected conflict error on re-dispatch of dispatched announcement")
	}
}
