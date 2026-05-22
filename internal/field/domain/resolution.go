package domain

import (
	"time"

	"github.com/google/uuid"
)

type ResolutionCategory string

const (
	ResCatConfig   ResolutionCategory = "config"
	ResCatHardware ResolutionCategory = "hardware"
	ResCatCabling  ResolutionCategory = "cabling"
	ResCatSignal   ResolutionCategory = "signal"
	ResCatSoftware ResolutionCategory = "software"
	ResCatOther    ResolutionCategory = "other"
)

// Valid mirrors the DB CHECK on field.wo_resolution_items.category.
func (c ResolutionCategory) Valid() bool {
	switch c {
	case ResCatConfig, ResCatHardware, ResCatCabling, ResCatSignal,
		ResCatSoftware, ResCatOther:
		return true
	}
	return false
}

type ResolutionStatus string

const (
	ResolutionResolved      ResolutionStatus = "resolved"
	ResolutionPartial       ResolutionStatus = "partial"
	ResolutionUnable        ResolutionStatus = "unable"
	ResolutionEscalatedNOC  ResolutionStatus = "escalated_to_noc"
	ResolutionEscalatedTL   ResolutionStatus = "escalated_to_team_leader"
)

// Valid mirrors the DB CHECK on field.wo_resolution_items.resolution_status.
func (s ResolutionStatus) Valid() bool {
	switch s {
	case ResolutionResolved, ResolutionPartial, ResolutionUnable,
		ResolutionEscalatedNOC, ResolutionEscalatedTL:
		return true
	}
	return false
}

// ResolutionItem is the on-site free-form work log. Multiple per WO; ordered.
// We let any item_order — no uniqueness — so the UI can append without
// re-numbering existing rows when re-ordering after the fact.
type ResolutionItem struct {
	ID                uuid.UUID
	WOID              uuid.UUID
	ItemOrder         int
	ItemLabel         string
	Category          ResolutionCategory
	Finding           string
	ActionTaken       string
	ResolutionStatus  ResolutionStatus
	TimeSpentMinutes  *int
	ResolvedBy        *uuid.UUID
	LoggedAt          time.Time
}
