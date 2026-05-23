package domain

import (
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// TestOpportunity_Reassign — Wave 106 TC-OP-011.
//
// Covers:
//   - happy: pending opportunity, new owner replaces previous one
//   - happy: nil-owner opportunity gets first owner
//   - same-owner reject (Validation)
//   - terminal stage Won rejects (Conflict)
//   - terminal stage Lost rejects (Conflict)
//   - new_owner = uuid.Nil rejects (Validation)
func TestOpportunity_Reassign(t *testing.T) {
	user1 := uuid.New()
	user2 := uuid.New()
	user3 := uuid.New()
	tests := []struct {
		name      string
		setup     func() *Opportunity
		newOwner  uuid.UUID
		wantErr   bool
		wantCode  string
		wantPrev  *uuid.UUID
	}{
		{
			name: "happy: replace existing owner",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				o.OwnerUserID = &user1
				return o
			},
			newOwner: user2,
			wantErr:  false,
			wantPrev: &user1,
		},
		{
			name: "happy: nil owner gets first assignment",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				return o
			},
			newOwner: user3,
			wantErr:  false,
			wantPrev: nil,
		},
		{
			name: "reject: same owner",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				o.OwnerUserID = &user1
				return o
			},
			newOwner: user1,
			wantErr:  true,
			wantCode: "opportunity.reassign_same_owner",
		},
		{
			name: "reject: terminal Won",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				o.Stage = OpportunityStageWon
				return o
			},
			newOwner: user1,
			wantErr:  true,
			wantCode: "opportunity.terminal",
		},
		{
			name: "reject: terminal Lost",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				o.Stage = OpportunityStageLost
				return o
			},
			newOwner: user1,
			wantErr:  true,
			wantCode: "opportunity.terminal",
		},
		{
			name: "reject: nil new_owner",
			setup: func() *Opportunity {
				o, _ := NewOpportunity("Acme Corp")
				return o
			},
			newOwner: uuid.Nil,
			wantErr:  true,
			wantCode: "opportunity.reassign_new_owner_required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := tt.setup()
			prev, err := o.Reassign(tt.newOwner)
			if tt.wantErr {
				de := derrors.As(err)
				if de == nil {
					t.Fatalf("want error %q, got nil", tt.wantCode)
				}
				if de.Code != tt.wantCode {
					t.Errorf("want code %q, got %q", tt.wantCode, de.Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil error, got %v", err)
			}
			if o.OwnerUserID == nil || *o.OwnerUserID != tt.newOwner {
				t.Errorf("OwnerUserID not updated to %v: got %v", tt.newOwner, o.OwnerUserID)
			}
			if (prev == nil) != (tt.wantPrev == nil) {
				t.Errorf("prev owner mismatch: want %v, got %v", tt.wantPrev, prev)
			}
			if prev != nil && tt.wantPrev != nil && *prev != *tt.wantPrev {
				t.Errorf("prev owner value: want %v, got %v", *tt.wantPrev, *prev)
			}
		})
	}
}
