package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	opsport "github.com/ion-core/backend/internal/operations/port"
)

// =====================================================================
// Stubs
// =====================================================================

type stubDashRepo struct {
	created []dashCreateCall
	rows    map[string]*opsport.DashboardAggregationRow
}

type dashCreateCall struct {
	kind    string
	userID  *uuid.UUID
	teamID  *uuid.UUID
	payload map[string]any
}

func newStubDashRepo() *stubDashRepo {
	return &stubDashRepo{rows: map[string]*opsport.DashboardAggregationRow{}}
}

func (s *stubDashRepo) Create(_ context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID, _ , _ *time.Time, payload map[string]any) error {
	s.created = append(s.created, dashCreateCall{kind: kind, userID: scopeUserID, teamID: scopeTeamID, payload: payload})
	return nil
}
func (s *stubDashRepo) LatestByKind(_ context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID) (*opsport.DashboardAggregationRow, error) {
	key := kind
	if scopeUserID != nil {
		key += scopeUserID.String()
	}
	if scopeTeamID != nil {
		key += scopeTeamID.String()
	}
	return s.rows[key], nil
}

type stubDashLive struct {
	agentSnap    opsport.AgentQueueSnapshot
	teamSnap     opsport.TeamSLASnapshot
	escs         []opsport.EscalationRow
	satSummary   opsport.SatisfactionSummary
	chanDist     map[string]int
	activeAgents []uuid.UUID
	supervisors  []uuid.UUID
}

func (s *stubDashLive) AgentQueue(_ context.Context, userID uuid.UUID) (opsport.AgentQueueSnapshot, error) {
	out := s.agentSnap
	out.UserID = userID
	return out, nil
}
func (s *stubDashLive) SupervisorTeamSLA(_ context.Context, sid uuid.UUID) (opsport.TeamSLASnapshot, error) {
	out := s.teamSnap
	out.SupervisorUserID = sid
	return out, nil
}
func (s *stubDashLive) EscalationQueue(_ context.Context, _ int, _ int) ([]opsport.EscalationRow, error) {
	return s.escs, nil
}
func (s *stubDashLive) SatisfactionSummary(_ context.Context, _, _ time.Time) (opsport.SatisfactionSummary, error) {
	return s.satSummary, nil
}
func (s *stubDashLive) ChannelDistribution(_ context.Context, _, _ time.Time) (map[string]int, error) {
	return s.chanDist, nil
}
func (s *stubDashLive) ActiveAgentIDs(_ context.Context, _ int) ([]uuid.UUID, error) {
	return s.activeAgents, nil
}
func (s *stubDashLive) SupervisorTeamIDs(_ context.Context, _ int) ([]uuid.UUID, error) {
	return s.supervisors, nil
}

// =====================================================================
// Tests
// =====================================================================

func TestCSDashboard_AgentQueue_LiveFallback(t *testing.T) {
	live := &stubDashLive{agentSnap: opsport.AgentQueueSnapshot{OpenAssigned: 4, SLAAtRisk: 1}}
	svc := NewCSDashboardService(CSDashboardDeps{Live: live})
	uid := uuid.New()
	out, err := svc.AgentQueue(context.Background(), uid)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.OpenAssigned != 4 {
		t.Errorf("expected 4, got %d", out.OpenAssigned)
	}
	if out.UserID != uid {
		t.Errorf("user id not propagated")
	}
}

func TestCSDashboard_AgentQueue_UsesCacheWhenFresh(t *testing.T) {
	uid := uuid.New()
	repo := newStubDashRepo()
	key := "agent_queue" + uid.String()
	repo.rows[key] = &opsport.DashboardAggregationRow{
		Kind:         "agent_queue",
		ScopeUserID:  &uid,
		AggregatedAt: time.Now().Add(-time.Minute),
		Payload: map[string]any{
			"open_assigned": float64(7),
			"sla_at_risk":   float64(2),
		},
	}
	live := &stubDashLive{agentSnap: opsport.AgentQueueSnapshot{OpenAssigned: 99}} // shouldn't be used
	svc := NewCSDashboardService(CSDashboardDeps{Repo: repo, Live: live, StaleAfter: 5 * time.Minute})
	out, err := svc.AgentQueue(context.Background(), uid)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.OpenAssigned != 7 {
		t.Errorf("expected cache hit (7), got %d", out.OpenAssigned)
	}
}

func TestCSDashboard_AgentQueue_FallsBackOnStaleCache(t *testing.T) {
	uid := uuid.New()
	repo := newStubDashRepo()
	key := "agent_queue" + uid.String()
	repo.rows[key] = &opsport.DashboardAggregationRow{
		Kind:         "agent_queue",
		ScopeUserID:  &uid,
		AggregatedAt: time.Now().Add(-time.Hour), // stale
		Payload: map[string]any{
			"open_assigned": float64(7),
		},
	}
	live := &stubDashLive{agentSnap: opsport.AgentQueueSnapshot{OpenAssigned: 12}}
	svc := NewCSDashboardService(CSDashboardDeps{Repo: repo, Live: live, StaleAfter: 5 * time.Minute})
	out, _ := svc.AgentQueue(context.Background(), uid)
	if out.OpenAssigned != 12 {
		t.Errorf("expected live fallback (12), got %d", out.OpenAssigned)
	}
}

func TestCSDashboard_PrecomputeTick_PersistsAllKinds(t *testing.T) {
	live := &stubDashLive{
		activeAgents: []uuid.UUID{uuid.New(), uuid.New()},
		supervisors:  []uuid.UUID{uuid.New()},
		escs:         []opsport.EscalationRow{{TicketID: uuid.New(), Level: 3}},
		satSummary:   opsport.SatisfactionSummary{Count: 10, AvgRating: 4.2, NPSScore: 30},
		chanDist:     map[string]int{"whatsapp": 12, "email": 8},
	}
	repo := newStubDashRepo()
	svc := NewCSDashboardService(CSDashboardDeps{Repo: repo, Live: live})
	n, err := svc.PrecomputeTick(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 2 agent rows + 1 supervisor + 1 escalation + 1 satisfaction + 1 channel = 6
	if n != 6 {
		t.Errorf("expected 6 rows precomputed, got %d", n)
	}
	if len(repo.created) != 6 {
		t.Errorf("expected 6 creates, got %d", len(repo.created))
	}
}

func TestCSDashboard_SupervisorTeamSLA_Live(t *testing.T) {
	live := &stubDashLive{teamSnap: opsport.TeamSLASnapshot{OpenCount: 25, CompliancePct: 0.93}}
	svc := NewCSDashboardService(CSDashboardDeps{Live: live})
	out, _ := svc.SupervisorTeamSLA(context.Background(), uuid.New())
	if out.OpenCount != 25 {
		t.Errorf("expected 25, got %d", out.OpenCount)
	}
}

func TestCSDashboard_EscalationQueue_DefaultsMinLevel(t *testing.T) {
	live := &stubDashLive{escs: []opsport.EscalationRow{{TicketID: uuid.New(), Level: 2}}}
	svc := NewCSDashboardService(CSDashboardDeps{Live: live})
	out, err := svc.EscalationQueue(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("expected 1 escalation, got %d", len(out))
	}
}
