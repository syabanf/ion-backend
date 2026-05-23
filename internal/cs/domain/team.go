package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Team + TeamMember — agent grouping for round-robin / team-level
// assignment.
// =====================================================================

// Team mirrors cs.teams. focus_ticket_types is an array of TicketType
// values the team handles; an empty slice means "any".
type Team struct {
	ID               uuid.UUID
	Name             string
	Description      string
	ManagerUserID    *uuid.UUID
	MembersCount     int
	FocusTicketTypes []TicketType
	IsActive         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewTeam constructs a team.
func NewTeam(name, description string, managerUserID *uuid.UUID, focus []TicketType) (*Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.Validation("cs.team.name_required", "team name is required")
	}
	for _, tt := range focus {
		if !tt.Valid() {
			return nil, errors.Validation("cs.team.focus_invalid", "focus_ticket_types contains invalid value: "+string(tt))
		}
	}
	now := time.Now().UTC()
	return &Team{
		ID:               uuid.New(),
		Name:             name,
		Description:      description,
		ManagerUserID:    managerUserID,
		FocusTicketTypes: focus,
		IsActive:         true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// CanReceiveTicketType reports whether the team's focus allows this
// ticket type. Empty focus → any type ok.
func (t *Team) CanReceiveTicketType(tt TicketType) bool {
	if !t.IsActive {
		return false
	}
	if len(t.FocusTicketTypes) == 0 {
		return true
	}
	for _, f := range t.FocusTicketTypes {
		if f == tt {
			return true
		}
	}
	return false
}

// TeamMemberRole — cs.team_members.role_in_team.
type TeamMemberRole string

const (
	TeamRoleAgent   TeamMemberRole = "agent"
	TeamRoleLead    TeamMemberRole = "lead"
	TeamRoleManager TeamMemberRole = "manager"
	TeamRoleBackup  TeamMemberRole = "backup"
)

func (r TeamMemberRole) Valid() bool {
	switch r {
	case TeamRoleAgent, TeamRoleLead, TeamRoleManager, TeamRoleBackup:
		return true
	}
	return false
}

// TeamMember is a soft-delete entity (left_at marks removal).
type TeamMember struct {
	ID         uuid.UUID
	TeamID     uuid.UUID
	UserID     uuid.UUID
	RoleInTeam TeamMemberRole
	JoinedAt   time.Time
	LeftAt     *time.Time
}

// NewTeamMember constructs a member with role default = agent.
func NewTeamMember(teamID, userID uuid.UUID, role TeamMemberRole) (*TeamMember, error) {
	if teamID == uuid.Nil {
		return nil, errors.Validation("cs.team_member.team_required", "team_id is required")
	}
	if userID == uuid.Nil {
		return nil, errors.Validation("cs.team_member.user_required", "user_id is required")
	}
	if role == "" {
		role = TeamRoleAgent
	}
	if !role.Valid() {
		return nil, errors.Validation("cs.team_member.role_invalid", "role_in_team is not recognized")
	}
	return &TeamMember{
		ID:         uuid.New(),
		TeamID:     teamID,
		UserID:     userID,
		RoleInTeam: role,
		JoinedAt:   time.Now().UTC(),
	}, nil
}

// Leave soft-deletes the member by stamping left_at. Idempotent.
func (m *TeamMember) Leave(at time.Time) {
	if m.LeftAt != nil {
		return
	}
	v := at.UTC()
	m.LeftAt = &v
}

// PromoteTo updates the role. Refuses if the member has left.
func (m *TeamMember) PromoteTo(role TeamMemberRole) error {
	if m.LeftAt != nil {
		return errors.Conflict("cs.team_member.left", "member has already left the team")
	}
	if !role.Valid() {
		return errors.Validation("cs.team_member.role_invalid", "role_in_team is not recognized")
	}
	m.RoleInTeam = role
	return nil
}

// IsActive reports whether the member is still in the team.
func (m *TeamMember) IsActive() bool {
	return m.LeftAt == nil
}
