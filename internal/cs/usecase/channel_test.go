package usecase

import (
	"context"
	"sync"
	"testing"

	"github.com/ion-core/backend/internal/cs/domain"
)

// stubChannelRepo implements port.TicketChannelRepository in memory.
type stubChannelRepo struct {
	mu      sync.Mutex
	byCode  map[string]*domain.Channel
}

func newStubChannelRepo() *stubChannelRepo {
	return &stubChannelRepo{byCode: map[string]*domain.Channel{}}
}

func (r *stubChannelRepo) Insert(_ context.Context, c *domain.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byCode[c.Code]; ok {
		return errAlreadyExists
	}
	r.byCode[c.Code] = c
	return nil
}
func (r *stubChannelRepo) Update(_ context.Context, c *domain.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byCode[c.Code] = c
	return nil
}
func (r *stubChannelRepo) FindByCode(_ context.Context, code string) (*domain.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byCode[code]
	if !ok {
		return nil, errNotFound
	}
	return c, nil
}
func (r *stubChannelRepo) List(_ context.Context, onlyActive bool) ([]domain.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.Channel{}
	for _, c := range r.byCode {
		if onlyActive && !c.IsActive {
			continue
		}
		out = append(out, *c)
	}
	return out, nil
}

var errAlreadyExists = stubErr("already exists")

func TestChannelService_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := newStubChannelRepo()
	svc := NewChannelService(repo)

	c, err := svc.CreateChannel(ctx, "voicemail", "Voicemail", domain.ChannelKindInbound, true, map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Code != "voicemail" {
		t.Fatalf("code = %q want voicemail", c.Code)
	}

	got, err := svc.ListChannels(ctx, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(got))
	}

	disabled := false
	newName := "Voicemail Inbox"
	upd, err := svc.UpdateChannel(ctx, "voicemail", &newName, nil, &disabled, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Name != newName {
		t.Fatalf("name = %q want %q", upd.Name, newName)
	}
	if upd.IsActive {
		t.Fatal("expected is_active=false")
	}

	// only_active should now hide it
	got, _ = svc.ListChannels(ctx, true)
	if len(got) != 0 {
		t.Fatalf("expected zero active channels, got %d", len(got))
	}
}

func TestChannelService_CreateRejectsBadKind(t *testing.T) {
	ctx := context.Background()
	repo := newStubChannelRepo()
	svc := NewChannelService(repo)
	if _, err := svc.CreateChannel(ctx, "test", "Test", domain.ChannelKind("psychic"), true, nil); err == nil {
		t.Fatal("expected validation error on bad kind")
	}
}
