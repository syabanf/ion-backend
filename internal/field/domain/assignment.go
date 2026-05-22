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

// Valid mirrors the DB CHECK on field.wo_assignments.grade so HTTP
// handlers can return a clean 400 for bad input rather than letting
// the DB violation surface as a 500.
func (g TechGrade) Valid() bool {
	switch g {
	case GradeSenior, GradeJunior:
		return true
	}
	return false
}

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
