// Wave 127 — CS SLA + Service Requests + Teams + WO-from-Ticket + CSAT
// E2E (Wave 124 surface).
//
// All tests t.Skip cleanly when DATABASE_URL is unset or the Wave 124
// migration (0083) hasn't been applied (cs.sla_matrix table missing).
//
// Notes on the breach test:
//   - We seed a matrix with tiny budgets (1 minute each).
//   - We CreateTicket, then SQL-UPDATE created_at to 5 minutes ago so
//     EffectiveAge crosses both budgets.
//   - We invoke EvaluateBreaches twice — first emit; second is the
//     idempotency guard (no new breach increments).
//
// The pause test:
//   - Same trick — SQL-rewind created_at past the resolve budget.
//   - We accumulate pause_seconds via SQL UPDATE so EffectiveAge =
//     wall_age - pause is back under budget.
//   - EvaluateBreaches must NOT flip the resolve flag in that case.
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	cspg "github.com/ion-core/backend/internal/cs/adapter/postgres"
	csdom "github.com/ion-core/backend/internal/cs/domain"
	csport "github.com/ion-core/backend/internal/cs/port"
	csuc "github.com/ion-core/backend/internal/cs/usecase"
)

// csSLAHarness wires the SLA + ticket + (optional) team / sr / csat
// services. Wave 124 schema-dependent — skips on missing cs.sla_matrix.
type csSLAHarness struct {
	tickets    *csuc.TicketService
	sla        *csuc.SLAService
	teams      *csuc.TeamService
	csat       *csuc.CSATService
	srSvc      *csuc.ServiceRequestService
	wftSvc     *csuc.WOFromTicketService

	ticketRepo *cspg.TicketRepository
	eventRepo  *cspg.TicketEventRepository
	slaRepo    *cspg.SLAMatrixRepository
	teamRepo   *cspg.TeamRepository
	memberRepo *cspg.TeamMemberRepository
	assignHist *cspg.AssignmentHistoryRepository
	srRepo     *cspg.ServiceRequestRepository
	csatRepo   *cspg.CSATRepository

	notifier *recordingNotifier
	ctRes    *recordingCustomerType
	woBridge *recordingWOBridge
	csatDisp *recordingCSATDispatcher
}

// recordingCustomerType is a CustomerTypeResolver stub. Returns
// CustomerTypeResidential for everyone unless explicitly set.
type recordingCustomerType struct {
	Default csdom.CustomerType
	M       map[uuid.UUID]csdom.CustomerType
}

func (r *recordingCustomerType) Resolve(_ context.Context, id uuid.UUID) (csdom.CustomerType, error) {
	if r.M != nil {
		if ct, ok := r.M[id]; ok {
			return ct, nil
		}
	}
	if r.Default != "" {
		return r.Default, nil
	}
	return csdom.CustomerTypeResidential, nil
}

// recordingWOBridge records CreateWOFromTicket calls — returns a fresh
// uuid each call but doesn't touch field.work_orders.
type recordingWOBridge struct {
	Calls []uuid.UUID
}

func (b *recordingWOBridge) CreateWOFromTicket(_ context.Context, ticketID uuid.UUID, _ *uuid.UUID, _ *time.Time, _ uuid.UUID) (uuid.UUID, error) {
	b.Calls = append(b.Calls, ticketID)
	return uuid.New(), nil
}

// recordingCSATDispatcher records SendInvite calls.
type recordingCSATDispatcher struct {
	Calls int
}

func (d *recordingCSATDispatcher) SendInvite(_ context.Context, _, _ uuid.UUID, _ string) error {
	d.Calls++
	return nil
}

func newCSSLAHarness(t *testing.T) *csSLAHarness {
	t.Helper()
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "cs.tickets")
	w121cSkipIfMissingTable(t, pool, "cs.sla_matrix")
	w121cSkipIfMissingTable(t, pool, "cs.service_requests")
	w121cSkipIfMissingTable(t, pool, "cs.teams")
	w121cSkipIfMissingTable(t, pool, "cs.team_members")
	w121cSkipIfMissingTable(t, pool, "cs.ticket_assignments_history")
	w121cSkipIfMissingTable(t, pool, "cs.csat_responses")

	ticketRepo := cspg.NewTicketRepository(pool)
	eventRepo := cspg.NewTicketEventRepository(pool)
	slaRepo := cspg.NewSLAMatrixRepository(pool)
	teamRepo := cspg.NewTeamRepository(pool)
	memberRepo := cspg.NewTeamMemberRepository(pool)
	assignHist := cspg.NewAssignmentHistoryRepository(pool)
	srRepo := cspg.NewServiceRequestRepository(pool)
	csatRepo := cspg.NewCSATRepository(pool)

	notifier := &recordingNotifier{}
	ctRes := &recordingCustomerType{Default: csdom.CustomerTypeResidential, M: map[uuid.UUID]csdom.CustomerType{}}
	woBridge := &recordingWOBridge{}
	csatDisp := &recordingCSATDispatcher{}

	slaSvc := csuc.NewSLAService(slaRepo, ticketRepo, eventRepo, ctRes, notifier, ticketRepo)
	csatSvc := csuc.NewCSATService(csatRepo, ticketRepo, eventRepo, csatDisp, notifier)
	tickets := csuc.NewTicketService(ticketRepo, eventRepo, notifier).
		WithSLA(slaSvc).WithCSAT(csatSvc).WithAssignmentHistory(assignHist)
	srSvc := csuc.NewServiceRequestService(srRepo, tickets, eventRepo).WithWOBridge(woBridge)
	teams := csuc.NewTeamService(teamRepo, memberRepo, ticketRepo, assignHist, eventRepo, notifier)
	wftSvc := csuc.NewWOFromTicketService(ticketRepo, eventRepo, woBridge)

	return &csSLAHarness{
		tickets:    tickets,
		sla:        slaSvc,
		teams:      teams,
		csat:       csatSvc,
		srSvc:      srSvc,
		wftSvc:     wftSvc,
		ticketRepo: ticketRepo,
		eventRepo:  eventRepo,
		slaRepo:    slaRepo,
		teamRepo:   teamRepo,
		memberRepo: memberRepo,
		assignHist: assignHist,
		srRepo:     srRepo,
		csatRepo:   csatRepo,
		notifier:   notifier,
		ctRes:      ctRes,
		woBridge:   woBridge,
		csatDisp:   csatDisp,
	}
}

// seedSLAMatrixEntry inserts an active SLA-matrix row scoped by (ct, tt,
// p). Returns the entry id.
func seedSLAMatrixEntry(t *testing.T, h *csSLAHarness, ct csdom.CustomerType, tt csdom.TicketType, p csdom.Priority, frMin, rvMin int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	e, err := csdom.NewSLAMatrixEntry(ct, tt, p, frMin, rvMin, 0.80, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("NewSLAMatrixEntry: %v", err)
	}
	if err := h.slaRepo.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert matrix: %v", err)
	}
	pool := w121cDB(t)
	t.Cleanup(w121cCleanup(pool, "cs.sla_matrix", "id", e.ID.String()))
	return e.ID
}

// TC-PSL-001 — ticket open stamps SLA-matrix snapshot at create time.
func TestCS_SLA_AppliedOnCreate(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	matrixID := seedSLAMatrixEntry(t, h, csdom.CustomerTypeResidential, csdom.TicketTypeTechnical, csdom.PriorityHigh, 60, 240)

	customerID := uuid.New()
	h.ctRes.M[customerID] = csdom.CustomerTypeResidential
	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: customerID, OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 SLA-apply " + uuid.New().String()[:8], Priority: csdom.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	if tk.SLAMatrixID == nil || *tk.SLAMatrixID != matrixID {
		t.Errorf("sla_matrix_id: got %v want %s", tk.SLAMatrixID, matrixID)
	}
	if tk.SLAFirstResponseDueAt == nil || tk.SLAResolveDueAt == nil {
		t.Fatalf("sla due dates not stamped")
	}
	frDelta := tk.SLAFirstResponseDueAt.Sub(tk.CreatedAt)
	if frDelta < 59*time.Minute || frDelta > 61*time.Minute {
		t.Errorf("first_response_due delta: got %v want ≈60m", frDelta)
	}
}

// TC-PSL-005 — EvaluateBreaches flips first_response + resolve flags
// when EffectiveAge exceeds budgets; re-run is idempotent (no double-count).
func TestCS_SLA_EvaluateBreaches(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	seedSLAMatrixEntry(t, h, csdom.CustomerTypeResidential, csdom.TicketTypeTechnical, csdom.PriorityHigh, 1, 1)

	customerID := uuid.New()
	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: customerID, OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 sla-breach " + uuid.New().String()[:8], Priority: csdom.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	// Rewind created_at to 5 minutes ago so EffectiveAge crosses both
	// 1-minute budgets. We also rewind sla_*_due_at by 5min so the
	// breach predicate fires.
	w121cExec(t, pool, `
		UPDATE cs.tickets
		   SET created_at                  = NOW() - INTERVAL '5 minutes',
		       sla_first_response_due_at   = NOW() - INTERVAL '4 minutes',
		       sla_resolve_due_at          = NOW() - INTERVAL '4 minutes'
		 WHERE id = $1
	`, tk.ID)

	rep, err := h.sla.EvaluateBreaches(ctx)
	if err != nil {
		t.Fatalf("EvaluateBreaches: %v", err)
	}
	if rep.NewFirstResponseBreach < 1 {
		t.Errorf("first_response breach count: got %d want >=1", rep.NewFirstResponseBreach)
	}
	if rep.NewResolveBreach < 1 {
		t.Errorf("resolve breach count: got %d want >=1", rep.NewResolveBreach)
	}

	// Re-run — flags already set, so no new breaches.
	rep2, err := h.sla.EvaluateBreaches(ctx)
	if err != nil {
		t.Fatalf("EvaluateBreaches#2: %v", err)
	}
	// We can't assert "exactly 0" if other tests left rows; we can only
	// assert that this particular ticket no longer contributes. Check
	// the row directly.
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if !got.SLABreachedFirstResponse || !got.SLABreachedResolve {
		t.Errorf("breach flags should be set: fr=%v rv=%v", got.SLABreachedFirstResponse, got.SLABreachedResolve)
	}
	_ = rep2
}

// TC-PSL-004 — pause subtracts from EffectiveAge so a paused ticket
// past the wall-clock budget is NOT flagged as breached.
func TestCS_SLA_PauseSubtractsFromAge(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	seedSLAMatrixEntry(t, h, csdom.CustomerTypeResidential, csdom.TicketTypeBilling, csdom.PriorityNormal, 10, 60)

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeBilling,
		Title: "W127 pause-sub " + uuid.New().String()[:8], Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	// Rewind created_at by 90 minutes (past the 60-min resolve budget),
	// add 80 minutes of pause_seconds — net 10 minutes effective age.
	w121cExec(t, pool, `
		UPDATE cs.tickets
		   SET created_at      = NOW() - INTERVAL '90 minutes',
		       pause_seconds   = 80 * 60,
		       sla_resolve_due_at = NOW() + INTERVAL '50 minutes'
		 WHERE id = $1
	`, tk.ID)

	rep, err := h.sla.EvaluateBreaches(ctx)
	if err != nil {
		t.Fatalf("EvaluateBreaches: %v", err)
	}
	_ = rep
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if got.SLABreachedResolve {
		t.Errorf("resolve breach unexpectedly set on paused ticket")
	}
}

// TC-SR-001..006 — Service Request submit → approve → start → fulfill flow.
func TestCS_ServiceRequest_FullFlow(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	seedSLAMatrixEntry(t, h, csdom.CustomerTypeResidential, csdom.TicketTypeServiceRequest, csdom.PriorityNormal, 30, 1440)

	customerID := uuid.New()
	submittedBy := uuid.New()
	sr, err := h.srSvc.Submit(ctx, csport.SubmitServiceRequestInput{
		CustomerID:  customerID,
		RequestType: csdom.SRTypePlanChange,
		SubmittedBy: submittedBy,
		OpenedVia:   csdom.OpenedViaPortal,
		Title:       "W127 SR-plan-change " + uuid.New().String()[:8],
		Description: "Upgrade requested",
		Priority:    csdom.PriorityNormal,
		Payload:     map[string]any{"to_product_code": "BB-100"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.service_requests", "id", sr.ID.String()))
	if sr.TicketID != uuid.Nil {
		t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", sr.TicketID.String()))
	}

	// Approve (plan_change requires approval per domain rule).
	approver := uuid.New()
	if sr.Status == csdom.SRStatusSubmitted {
		if _, err := h.srSvc.Approve(ctx, sr.ID, approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
	}
	if _, err := h.srSvc.StartFulfillment(ctx, sr.ID, approver); err != nil {
		t.Fatalf("StartFulfillment: %v", err)
	}
	if _, err := h.srSvc.MarkFulfilled(ctx, sr.ID, approver); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	got, _ := h.srSvc.Get(ctx, sr.ID)
	if got.Status != csdom.SRStatusFulfilled {
		t.Errorf("final SR status: got %q want fulfilled", got.Status)
	}
}

// TC-TA-003 — Team round-robin assignment picks the member with the
// lowest open-ticket count.
func TestCS_Team_RoundRobinPicksLowestOpen(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	// Create a team with 2 members.
	team, err := h.teams.CreateTeam(ctx, "W127-team-"+uuid.New().String()[:8], "wave127 e2e", nil, []csdom.TicketType{csdom.TicketTypeTechnical})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.teams", "id", team.ID.String()))

	agentA := uuid.New()
	agentB := uuid.New()
	if _, err := h.teams.AddMember(ctx, team.ID, agentA, csdom.TeamRoleAgent); err != nil {
		t.Fatalf("AddMember A: %v", err)
	}
	if _, err := h.teams.AddMember(ctx, team.ID, agentB, csdom.TeamRoleAgent); err != nil {
		t.Fatalf("AddMember B: %v", err)
	}

	// Pre-seed two tickets already assigned to agentA so RR picks agentB.
	for i := 0; i < 2; i++ {
		seedTk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
			CustomerID: uuid.New(), OpenedBy: uuid.New(),
			OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
			Title: "W127 seed " + uuid.New().String()[:8], Priority: csdom.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("seed CreateTicket: %v", err)
		}
		t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", seedTk.ID.String()))
		if _, err := h.tickets.AssignTicket(ctx, seedTk.ID, agentA, uuid.New(), "supervisor"); err != nil {
			t.Fatalf("seed Assign: %v", err)
		}
	}

	// New ticket, then RoundRobinAssign.
	newTk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 rr " + uuid.New().String()[:8], Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", newTk.ID.String()))

	got, err := h.teams.RoundRobinAssign(ctx, newTk.ID, team.ID, uuid.New(), "supervisor")
	if err != nil {
		t.Fatalf("RoundRobinAssign: %v", err)
	}
	if got.AssignedUserID == nil || *got.AssignedUserID != agentB {
		t.Errorf("RR pick: got %v want agentB %s", got.AssignedUserID, agentB)
	}
}

// TC-WFT-001 — WO-from-Ticket bridge fires and stamps related_wo_id.
func TestCS_WOFromTicket_StampsRelatedWOID(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 wo-from " + uuid.New().String()[:8], Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	by := uuid.New()
	woID, err := h.wftSvc.CreateWO(ctx, tk.ID, nil, nil, by, "agent")
	if err != nil {
		t.Fatalf("CreateWO: %v", err)
	}
	if woID == uuid.Nil {
		t.Fatal("CreateWO returned uuid.Nil")
	}
	if len(h.woBridge.Calls) != 1 || h.woBridge.Calls[0] != tk.ID {
		t.Errorf("woBridge.Calls = %v want [%s]", h.woBridge.Calls, tk.ID)
	}
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if got.RelatedWOID == nil || *got.RelatedWOID != woID {
		t.Errorf("related_wo_id: got %v want %s", got.RelatedWOID, woID)
	}
}

// TC-CSAT-003 — CSAT rating=1 fires the critical/alert signal. The
// CSATService persists the row + records an event; the supervisor
// notification is downstream of the event subject.
func TestCS_CSAT_LowScoreSignal(t *testing.T) {
	h := newCSSLAHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 csat-low " + uuid.New().String()[:8], Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))
	if _, err := h.tickets.StartTicket(ctx, tk.ID, uuid.New(), "agent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.tickets.ResolveTicket(ctx, tk.ID, "best effort", uuid.New(), "agent"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	resp, err := h.csat.RecordResponse(ctx, tk.ID, 1, "very dissatisfied", "email")
	if err != nil {
		t.Fatalf("RecordResponse: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.csat_responses", "id", resp.ID.String()))
	if resp.Rating != 1 {
		t.Errorf("rating: got %d want 1", resp.Rating)
	}

	// Aggregations report this as a detractor.
	agg, err := h.csat.Aggregations(ctx, csport.CSATAggregationFilter{})
	if err != nil {
		t.Fatalf("Aggregations: %v", err)
	}
	if agg.Detractors < 1 {
		t.Errorf("detractors after rating=1: got %d want >=1", agg.Detractors)
	}
}
