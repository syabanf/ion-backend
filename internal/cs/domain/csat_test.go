package domain

import (
	"testing"

	"github.com/google/uuid"
)

// =====================================================================
// Wave 124 — CSAT response tests (TC-CSAT-*).
// =====================================================================

func TestCSAT_ValidRating(t *testing.T) {
	c, err := NewCSATResponse(uuid.New(), uuid.New(), 4, "good experience", CSATChannelEmail)
	if err != nil {
		t.Fatalf("NewCSATResponse: %v", err)
	}
	if c.Rating != 4 {
		t.Fatalf("rating not preserved")
	}
	if c.Channel != CSATChannelEmail {
		t.Fatalf("channel not preserved")
	}
	if c.RespondedAt == nil {
		t.Fatalf("RespondedAt should be stamped")
	}
}

func TestCSAT_RatingOutOfRange(t *testing.T) {
	for _, bad := range []int{-1, 0, 6, 100} {
		if _, err := NewCSATResponse(uuid.New(), uuid.New(), bad, "", CSATChannelEmail); err == nil {
			t.Fatalf("expected validation for rating=%d", bad)
		}
	}
}

func TestCSAT_IsCriticalLow(t *testing.T) {
	for _, c := range []struct {
		r        int
		critical bool
	}{
		{1, true}, {2, true},
		{3, false}, {4, false}, {5, false},
		{0, false}, {6, false}, // invalid ratings are not "critical" — they're invalid
	} {
		if got := IsCriticalLow(c.r); got != c.critical {
			t.Fatalf("IsCriticalLow(%d) = %v, want %v", c.r, got, c.critical)
		}
	}
}
