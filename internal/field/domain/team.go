package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Team is a branch-scoped roster of technicians led by one user.
type Team struct {
	ID           uuid.UUID
	Code         string
	Name         string
	BranchID     uuid.UUID
	TeamLeaderID *uuid.UUID
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NewTeam(code, name string, branchID uuid.UUID, leaderID *uuid.UUID) (*Team, error) {
	code = strings.TrimSpace(strings.ToUpper(code))
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, errors.Validation("team.code_required", "code is required")
	}
	if name == "" {
		return nil, errors.Validation("team.name_required", "name is required")
	}
	if branchID == uuid.Nil {
		return nil, errors.Validation("team.branch_required", "branch_id is required")
	}
	return &Team{
		ID:           uuid.New(),
		Code:         code,
		Name:         name,
		BranchID:     branchID,
		TeamLeaderID: leaderID,
		Active:       true,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}, nil
}

type TeamMember struct {
	ID       uuid.UUID
	TeamID   uuid.UUID
	UserID   uuid.UUID
	Grade    TechGrade
	Active   bool
	JoinedAt time.Time
}
