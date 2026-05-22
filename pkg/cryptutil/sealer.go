// Package cryptutil provides reversible field-level encryption for
// at-rest data that must round-trip back to plaintext (KTP NIK, KK
// number, anything that needs display + exact-match lookup later).
//
// Round-3 uses AES-256-GCM with a per-process key loaded from env at
// startup. The ciphertext layout is `nonce(12B) || tag(16B) || data`,
// which is the standard Go AEAD encoding; new key versions can prefix
// with a key id in a future round (we have no rotation requirement
// yet).
//
// The sealer keeps the key in memory. Callers should not log
// ciphertext alongside identifiers that could leak the plaintext via
// confused-deputy attacks (e.g. logging both `customer_id` and the
// encrypted NIK in the same line is fine; logging both the encrypted
// NIK and a non-encrypted "customer name + birthdate" is the kind of
// pairing to avoid). Defensive logging policy lives in the call site.
package cryptutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Sealer wraps an AEAD cipher with a sealed key. Construct once at
// startup; safe for concurrent use.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer constructs a Sealer from a hex-encoded key.
// AES-256 requires 32 bytes (= 64 hex chars). We refuse shorter keys
// rather than silently downgrading to AES-128.
func NewSealer(keyHex string) (*Sealer, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("cryptutil: key not valid hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("cryptutil: key must be 32 bytes (got %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptutil: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptutil: gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts `plain` and returns `nonce || ciphertext-with-tag`.
// Empty plaintext is allowed and returns a non-empty result (the
// nonce + auth tag still occupy space) — callers may want to treat
// empty plaintext as "store NULL" themselves rather than seal.
func (s *Sealer) Seal(plain string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("cryptutil: nil sealer")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("cryptutil: nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, []byte(plain), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open decrypts `nonce || ciphertext-with-tag`. Returns an error when
// the input is truncated or the tag doesn't verify (key mismatch,
// bit-flip, attempted forgery).
func (s *Sealer) Open(sealed []byte) (string, error) {
	if s == nil {
		return "", errors.New("cryptutil: nil sealer")
	}
	ns := s.aead.NonceSize()
	if len(sealed) < ns+s.aead.Overhead() {
		return "", errors.New("cryptutil: sealed payload too short")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("cryptutil: open: %w", err)
	}
	return string(plain), nil
}

// GenerateKey is a helper for ops to mint a fresh hex key. Only used
// from a side cmd, never on the request path.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}
