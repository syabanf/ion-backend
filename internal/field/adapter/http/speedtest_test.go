package http

import (
	"testing"
)

// TestParseSpeedtestText covers the pipe-encoded payload the tech app
// writes into wo_checklist_responses.response_text. The parser is pure
// (no DB), so we exercise every branch here:
//
//   - all three fields present
//   - subset (e.g. ping captured but upload dropped)
//   - aliases (down / down_mbps / download)
//   - whitespace
//   - malformed segments (silently skipped, not fatal)
//   - empty input (returns ok=false)
func TestParseSpeedtestText(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantOk    bool
		wantDown  *float64
		wantUp    *float64
		wantPing  *float64
	}{
		{
			name:     "happy path",
			in:       "down=85.2|up=42.7|ping_ms=12",
			wantOk:   true,
			wantDown: f64(85.2),
			wantUp:   f64(42.7),
			wantPing: f64(12),
		},
		{
			name:     "aliases",
			in:       "download=100|upload=50|latency_ms=8",
			wantOk:   true,
			wantDown: f64(100),
			wantUp:   f64(50),
			wantPing: f64(8),
		},
		{
			name:     "down_mbps + up_mbps + ping",
			in:       "down_mbps=33.3|up_mbps=11.1|ping=20",
			wantOk:   true,
			wantDown: f64(33.3),
			wantUp:   f64(11.1),
			wantPing: f64(20),
		},
		{
			name:     "partial — only ping",
			in:       "ping_ms=5",
			wantOk:   true,
			wantPing: f64(5),
		},
		{
			name:     "whitespace around segments",
			in:       "  down = 80 | up=40 | ping_ms = 15  ",
			wantOk:   true,
			wantDown: f64(80),
			wantUp:   f64(40),
			wantPing: f64(15),
		},
		{
			name:     "garbage segments are skipped",
			in:       "down=70|wat|=12|up=garbage|ping_ms=9",
			wantOk:   true,
			wantDown: f64(70),
			wantPing: f64(9),
			// up=garbage is skipped (ParseFloat fails) — no UpMbps.
		},
		{
			name:   "empty string → not ok",
			in:     "",
			wantOk: false,
		},
		{
			name:   "all garbage → not ok",
			in:     "hello|world|foo=bar",
			wantOk: false,
		},
		{
			name:     "case-insensitive keys",
			in:       "DOWN=99|UP=49|PING_MS=7",
			wantOk:   true,
			wantDown: f64(99),
			wantUp:   f64(49),
			wantPing: f64(7),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSpeedtestText(tc.in)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOk)
			}
			if !sameOptF(got.DownMbps, tc.wantDown) {
				t.Errorf("DownMbps = %v, want %v", deref(got.DownMbps), deref(tc.wantDown))
			}
			if !sameOptF(got.UpMbps, tc.wantUp) {
				t.Errorf("UpMbps = %v, want %v", deref(got.UpMbps), deref(tc.wantUp))
			}
			if !sameOptF(got.PingMs, tc.wantPing) {
				t.Errorf("PingMs = %v, want %v", deref(got.PingMs), deref(tc.wantPing))
			}
			if got.Raw != tc.in {
				t.Errorf("Raw not preserved: got %q, want %q", got.Raw, tc.in)
			}
		})
	}
}

func f64(v float64) *float64 { return &v }

func sameOptF(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func deref(p *float64) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}
