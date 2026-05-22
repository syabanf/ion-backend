package domain

import (
	"time"

	"github.com/google/uuid"
)

type TechGrade string

const (
	GradeSenior TechGrade = "senior"
	GradeJunior TechGrade = "junior"
)

type WORole string

const (
	WORoleLead     WORole = "lead"
	WORoleObserver WORole = "observer"
)

// Assignment ties a technician to a WO with a grade + role.
//
// Round-1 invariant: a WO can have at most ONE lead (DB partial-unique
// enforces this — see uniq_wo_lead). The observer count is unbounded but
// in practice we expect senior+junior pairs.
type Assignment struct {
	ID           uuid.UUID
	WOID         uuid.UUID
	TechnicianID uuid.UUID
	Grade        TechGrade
	WORole       WORole
	AssignedBy   *uuid.UUID
	AssignedAt   time.Time
}
