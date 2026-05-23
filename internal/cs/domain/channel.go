package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ChannelKind disambiguates direction. Mirror of
// cs.ticket_channels.kind CHECK.
type ChannelKind string

const (
	ChannelKindInbound  ChannelKind = "inbound"
	ChannelKindOutbound ChannelKind = "outbound"
	ChannelKindBoth     ChannelKind = "both"
)

func (k ChannelKind) Valid() bool {
	switch k {
	case ChannelKindInbound, ChannelKindOutbound, ChannelKindBoth:
		return true
	}
	return false
}

// Channel is a CS communication channel record.
type Channel struct {
	ID            uuid.UUID
	Code          string
	Name          string
	Kind          ChannelKind
	IsActive      bool
	ConfigPayload map[string]any
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewChannel validates required fields and stamps timestamps.
func NewChannel(code, name string, kind ChannelKind, isActive bool, config map[string]any) (*Channel, error) {
	code = strings.TrimSpace(strings.ToLower(code))
	if code == "" {
		return nil, errors.Validation("cs.channel.code_required", "channel code is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.Validation("cs.channel.name_required", "channel name is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("cs.channel.kind_invalid", "channel kind must be inbound|outbound|both")
	}
	now := time.Now().UTC()
	return &Channel{
		ID:            uuid.New(),
		Code:          code,
		Name:          name,
		Kind:          kind,
		IsActive:      isActive,
		ConfigPayload: config,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// IsInbound reports whether the channel accepts inbound messages —
// used by the ticket-create path to validate opened_via against
// active inbound channels.
func (c *Channel) IsInbound() bool {
	return c.Kind == ChannelKindInbound || c.Kind == ChannelKindBoth
}

// Update applies a partial change. Nil pointers leave the field
// untouched.
func (c *Channel) Update(name *string, kind *ChannelKind, isActive *bool, config map[string]any) error {
	if name != nil {
		v := strings.TrimSpace(*name)
		if v == "" {
			return errors.Validation("cs.channel.name_required", "channel name is required")
		}
		c.Name = v
	}
	if kind != nil {
		if !kind.Valid() {
			return errors.Validation("cs.channel.kind_invalid", "channel kind must be inbound|outbound|both")
		}
		c.Kind = *kind
	}
	if isActive != nil {
		c.IsActive = *isActive
	}
	if config != nil {
		c.ConfigPayload = config
	}
	c.UpdatedAt = time.Now().UTC()
	return nil
}
