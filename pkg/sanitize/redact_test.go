package sanitize

import "testing"

func TestNIK(t *testing.T) {
	cases := []struct{ in, want string }{
		{"3174012345678901", "317401******8901"},
		{"", ""},
		{"short", "short"},
		{"12345678901", "12345678901"},             // 11 chars — below min
		{"123456789012", "123456**9012"},           // 12 chars — borderline maskable
		{"31740123456789012345", "317401**********2345"}, // longer than 16 still works
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := NIK(tc.in)
			if got != tc.want {
				t.Fatalf("NIK(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
