package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 123 — Ticket state-machine contract tests (TC-TL-*).
//
//   open → assigned → in_progress → pending_customer | pending_internal
//     → resolved → closed (terminal)
//   resolved | closed → in_progress (reopen)
// =====================================================================

func newTicketAt(t *testing.T, status TicketStatus) *Ticket {
	t.Helper()
	tk, err := NewTicket(uuid.New(), uuid.New(), OpenedViaPortal, TicketTypeTechnical,
		"No internet", "totally dark", PriorityHigh)
	if err != nil {
		t.Fatalf("NewTicket: %v", err)
	}
	by := uuid.New()
	switch status {
	case TicketStatusOpen:
	case TicketStatusAssigned:
		if err := tk.Assign(uuid.New()); err != nil {
			t.Fatalf("Assign: %v", err)
		}
	case TicketStatusInProgress:
		if err := tk.Start(by); err != nil {
			t.Fatalf("Start: %v", err)
		}
	case TicketStatusPendingCustomer:
		if err := tk.Start(by); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := tk.Pause(PauseWaitCustomer, "waiting for confirmation"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
	case TicketStatusPendingInternal:
		if err := tk.Start(by); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := tk.Pause(PauseWaitInternal, "waiting for NOC"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
	case TicketStatusResolved:
		if err := tk.Start(by); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := tk.Resolve(by, "fixed"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	case TicketStatusClosed:
		if err := tk.Start(by); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := tk.Resolve(by, "fixed"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if err := tk.Close(by); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	return tk
}

// TestNewTicket_Validation guards the constructor invariants.
func TestNewTicket_Validation(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*ticketArgs)
		wantErrCode string
	}{
		{"empty title", func(a *ticketArgs) { a.title = "" }, "cs.ticket.title_required"},
		{"empty customer", func(a *ticketArgs) { a.customerID = uuid.Nil }, "cs.ticket.customer_required"},
		{"empty opener", func(a *ticketArgs) { a.openedBy = uuid.Nil }, "cs.ticket.opened_by_required"},
		{"bad channel", func(a *ticketArgs) { a.openedVia = OpenedVia("smoke_signal") }, "cs.ticket.opened_via_invalid"},
		{"bad type", func(a *ticketArgs) { a.ticketType = TicketType("rant") }, "cs.ticket.type_invalid"},
		{"bad priority", func(a *ticketArgs) { a.priority = Priority("five_alarm_fire") }, "cs.ticket.priority_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := defaultTicketArgs()
			tc.mutate(&a)
			_, err := NewTicket(a.customerID, a.openedBy, a.openedVia, a.ticketType, a.title, a.description, a.priority)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErrCode)
			}
			if e := derrors.As(err); e == nil || e.Code != tc.wantErrCode {
				t.Fatalf("expected code %q, got %q", tc.wantErrCode, errCodeOf(err))
			}
		})
	}
}

// TestTicketSM_ValidTransitions covers every legal arrow.
func TestTicketSM_ValidTransitions(t *testing.T) {
	by := uuid.New()
	cases := []struct {
		name     string
		from     TicketStatus
		action   func(*Ticket) error
		want     TicketStatus
	}{
		{"open→assigned", TicketStatusOpen, func(tk *Ticket) error { return tk.Assign(uuid.New()) }, TicketStatusAssigned},
		{"open→in_progress (start)", TicketStatusOpen, func(tk *Ticket) error { return tk.Start(by) }, TicketStatusInProgress},
		{"assigned→in_progress", TicketStatusAssigned, func(tk *Ticket) error { return tk.Start(by) }, TicketStatusInProgress},
		{"in_progress→pending_customer", TicketStatusInProgress, func(tk *Ticket) error { return tk.Pause(PauseWaitCustomer, "wait") }, TicketStatusPendingCustomer},
		{"in_progress→pending_internal", TicketStatusInProgress, func(tk *Ticket) error { return tk.Pause(PauseWaitInternal, "wait") }, TicketStatusPendingInternal},
		{"pending_customer→in_progress (resume)", TicketStatusPendingCustomer, func(tk *Ticket) error { return tk.Resume(by) }, TicketStatusInProgress},
		{"pending_internal→in_progress (resume)", TicketStatusPendingInternal, func(tk *Ticket) error { return tk.Resume(by) }, TicketStatusInProgress},
		{"in_progress→resolved", TicketStatusInProgress, func(tk *Ticket) error { return tk.Resolve(by, "fixed") }, TicketStatusResolved},
		{"resolved→closed", TicketStatusResolved, func(tk *Ticket) error { return tk.Close(by) }, TicketStatusClosed},
		{"closed→in_progress (reopen)", TicketStatusClosed, func(tk *Ticket) error { return tk.Reopen(by, "still broken") }, TicketStatusInProgress},
		{"resolved→in_progress (reopen)", TicketStatusResolved, func(tk *Ticket) error { return tk.Reopen(by, "still broken") }, TicketStatusInProgress},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := newTicketAt(t, tc.from)
			if err := tc.action(tk); err != nil {
				t.Fatalf("action: %v", err)
			}
			if tk.Status != tc.want {
				t.Fatalf("status = %q, want %q", tk.Status, tc.want)
			}
		})
	}
}

// TestTicketSM_InvalidTransitions guards the SM against illegal arrows.
func TestTicketSM_InvalidTransitions(t *testing.T) {
	by := uuid.New()
	cases := []struct {
		name    string
		from    TicketStatus
		action  func(*Ticket) error
		wantErr string
	}{
		{"close from open", TicketStatusOpen, func(tk *Ticket) error { return tk.Close(by) }, "cs.ticket.cannot_close"},
		{"close from in_progress", TicketStatusInProgress, func(tk *Ticket) error { return tk.Close(by) }, "cs.ticket.cannot_close"},
		{"resolve from open", TicketStatusOpen, func(tk *Ticket) error { return tk.Resolve(by, "x") }, "cs.ticket.cannot_resolve"},
		{"resolve from pending_customer", TicketStatusPendingCustomer, func(tk *Ticket) error { return tk.Resolve(by, "x") }, "cs.ticket.cannot_resolve"},
		{"pause from open", TicketStatusOpen, func(tk *Ticket) error { return tk.Pause(PauseWaitCustomer, "x") }, "cs.ticket.cannot_pause"},
		{"resume from open", TicketStatusOpen, func(tk *Ticket) error { return tk.Resume(by) }, "cs.ticket.cannot_resume"},
		{"resume from in_progress", TicketStatusInProgress, func(tk *Ticket) error { return tk.Resume(by) }, "cs.ticket.cannot_resume"},
		{"assign from in_progress", TicketStatusInProgress, func(tk *Ticket) error { return tk.Assign(uuid.New()) }, "cs.ticket.cannot_assign"},
		{"assign from closed", TicketStatusClosed, func(tk *Ticket) error { return tk.Assign(uuid.New()) }, "cs.ticket.cannot_assign"},
		{"start from resolved", TicketStatusResolved, func(tk *Ticket) error { return tk.Start(by) }, "cs.ticket.cannot_start"},
		{"reopen from open", TicketStatusOpen, func(tk *Ticket) error { return tk.Reopen(by, "x") }, "cs.ticket.cannot_reopen"},
		{"resolve without notes", TicketStatusInProgress, func(tk *Ticket) error { return tk.Resolve(by, "  ") }, "cs.ticket.resolution_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := newTicketAt(t, tc.from)
			err := tc.action(tk)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if got := errCodeOf(err); got != tc.wantErr {
				t.Fatalf("expected code %q, got %q", tc.wantErr, got)
			}
		})
	}
}

// TestTicketSM_FirstResponseStampOnce verifies that MarkFirstResponse
// is idempotent — repeated Starts (e.g. after a reopen) don't reset it.
func TestTicketSM_FirstResponseStampOnce(t *testing.T) {
	by := uuid.New()
	tk := newTicketAt(t, TicketStatusOpen)
	if err := tk.Start(by); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if tk.FirstResponseAt == nil {
		t.Fatal("expected first_response_at after Start")
	}
	first := *tk.FirstResponseAt
	// resolve + close + reopen + restart
	if err := tk.Resolve(by, "fixed"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := tk.Close(by); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tk.Reopen(by, "no"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	// after reopen we're already in_progress; Start should be a no-op
	if err := tk.Start(by); err != nil {
		t.Fatalf("Start (after reopen): %v", err)
	}
	if tk.FirstResponseAt == nil || !tk.FirstResponseAt.Equal(first) {
		t.Fatalf("first_response_at changed across reopen: got %v want %v", tk.FirstResponseAt, first)
	}
}

// TestTicketSM_PauseAccumulator verifies that consecutive
// pause/resume cycles add up.
func TestTicketSM_PauseAccumulator(t *testing.T) {
	by := uuid.New()
	tk := newTicketAt(t, TicketStatusInProgress)

	if err := tk.Pause(PauseWaitCustomer, "wait"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// fake the elapsed pause by rewinding paused_since 30s
	past := time.Now().UTC().Add(-30 * time.Second)
	tk.PausedSince = &past
	if err := tk.Resume(by); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if tk.PauseSeconds < 29 {
		t.Fatalf("expected pause_seconds >= 29, got %d", tk.PauseSeconds)
	}

	// second cycle
	if err := tk.Pause(PauseWaitInternal, "noc"); err != nil {
		t.Fatalf("Pause #2: %v", err)
	}
	past = time.Now().UTC().Add(-15 * time.Second)
	tk.PausedSince = &past
	if err := tk.Resume(by); err != nil {
		t.Fatalf("Resume #2: %v", err)
	}
	if tk.PauseSeconds < 44 {
		t.Fatalf("expected pause_seconds >= 44 (cumulative), got %d", tk.PauseSeconds)
	}
}

// TestTicketSM_ReopenIncrementsEscalation tracks the reopens counter
// the catalog calls for (TC-TL-008 / TC-TL-009).
func TestTicketSM_ReopenIncrementsEscalation(t *testing.T) {
	by := uuid.New()
	tk := newTicketAt(t, TicketStatusResolved)

	if tk.EscalationLevel != 0 {
		t.Fatalf("initial escalation_level should be 0, got %d", tk.EscalationLevel)
	}
	if err := tk.Reopen(by, "still broken"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if tk.EscalationLevel != 1 {
		t.Fatalf("escalation_level after first reopen = %d, want 1", tk.EscalationLevel)
	}
	if err := tk.Resolve(by, "fixed again"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := tk.Reopen(by, "still broken"); err != nil {
		t.Fatalf("Reopen #2: %v", err)
	}
	if tk.EscalationLevel != 2 {
		t.Fatalf("escalation_level after second reopen = %d, want 2", tk.EscalationLevel)
	}
}

// TestTicketSM_EffectiveAgeNetsOutPause covers the SLA-on-pause primitive
// Wave 124 will use.
func TestTicketSM_EffectiveAgeNetsOutPause(t *testing.T) {
	tk := newTicketAt(t, TicketStatusOpen)
	// pretend the ticket was opened 60s ago
	tk.CreatedAt = time.Now().UTC().Add(-60 * time.Second)
	tk.PauseSeconds = 15

	got := tk.EffectiveAge(time.Now().UTC())
	if got < 44*time.Second || got > 46*time.Second {
		t.Fatalf("EffectiveAge ~ 45s expected, got %v", got)
	}
}

// TestTicketSM_ChangePriority guards the non-closed gate.
func TestTicketSM_ChangePriority(t *testing.T) {
	tk := newTicketAt(t, TicketStatusInProgress)
	if err := tk.ChangePriority(PriorityUrgent); err != nil {
		t.Fatalf("ChangePriority on in_progress: %v", err)
	}
	if tk.Priority != PriorityUrgent {
		t.Fatalf("priority = %v want urgent", tk.Priority)
	}

	tk2 := newTicketAt(t, TicketStatusClosed)
	if err := tk2.ChangePriority(PriorityUrgent); err == nil {
		t.Fatal("expected error on closed ticket priority change")
	}
}

// TestTicketSM_AssignIdempotent verifies that re-assigning the same
// user is a no-op rather than an error.
func TestTicketSM_AssignIdempotent(t *testing.T) {
	tk := newTicketAt(t, TicketStatusOpen)
	u := uuid.New()
	if err := tk.Assign(u); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := tk.Assign(u); err != nil {
		t.Fatalf("Assign idempotent: %v", err)
	}
	// reassigning to a different user from the assigned state should succeed
	other := uuid.New()
	if err := tk.Assign(other); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	if tk.AssignedUserID == nil || *tk.AssignedUserID != other {
		t.Fatalf("assignee not updated to %v", other)
	}
}

// =====================================================================
// helpers
// =====================================================================

type ticketArgs struct {
	customerID  uuid.UUID
	openedBy    uuid.UUID
	openedVia   OpenedVia
	ticketType  TicketType
	title       string
	description string
	priority    Priority
}

func defaultTicketArgs() ticketArgs {
	return ticketArgs{
		customerID:  uuid.New(),
		openedBy:    uuid.New(),
		openedVia:   OpenedViaPortal,
		ticketType:  TicketTypeTechnical,
		title:       "title",
		description: "desc",
		priority:    PriorityNormal,
	}
}

func errCodeOf(err error) string {
	if e := derrors.As(err); e != nil {
		return e.Code
	}
	return ""
}
