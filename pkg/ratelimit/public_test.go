package ratelimit

import (
	"testing"
	"time"
)

func TestISecs(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{60 * time.Second, "60"},
		{time.Hour, "3600"},
		{500 * time.Millisecond, "1"}, // floor at 1
	}
	for _, c := range cases {
		got := iSecs(c.in)
		if got != c.want {
			t.Fatalf("iSecs(%v): want %q got %q", c.in, c.want, got)
		}
	}
}
