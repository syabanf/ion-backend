package cryptutil

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	cases := []string{
		"",
		"3174000000000001",
		"unicode: 日本人 indonesia",
		"  whitespace edges  ",
	}
	for _, plain := range cases {
		t.Run(plain, func(t *testing.T) {
			sealed, err := s.Seal(plain)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			got, err := s.Open(sealed)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got != plain {
				t.Fatalf("round-trip mismatch: want %q got %q", plain, got)
			}
		})
	}
}

func TestSealNonDeterministic(t *testing.T) {
	// Two seals of the same plaintext must produce distinct ciphertexts
	// (different nonces), otherwise an observer can deduce equality
	// between rows on disk.
	key, _ := GenerateKey()
	s, _ := NewSealer(key)
	a, _ := s.Seal("same")
	b, _ := s.Seal("same")
	if string(a) == string(b) {
		t.Fatal("seal is deterministic — nonce reuse")
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	key, _ := GenerateKey()
	s, _ := NewSealer(key)
	sealed, _ := s.Seal("hello")
	// Flip the last byte (auth tag region).
	sealed[len(sealed)-1] ^= 0xFF
	if _, err := s.Open(sealed); err == nil {
		t.Fatal("expected open to reject tampered ciphertext")
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer("not-hex"); err == nil {
		t.Error("expected non-hex key to fail")
	}
	if _, err := NewSealer("aabbccdd"); err == nil {
		t.Error("expected 4-byte key to fail")
	}
}
