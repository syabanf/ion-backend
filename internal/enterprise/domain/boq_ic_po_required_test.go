package domain

import (
	"testing"

	"github.com/google/uuid"
)

// TestBOQ_LineICPORequired — Wave 106 TC-BQ-013.
//
// `ic_po_required` is true when the line's assigned_provider_company_id
// differs from the BOQ's commercial_owner_subsidiary_id. We probe each
// of the nil-safe fallback paths so legacy BOQs without the column
// don't accidentally surface the flag.
func TestBOQ_LineICPORequired(t *testing.T) {
	subA := uuid.New()
	subB := uuid.New()
	tests := []struct {
		name  string
		owner *uuid.UUID
		prov  *uuid.UUID
		want  bool
	}{
		{"no owner set, no provider", nil, nil, false},
		{"no owner set, provider set", nil, &subA, false},
		{"owner set, no provider", &subA, nil, false},
		{"owner = provider (same subsidiary)", &subA, &subA, false},
		{"owner != provider (sister)", &subA, &subB, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BOQ{CommercialOwnerSubsidiaryID: tt.owner}
			l := &BOQLine{AssignedProviderCompanyID: tt.prov}
			got := b.LineICPORequired(l)
			if got != tt.want {
				t.Errorf("LineICPORequired = %v, want %v", got, tt.want)
			}
		})
	}
	// Nil-safety: passing nil BOQ or nil line should not panic + must
	// return false.
	var nilBOQ *BOQ
	if nilBOQ.LineICPORequired(&BOQLine{}) {
		t.Errorf("nil BOQ should return false")
	}
	b := &BOQ{CommercialOwnerSubsidiaryID: &subA}
	if b.LineICPORequired(nil) {
		t.Errorf("nil line should return false")
	}
}
