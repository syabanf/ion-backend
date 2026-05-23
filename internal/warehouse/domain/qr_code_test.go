package domain

import (
	"testing"
)

func TestGenerateQR_Deterministic(t *testing.T) {
	src := NewQRSource(ItemTypeSerialized, "abcdef01-2345-6789-0000-000000000000", "SN-123")
	q1 := GenerateQR(src)
	q2 := GenerateQR(src)
	if q1 != q2 {
		t.Fatalf("expected deterministic QR, got %q vs %q", q1, q2)
	}
}

func TestGenerateQR_FormatShape(t *testing.T) {
	src := NewQRSource(ItemTypeSerialized, "abcdef01", "SN-123")
	q := GenerateQR(src)
	parsed, err := ParseQR(q)
	if err != nil {
		t.Fatalf("expected to parse generated QR, got %v", err)
	}
	if parsed.ItemType != ItemTypeSerialized {
		t.Fatalf("expected type1, got %s", parsed.ItemType)
	}
	if parsed.ItemID != "abcdef01" {
		t.Fatalf("expected abcdef01, got %s", parsed.ItemID)
	}
	if len(parsed.SerialHash) != 12 {
		t.Fatalf("expected 12-char hash, got %d", len(parsed.SerialHash))
	}
}

func TestParseQR_BadInputs(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"ION-foo-bar-baz",          // bad type
		"ION-type1-short-baz",      // bad id length
		"ION-type1-abcdef01-short", // bad hash length
		"ION-type1-abcdef01",       // missing hash
	}
	for _, c := range cases {
		if _, err := ParseQR(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestGenerateQR_RoundTrip_AllTypes(t *testing.T) {
	for _, tt := range []ItemType{ItemTypeSerialized, ItemTypeCable, ItemTypeConsumable, ItemTypeNetworkInfra} {
		q := GenerateQR(NewQRSource(tt, "abcdef01", "SN-X"))
		p, err := ParseQR(q)
		if err != nil {
			t.Fatalf("round-trip failed for %s: %v", tt, err)
		}
		if p.ItemType != tt {
			t.Fatalf("type mismatch: in=%s out=%s", tt, p.ItemType)
		}
	}
}
