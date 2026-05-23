package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// TeamService — Wave 124 team CRUD + ticket→team assignment +
// round-robin agent picker.
// =====================================================================

type TeamService struct {
	teams    port.TeamRepository
	members  port.TeamMemberRepository
	tickets  port.TicketRepository
	history  port.AssignmentHistoryRepository
	events   port.TicketEventRepository
	notifier port.NotificationBridge
}

func NewTeamService(
	teams port.TeamRepository,
	members port.TeamMemberRepository,
	tickets port.TicketRepository,
	history port.AssignmentHistoryRepository,
	events port.TicketEventRepository,
	notifier port.NotificationBridge,
) *TeamService {
	return &TeamService{
		teams:    teams,
		members:  members,
		tickets:  tickets,
		history:  history,
		events:   events,
		notifier: notifier,
	}
}

var _ port.TeamUseCase = (*TeamService)(nil)

func (s *TeamService) CreateTeam(ctx context.Context, name, desc string, managerUserID *uuid.UUID, focus []domain.TicketType) (*domain.Team, error) {
	t, err := domain.NewTeam(name, desc, managerUserID, focus)
	if err != nil {
		return nil, err
	}
	if err := s.teams.Insert(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *TeamService) ListTeams(ctx context.Context, onlyActive bool) ([]domain.Team, error) {
	return s.teams.List(ctx, onlyActive)
}

func (s *TeamService) GetTeam(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	return s.teams.FindByID(ctx, id)
}

func (s *TeamService) AddMember(ctx context.Context, teamID, userID uuid.UUID, role domain.TeamMemberRole) (*domain.TeamMember, error) {
	if _, err := s.teams.FindByID(ctx, teamID); err != nil {
		return nil, err
	}
	m, err := domain.NewTeamMember(teamID, userID, role)
	if err != nil {
		return nil, err
	}
	if err := s.members.Insert(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *TeamService) RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error {
	m, err := s.members.FindActiveByTeamUser(ctx, teamID, userID)
	if err != nil {
		return err
	}
	m.Leave(time.Now().UTC())
	return s.members.Update(ctx, m)
}

func (s *TeamService) ListMembers(ctx context.Context, teamID uuid.UUID, includeLeft bool) ([]domain.TeamMember, error) {
	return s.members.ListByTeam(ctx, teamID, includeLeft)
}

// AssignTicketToTeam sets the ticket's assigned_team_id (no user yet)
// and writes a ticket_assignments_history audit row.
func (s *TeamService) AssignTicketToTeam(ctx context.Context, ticketID, teamID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	team, err := s.teams.FindByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !team.CanReceiveTicketType(t.TicketType) {
		return nil, errors.Conflict("cs.team.focus_mismatch",
			"team focus does not allow ticket_type "+string(t.TicketType))
	}
	prevTeam := t.AssignedTeamID
	tid := team.ID
	t.AssignedTeamID = &tid
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	if s.history != nil {
		var byPtr *uuid.UUID
		if byUserID != uuid.Nil {
			b := byUserID
			byPtr = &b
		}
		_ = s.history.Insert(ctx, domain.NewAssignmentEvent(
			t.ID,
			domain.AssignmentKindTeam,
			nil, nil,
			prevTeam, &tid,
			byPtr,
			"assign-to-team",
		))
	}
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindAssignment, ptrIfNotNil(byUserID), actorRole, map[string]any{
			"assigned_team_id": tid.String(),
		}))
	}
	return t, nil
}

// RoundRobinAssign picks the active team member with the smallest open
// ticket queue and assigns the ticket to that user. Falls back to the
// first member if every member has zero open tickets (i.e. equal-tie).
func (s *TeamService) RoundRobinAssign(ctx context.Context, ticketID, teamID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	team, err := s.teams.FindByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	members, err := s.members.ListByTeam(ctx, teamID, false)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, errors.Conflict("cs.team.empty", "team has no active members")
	}
	counts, err := s.members.OpenTicketCountByUser(ctx, teamID)
	if err != nil {
		// Best-effort — fall back to first member.
		counts = map[uuid.UUID]int{}
	}

	// Skip lead/manager unless they're the only members — RR favors agents.
	type cand struct {
		uid uuid.UUID
		cnt int
	}
	var pool []cand
	for _, m := range members {
		if m.RoleInTeam == domain.TeamRoleAgent || m.RoleInTeam == domain.TeamRoleBackup {
			pool = append(pool, cand{uid: m.UserID, cnt: counts[m.UserID]})
		}
	}
	if len(pool) == 0 {
		// fall back to lead/manager
		for _, m := range members {
			pool = append(pool, cand{uid: m.UserID, cnt: counts[m.UserID]})
		}
	}
	winner := pool[0]
	for _, c := range pool[1:] {
		if c.cnt < winner.cnt {
			winner = c
		}
	}

	// Persist team + user assignment.
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !team.CanReceiveTicketType(t.TicketType) {
		return nil, errors.Conflict("cs.team.focus_mismatch",
			"team focus does not allow ticket_type "+string(t.TicketType))
	}
	prevUser := t.AssignedUserID
	if err := t.Assign(winner.uid); err != nil {
		return nil, err
	}
	tid := team.ID
	t.AssignedTeamID = &tid
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	if s.history != nil {
		var byPtr *uuid.UUID
		if byUserID != uuid.Nil {
			b := byUserID
			byPtr = &b
		}
		_ = s.history.Insert(ctx, domain.NewAssignmentEvent(
			t.ID,
			domain.AssignmentKindUser,
			prevUser, &winner.uid,
			nil, &tid,
			byPtr,
			"round-robin",
		))
	}
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindAssignment, ptrIfNotNil(byUserID), actorRole, map[string]any{
			"assignee_user_id": winner.uid.String(),
			"assigned_team_id": tid.String(),
			"strategy":         "round_robin",
		}))
	}
	if s.notifier != nil {
		s.notifier.NotifyAssignment(ctx, t.ID, winner.uid, t.Title)
	}
	return t, nil
}

func ptrIfNotNil(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	v := u
	return &v
}
