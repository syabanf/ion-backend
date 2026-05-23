package domain

import (
	"time"

	"github.com/google/uuid"
)

// ScheduleHistoryEntry is the append-only audit row written every time
// an EWO is rescheduled. The current values live on the EWO row; this
// table captures the values that were active *before* the change so a
// reviewer can replay the timeline.
//
// Wave 96 — used by RescheduleEWO. EWO.Reschedule returns one of these
// for the caller to persist via EWOScheduleHistoryRepository.Create.
type ScheduleHistoryEntry struct {
	ID             uuid.UUID
	EWOID          uuid.UUID
	PrevStart      time.Time
	PrevEnd        time.Time
	PrevTeamLead   uuid.UUID // uuid.Nil if previously unassigned
	PrevTechnician uuid.UUID // uuid.Nil if previously unassigned
	ChangedBy      uuid.UUID
	ChangedAt      time.Time
	Reason         string
}
