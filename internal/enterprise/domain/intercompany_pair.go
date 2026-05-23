package domain

import (
	"time"

	"github.com/google/uuid"
)

// IntercompanyPair is the policy row that drives auto-accept behavior
// between a commercial-owner subsidiary and an executing subsidiary.
// One pair per ordered (commercial_owner, executing) tuple — the
// reverse direction is a separate pair so each side can configure
// independently.
//
// When `auto_accept = true` AND the IC-PO total is at or below the
// threshold, the AcceptCustomerPO flow issues + accepts the IC-PO
// atomically in the same transaction. A nil threshold means "any
// amount" (full delegation).
type IntercompanyPair struct {
	ID                          uuid.UUID
	CommercialOwnerSubsidiaryID uuid.UUID
	ExecutingSubsidiaryID       uuid.UUID
	AutoAccept                  bool
	AutoAcceptThreshold         *float64
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

// MatchesAutoAccept reports whether a candidate IC-PO with the given
// total should be auto-accepted by this pair's policy. `nil` threshold
// means no cap — auto-accept any amount.
func (p *IntercompanyPair) MatchesAutoAccept(amount float64) bool {
	if p == nil || !p.AutoAccept {
		return false
	}
	if p.AutoAcceptThreshold == nil {
		return true
	}
	return amount <= *p.AutoAcceptThreshold
}
