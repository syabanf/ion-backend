package usecase

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// Stubs for team-related repos (Wave 124).
// =====================================================================

type stubTeamRepo struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*domain.Team
	byName map[string]*domain.Team
}

func newStubTeamRepo() *stubTeamRepo {
	return &stubTeamRepo{byID: map[uuid.UUID]*domain.Team{}, byName: map[string]*domain.Team{}}
}
func (r *stubTeamRepo) Insert(_ context.Context, t *domain.Team) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[t.ID] = t
	r.byName[t.Name] = t
	return nil
}
func (r *stubTeamRepo) Update(_ context.Context, t *domain.Team) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[t.ID] = t
	return nil
}
func (r *stubTeamRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Team, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, errNotFound
	}
	return t, nil
}
func (r *stubTeamRepo) List(_ context.Context, onlyActive bool) ([]domain.Team, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.Team{}
	for _, t := range r.byID {
		if onlyActive && !t.IsActive {
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

type stubTeamMemberRepo struct {
	mu       sync.Mutex
	rows     []*domain.TeamMember
	openMap  map[uuid.UUID]int
}

func newStubTeamMemberRepo() *stubTeamMemberRepo {
	return &stubTeamMemberRepo{openMap: map[uuid.UUID]int{}}
}
func (r *stubTeamMemberRepo) Insert(_ context.Context, m *domain.TeamMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, m)
	return nil
}
func (r *stubTeamMemberRepo) Update(_ context.Context, _ *domain.TeamMember) error {
	return nil
}
func (r *stubTeamMemberRepo) FindActiveByTeamUser(_ context.Context, teamID, userID uuid.UUID) (*domain.TeamMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.rows {
		if m.TeamID == teamID && m.UserID == userID && m.LeftAt == nil {
			return m, nil
		}
	}
	return nil, errNotFound
}
func (r *stubTeamMemberRepo) ListByTeam(_ context.Context, teamID uuid.UUID, includeLeft bool) ([]domain.TeamMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.TeamMember{}
	for _, m := range r.rows {
		if m.TeamID != teamID {
			continue
		}
		if !includeLeft && m.LeftAt != nil {
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}
func (r *stubTeamMemberRepo) OpenTicketCountByUser(_ context.Context, _ uuid.UUID) (map[uuid.UUID]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uuid.UUID]int, len(r.openMap))
	for k, v := range r.openMap {
		out[k] = v
	}
	return out, nil
}

type stubAssignHistoryRepo struct {
	mu       sync.Mutex
	rows     []*domain.AssignmentEvent
}

func (r *stubAssignHistoryRepo) Insert(_ context.Context, ev *domain.AssignmentEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, ev)
	return nil
}
func (r *stubAssignHistoryRepo) ListByTicket(_ context.Context, ticketID uuid.UUID, _ int) ([]domain.AssignmentEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.AssignmentEvent{}
	for _, ev := range r.rows {
		if ev.TicketID == ticketID {
			out = append(out, *ev)
		}
	}
	return out, nil
}

// =====================================================================
// Tests
// =====================================================================

func TestTeam_RoundRobinPicksLowestOpenCount(t *testing.T) {
	ctx := context.Background()
	teams := newStubTeamRepo()
	members := newStubTeamMemberRepo()
	hist := &stubAssignHistoryRepo{}
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	svc := NewTeamService(teams, members, tickets, hist, events, nil)

	team, err := svc.CreateTeam(ctx, "tech-support-1", "L1", nil, []domain.TicketType{domain.TicketTypeTechnical})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	// 3 agents with different open ticket counts. Round-robin picks the
	// one with the lowest count.
	alice := uuid.New()
	bob := uuid.New()
	carol := uuid.New()
	for _, uid := range []uuid.UUID{alice, bob, carol} {
		if _, err := svc.AddMember(ctx, team.ID, uid, domain.TeamRoleAgent); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
	}
	members.openMap[alice] = 5
	members.openMap[bob] = 1 // lowest → winner
	members.openMap[carol] = 7

	tk := mustNewTicket(t, uuid.New(), uuid.New())
	tickets.byID = map[uuid.UUID]*domain.Ticket{tk.ID: tk}
	out, err := svc.RoundRobinAssign(ctx, tk.ID, team.ID, uuid.New(), "cs_supervisor")
	if err != nil {
		t.Fatalf("RoundRobinAssign: %v", err)
	}
	if out.AssignedUserID == nil || *out.AssignedUserID != bob {
		t.Fatalf("expected bob (lowest open count), got %v", out.AssignedUserID)
	}
	if len(hist.rows) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(hist.rows))
	}
	if hist.rows[0].AssignmentKind != domain.AssignmentKindUser {
		t.Fatalf("expected user assignment kind, got %s", hist.rows[0].AssignmentKind)
	}
}

func TestTeam_RoundRobin_RefusesFocusMismatch(t *testing.T) {
	ctx := context.Background()
	teams := newStubTeamRepo()
	members := newStubTeamMemberRepo()
	hist := &stubAssignHistoryRepo{}
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	svc := NewTeamService(teams, members, tickets, hist, events, nil)

	// Billing-focused team.
	team, _ := svc.CreateTeam(ctx, "billing", "", nil, []domain.TicketType{domain.TicketTypeBilling})
	uid := uuid.New()
	_, _ = svc.AddMember(ctx, team.ID, uid, domain.TeamRoleAgent)

	tk := mustNewTicket(t, uuid.New(), uuid.New()) // ticket_type = technical
	tickets.byID = map[uuid.UUID]*domain.Ticket{tk.ID: tk}
	if _, err := svc.RoundRobinAssign(ctx, tk.ID, team.ID, uuid.New(), "cs_supervisor"); err == nil {
		t.Fatalf("expected focus-mismatch conflict")
	}
}

func TestTeam_AssignTicketToTeam_WritesHistory(t *testing.T) {
	ctx := context.Background()
	teams := newStubTeamRepo()
	members := newStubTeamMemberRepo()
	hist := &stubAssignHistoryRepo{}
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	svc := NewTeamService(teams, members, tickets, hist, events, nil)

	team, _ := svc.CreateTeam(ctx, "tech", "", nil, []domain.TicketType{domain.TicketTypeTechnical})
	tk := mustNewTicket(t, uuid.New(), uuid.New())
	tickets.byID = map[uuid.UUID]*domain.Ticket{tk.ID: tk}

	if _, err := svc.AssignTicketToTeam(ctx, tk.ID, team.ID, uuid.New(), "cs_supervisor"); err != nil {
		t.Fatalf("AssignTicketToTeam: %v", err)
	}
	if len(hist.rows) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(hist.rows))
	}
	if hist.rows[0].AssignmentKind != domain.AssignmentKindTeam {
		t.Fatalf("expected team assignment kind, got %s", hist.rows[0].AssignmentKind)
	}
	if hist.rows[0].ToTeamID == nil || *hist.rows[0].ToTeamID != team.ID {
		t.Fatalf("ToTeamID not recorded correctly")
	}
}

func TestTeam_RemoveMember(t *testing.T) {
	ctx := context.Background()
	teams := newStubTeamRepo()
	members := newStubTeamMemberRepo()
	svc := NewTeamService(teams, members, &stubTicketRepo{}, &stubAssignHistoryRepo{}, &stubEventRepo{}, nil)

	team, _ := svc.CreateTeam(ctx, "tech", "", nil, nil)
	uid := uuid.New()
	if _, err := svc.AddMember(ctx, team.ID, uid, domain.TeamRoleAgent); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := svc.RemoveMember(ctx, team.ID, uid); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	// FindActive should now miss.
	if _, err := members.FindActiveByTeamUser(ctx, team.ID, uid); err == nil {
		// allowed — left_at is set so the inverted predicate kicks in
		// only when the stub honors it. The stub does honor it.
		t.Fatalf("expected NotFound after leave; got nil")
	}
}
