package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// Wave 124 — Team + TeamMember tests (TC-TA-*).
// =====================================================================

func TestTeam_Construct(t *testing.T) {
	mgr := uuid.New()
	tm, err := NewTeam("tech-support-1", "L1 desk", &mgr, []TicketType{TicketTypeTechnical})
	if err != nil {
		t.Fatalf("NewTeam: %v", err)
	}
	if tm.Name != "tech-support-1" {
		t.Fatalf("name not preserved")
	}
	if !tm.IsActive {
		t.Fatalf("expected active by default")
	}
	if !tm.CanReceiveTicketType(TicketTypeTechnical) {
		t.Fatalf("focus type technical should be accepted")
	}
	if tm.CanReceiveTicketType(TicketTypeBilling) {
		t.Fatalf("focus type billing should be rejected")
	}
}

func TestTeam_EmptyFocusAcceptsAny(t *testing.T) {
	tm, err := NewTeam("general", "any", nil, nil)
	if err != nil {
		t.Fatalf("NewTeam: %v", err)
	}
	for _, tt := range []TicketType{TicketTypeTechnical, TicketTypeBilling, TicketTypeComplaint} {
		if !tm.CanReceiveTicketType(tt) {
			t.Fatalf("empty focus should accept %s", tt)
		}
	}
}

func TestTeam_InactiveRefuses(t *testing.T) {
	tm, _ := NewTeam("inactive", "", nil, nil)
	tm.IsActive = false
	if tm.CanReceiveTicketType(TicketTypeTechnical) {
		t.Fatalf("inactive team should refuse all types")
	}
}

func TestTeam_NameRequired(t *testing.T) {
	if _, err := NewTeam("", "", nil, nil); err == nil {
		t.Fatalf("expected name-required validation error")
	}
}

func TestTeamMember_LeaveAndPromote(t *testing.T) {
	m, err := NewTeamMember(uuid.New(), uuid.New(), TeamRoleAgent)
	if err != nil {
		t.Fatalf("NewTeamMember: %v", err)
	}
	if !m.IsActive() {
		t.Fatalf("new member should be active")
	}
	if err := m.PromoteTo(TeamRoleLead); err != nil {
		t.Fatalf("PromoteTo lead: %v", err)
	}
	if m.RoleInTeam != TeamRoleLead {
		t.Fatalf("role not updated")
	}
	m.Leave(time.Now().UTC())
	if m.IsActive() {
		t.Fatalf("member should be inactive after leave")
	}
	// Promote after leave should error.
	if err := m.PromoteTo(TeamRoleManager); err == nil {
		t.Fatalf("expected conflict promoting a left member")
	}
}
