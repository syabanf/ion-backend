package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CSATResponse — 1-to-1 customer satisfaction survey per ticket.
// =====================================================================

// CSATChannel matches cs.csat_responses.channel.
type CSATChannel string

const (
	CSATChannelEmail    CSATChannel = "email"
	CSATChannelWhatsApp CSATChannel = "whatsapp"
	CSATChannelSMS      CSATChannel = "sms"
	CSATChannelPortal   CSATChannel = "portal"
	CSATChannelInapp    CSATChannel = "inapp"
)

func (c CSATChannel) Valid() bool {
	switch c {
	case CSATChannelEmail, CSATChannelWhatsApp, CSATChannelSMS,
		CSATChannelPortal, CSATChannelInapp:
		return true
	}
	return false
}

// CSATResponse mirrors cs.csat_responses.
type CSATResponse struct {
	ID          uuid.UUID
	TicketID    uuid.UUID
	CustomerID  uuid.UUID
	Rating      int
	Comment     string
	Channel     CSATChannel
	RequestedAt time.Time
	RespondedAt *time.Time
}

// NewCSATInvite creates a CSAT row in 'requested' state (rating=0 is
// invalid but we allow the row to exist for tracking — the response
// flow Updates the same row when the customer answers). For Wave 124
// we instead create the row only when the customer answers.
func NewCSATResponse(
	ticketID, customerID uuid.UUID,
	rating int,
	comment string,
	channel CSATChannel,
) (*CSATResponse, error) {
	if ticketID == uuid.Nil {
		return nil, errors.Validation("cs.csat.ticket_required", "ticket_id is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("cs.csat.customer_required", "customer_id is required")
	}
	if rating < 1 || rating > 5 {
		return nil, errors.Validation("cs.csat.rating_invalid", "rating must be between 1 and 5")
	}
	if channel == "" {
		channel = CSATChannelEmail
	}
	if !channel.Valid() {
		return nil, errors.Validation("cs.csat.channel_invalid", "channel is not recognized")
	}
	now := time.Now().UTC()
	return &CSATResponse{
		ID:          uuid.New(),
		TicketID:    ticketID,
		CustomerID:  customerID,
		Rating:      rating,
		Comment:     strings.TrimSpace(comment),
		Channel:     channel,
		RequestedAt: now,
		RespondedAt: &now,
	}, nil
}

// IsCriticalLow flags a 1- or 2-star response for supervisor review.
// Used by the CSAT service to emit a critical-csat ticket_event.
func IsCriticalLow(rating int) bool {
	return rating >= 1 && rating <= 2
}
