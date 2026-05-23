package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Communication — every inbound/outbound message logged per ticket.
//
// One row per discrete message. Inbound rows are typically created by
// the WhatsApp / email webhook handlers; outbound rows by agent
// replies via the CommunicationService.LogOutbound entrypoint.
// =====================================================================

// CommunicationKind matches cs.communications.kind.
type CommunicationKind string

const (
	CommKindEmailIn     CommunicationKind = "email_in"
	CommKindEmailOut    CommunicationKind = "email_out"
	CommKindWhatsAppIn  CommunicationKind = "whatsapp_in"
	CommKindWhatsAppOut CommunicationKind = "whatsapp_out"
	CommKindSMSIn       CommunicationKind = "sms_in"
	CommKindSMSOut      CommunicationKind = "sms_out"
	CommKindCallLog     CommunicationKind = "call_log"
	CommKindPortalMsg   CommunicationKind = "portal_msg"
)

func (k CommunicationKind) Valid() bool {
	switch k {
	case CommKindEmailIn, CommKindEmailOut,
		CommKindWhatsAppIn, CommKindWhatsAppOut,
		CommKindSMSIn, CommKindSMSOut,
		CommKindCallLog, CommKindPortalMsg:
		return true
	}
	return false
}

// CommDirection — inbound | outbound. Derivable from CommunicationKind
// but kept as a column for fast filtering on the timeline.
type CommDirection string

const (
	CommDirectionIn  CommDirection = "inbound"
	CommDirectionOut CommDirection = "outbound"
)

// DirectionFor returns the direction implied by the kind.
func DirectionFor(k CommunicationKind) CommDirection {
	switch k {
	case CommKindEmailIn, CommKindWhatsAppIn, CommKindSMSIn,
		CommKindCallLog, CommKindPortalMsg:
		return CommDirectionIn
	default:
		return CommDirectionOut
	}
}

// CounterpartyKind — who the message is to/from.
type CounterpartyKind string

const (
	CounterpartyCustomer   CounterpartyKind = "customer"
	CounterpartyAgent      CounterpartyKind = "agent"
	CounterpartySupervisor CounterpartyKind = "supervisor"
	CounterpartyExternal   CounterpartyKind = "external"
	CounterpartySystem     CounterpartyKind = "system"
)

func (c CounterpartyKind) Valid() bool {
	switch c {
	case CounterpartyCustomer, CounterpartyAgent, CounterpartySupervisor,
		CounterpartyExternal, CounterpartySystem:
		return true
	}
	return false
}

// CommAttachment is an inline attachment reference. The blob itself
// lives in object storage; this carries the link + label.
type CommAttachment struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// Communication is the aggregate.
type Communication struct {
	ID                uuid.UUID
	TicketID          *uuid.UUID
	Kind              CommunicationKind
	Direction         CommDirection
	CounterpartyKind  CounterpartyKind
	CounterpartyID    *uuid.UUID
	CounterpartyLabel string
	Subject           string
	Body              string
	Attachments       []CommAttachment
	ExternalMessageID string
	SentAt            time.Time
	DeliveredAt       *time.Time
	ReadAt            *time.Time
	ErrorMsg          string
	CreatedAt         time.Time
}

// NewCommunication validates + stamps timestamps.
func NewCommunication(
	ticketID *uuid.UUID,
	kind CommunicationKind,
	counterpartyKind CounterpartyKind,
	counterpartyID *uuid.UUID,
	counterpartyLabel, subject, body string,
	attachments []CommAttachment,
) (*Communication, error) {
	if !kind.Valid() {
		return nil, errors.Validation("cs.comm.kind_invalid", "communication kind is not recognized")
	}
	if counterpartyKind == "" {
		counterpartyKind = CounterpartyCustomer
	}
	if !counterpartyKind.Valid() {
		return nil, errors.Validation("cs.comm.counterparty_invalid", "counterparty_kind is not recognized")
	}
	if strings.TrimSpace(body) == "" && strings.TrimSpace(subject) == "" {
		return nil, errors.Validation("cs.comm.body_required", "subject or body is required")
	}
	now := time.Now().UTC()
	return &Communication{
		ID:                uuid.New(),
		TicketID:          ticketID,
		Kind:              kind,
		Direction:         DirectionFor(kind),
		CounterpartyKind:  counterpartyKind,
		CounterpartyID:    counterpartyID,
		CounterpartyLabel: counterpartyLabel,
		Subject:           subject,
		Body:              body,
		Attachments:       attachments,
		SentAt:            now,
		CreatedAt:         now,
	}, nil
}

// MarkDelivered stamps delivered_at if not already set.
func (c *Communication) MarkDelivered(at time.Time) {
	if c.DeliveredAt != nil {
		return
	}
	v := at.UTC()
	c.DeliveredAt = &v
}

// MarkRead stamps read_at if not already set.
func (c *Communication) MarkRead(at time.Time) {
	if c.ReadAt != nil {
		return
	}
	v := at.UTC()
	c.ReadAt = &v
}

// MarkError records an outbound-send failure.
func (c *Communication) MarkError(msg string) {
	c.ErrorMsg = msg
}
