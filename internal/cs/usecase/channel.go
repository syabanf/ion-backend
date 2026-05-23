package usecase

import (
	"context"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// ChannelService implements port.ChannelUseCase.
type ChannelService struct {
	channels port.TicketChannelRepository
}

func NewChannelService(channels port.TicketChannelRepository) *ChannelService {
	return &ChannelService{channels: channels}
}

var _ port.ChannelUseCase = (*ChannelService)(nil)

func (s *ChannelService) ListChannels(ctx context.Context, onlyActive bool) ([]domain.Channel, error) {
	return s.channels.List(ctx, onlyActive)
}

func (s *ChannelService) CreateChannel(ctx context.Context, code, name string, kind domain.ChannelKind, isActive bool, config map[string]any) (*domain.Channel, error) {
	c, err := domain.NewChannel(code, name, kind, isActive, config)
	if err != nil {
		return nil, err
	}
	if err := s.channels.Insert(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *ChannelService) UpdateChannel(ctx context.Context, code string, name *string, kind *domain.ChannelKind, isActive *bool, config map[string]any) (*domain.Channel, error) {
	c, err := s.channels.FindByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if err := c.Update(name, kind, isActive, config); err != nil {
		return nil, err
	}
	if err := s.channels.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}
