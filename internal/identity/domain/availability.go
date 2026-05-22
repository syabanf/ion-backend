package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// AvailabilityStatus mirrors the CHECK constraint on identity.user_availability.
type AvailabilityStatus string

const (
	AvailableYes      AvailabilityStatus = "available"
	AvailableLeave    AvailabilityStatus = "leave"
	AvailableSick     AvailabilityStatus = "sick"
	AvailableTraining AvailabilityStatus = "training"
	AvailableOff      AvailabilityStatus = "off"
)

func IsValidAvailabilityStatus(s string) bool {
	switch AvailabilityStatus(s) {
	case AvailableYes, AvailableLeave, AvailableSick, AvailableTraining, AvailableOff:
		return true
	}
	return false
}

// UserAvailability is one row in identity.user_availability.
//
// We store one status per (user, date). The Team Leader sees a roster
// computed at read time: for each technician in their team, look up
// the row for today; missing rows default to "available" so quiet days
// don't require explicit data entry.
type UserAvailability struct {
	UserID    uuid.UUID
	Date      time.Time // date-only; the repo strips time
	Status    AvailabilityStatus
	Notes     string
	UpdatedBy *uuid.UUID
	UpdatedAt time.Time
}

// NormalizeNotes trims notes to avoid asymmetric whitespace in queries.
func (a *UserAvailability) NormalizeNotes() {
	a.Notes = strings.TrimSpace(a.Notes)
}
