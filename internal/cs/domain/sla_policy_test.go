package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// Wave 124 — SLA matrix entry + pause-aware breach tests (TC-PSL-*).
//
// These tests focus on the *logic* of resolution / breach computation
// — the repo + service tests live separately. They especially cover
// the integration point with Wave 123's Ticket.EffectiveAge (pause
// semantics).
// =====================================================================

func TestSLA_ResolveDueDates(t *testing.T) {
	e, err := NewSLAMatrixEntry(
		CustomerTypeResidential, TicketTypeTechnical, PriorityHigh,
		30, 480, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	if err != nil {
		t.Fatalf("NewSLAMatrixEntry: %v", err)
	}
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	fr, rv := e.ResolveDueDates(now)
	if got, want := fr.Sub(now), 30*time.Minute; got != want {
		t.Fatalf("first-response due = %s, want %s", got, want)
	}
	if got, want := rv.Sub(now), 480*time.Minute; got != want {
		t.Fatalf("resolve due = %s, want %s", got, want)
	}
}

func TestSLA_PauseAwareBreach(t *testing.T) {
	// A normal-priority residential technical ticket: 120 min first
	// response, 1440 min resolve. Open at t=0. Pause from t=10 to t=130
	// (120 minutes paused). At t=120 wall-clock, effective age is
	// (120 - 120 paused remainder) = ... actually let's compute exactly:
	//
	//   wall t = 120 min
	//   paused = 120 min - 10 min (still paused remainder at t=120 is 110 min from pause start at t=10)
	//   EffectiveAge subtracts the live in-flight pause window: paused (in-flight, since t=10) = 110 min
	//   effective_age = 120 - 110 = 10 min
	//
	// Since 10 min < 120 min first-response budget, no breach yet.
	tk, err := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"slow speed", "speed below plan", PriorityNormal)
	if err != nil {
		t.Fatalf("NewTicket: %v", err)
	}
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tk.CreatedAt = t0
	// Move to pending_customer at t+10.
	if err := tk.Start(uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tk.Pause(PauseWaitCustomer, "awaiting reply"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	pauseStart := t0.Add(10 * time.Minute)
	tk.PausedSince = &pauseStart
	// MarkFirstResponse fires on Start, so we have first_response_at set;
	// for this test we want to model a ticket where first_response is NOT
	// yet recorded (pause before first reply). Clear it.
	tk.FirstResponseAt = nil

	entry := mustNewSLA(t, PriorityNormal, 120, 1440)
	now := t0.Add(120 * time.Minute) // 120 min wall
	if entry.IsBreachedFirstResponse(now, tk) {
		t.Fatalf("should NOT be breached: pause kept effective age low")
	}
	// At t+250 (240 min into the pause), effective age = 250 - 240 = 10 min still
	// (pause grew with wall-clock). Still no breach.
	now2 := t0.Add(250 * time.Minute)
	if entry.IsBreachedFirstResponse(now2, tk) {
		t.Fatalf("still should not be breached while paused")
	}
}

func TestSLA_BreachesWhenAgeExceedsBudget(t *testing.T) {
	tk, _ := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"x", "y", PriorityHigh)
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tk.CreatedAt = t0
	tk.FirstResponseAt = nil
	entry := mustNewSLA(t, PriorityHigh, 30, 480)
	now := t0.Add(45 * time.Minute) // 45 min > 30 min budget
	if !entry.IsBreachedFirstResponse(now, tk) {
		t.Fatalf("expected first-response breach at 45 min for 30 min budget")
	}
	if entry.IsBreachedResolve(now, tk) {
		t.Fatalf("should NOT be resolve-breached at 45 min for 480 min budget")
	}
}

func TestSLA_NotBreachedAfterFirstResponseRecorded(t *testing.T) {
	tk, _ := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"x", "y", PriorityHigh)
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tk.CreatedAt = t0
	respondedAt := t0.Add(20 * time.Minute)
	tk.FirstResponseAt = &respondedAt
	entry := mustNewSLA(t, PriorityHigh, 30, 480)
	now := t0.Add(120 * time.Minute) // long past 30 min budget
	if entry.IsBreachedFirstResponse(now, tk) {
		t.Fatalf("first-response breach should NOT fire after first_response_at recorded")
	}
}

func TestSLA_ResolveBreachIgnoresResolvedTicket(t *testing.T) {
	tk, _ := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"x", "y", PriorityHigh)
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tk.CreatedAt = t0
	tk.Status = TicketStatusResolved
	entry := mustNewSLA(t, PriorityHigh, 30, 60)
	now := t0.Add(120 * time.Minute)
	if entry.IsBreachedResolve(now, tk) {
		t.Fatalf("resolved tickets should not be flagged for resolve breach")
	}
}

func TestSLA_WarnWindow(t *testing.T) {
	tk, _ := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"x", "y", PriorityHigh)
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tk.CreatedAt = t0
	// budget = 100 min; warn at 80% = 80 min.
	entry := mustNewSLA(t, PriorityHigh, 30, 100)
	// At 85 min — past warn line, before breach.
	if !entry.IsInWarnWindow(t0.Add(85*time.Minute), tk) {
		t.Fatalf("expected in-warn-window at 85 min")
	}
	// At 70 min — before warn line.
	if entry.IsInWarnWindow(t0.Add(70*time.Minute), tk) {
		t.Fatalf("should not be in warn window at 70 min")
	}
	// At 105 min — past breach.
	if entry.IsInWarnWindow(t0.Add(105*time.Minute), tk) {
		t.Fatalf("should not warn — already breached at 105 min")
	}
	// Once warned_at is set, warn-window returns false.
	tk.SLAWarnedAt = &t0
	if entry.IsInWarnWindow(t0.Add(85*time.Minute), tk) {
		t.Fatalf("should not warn again after warned_at set")
	}
}

func TestSLA_MapCRMCustomerType(t *testing.T) {
	cases := map[string]CustomerType{
		"broadband":  CustomerTypeResidential,
		"":           CustomerTypeResidential,
		"BUSINESS":   CustomerTypeBusiness,
		"enterprise": CustomerTypeEnterprise,
		"corporate":  CustomerTypeEnterprise,
		"reseller":   CustomerTypeReseller,
		"internal":   CustomerTypeInternal,
		"unknown":    CustomerTypeResidential,
	}
	for in, want := range cases {
		if got := MapCRMCustomerType(in); got != want {
			t.Fatalf("MapCRMCustomerType(%q) = %s, want %s", in, got, want)
		}
	}
}

// 10th matrix-cell test — all 5×5×4 cells should validate as resolvable
// via NewSLAMatrixEntry.
func TestSLA_AllCellsAreConstructible(t *testing.T) {
	customerTypes := []CustomerType{
		CustomerTypeResidential, CustomerTypeBusiness, CustomerTypeEnterprise,
		CustomerTypeReseller, CustomerTypeInternal,
	}
	ticketTypes := []TicketType{
		TicketTypeTechnical, TicketTypeBilling, TicketTypeComplaint,
		TicketTypeServiceRequest, TicketTypeInformation,
	}
	priorities := []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}
	count := 0
	for _, ct := range customerTypes {
		for _, tt := range ticketTypes {
			for _, p := range priorities {
				_, err := NewSLAMatrixEntry(ct, tt, p, 30, 240, 0.80, time.Now().UTC())
				if err != nil {
					t.Fatalf("NewSLAMatrixEntry(%s,%s,%s): %v", ct, tt, p, err)
				}
				count++
			}
		}
	}
	if count != 100 {
		t.Fatalf("expected 100 cells, built %d", count)
	}
}

func mustNewSLA(t *testing.T, p Priority, frMin, rvMin int) *SLAMatrixEntry {
	t.Helper()
	e, err := NewSLAMatrixEntry(
		CustomerTypeResidential, TicketTypeTechnical, p,
		frMin, rvMin, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	if err != nil {
		t.Fatalf("NewSLAMatrixEntry: %v", err)
	}
	return e
}
