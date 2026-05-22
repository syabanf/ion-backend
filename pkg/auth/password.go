package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash suitable for storage.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ComparePassword returns nil on match, or an error otherwise.
// Callers should not distinguish between "wrong password" and other errors
// in their HTTP response, to avoid user enumeration.
func ComparePassword(hash, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrInvalidCredentials
	}
	return err
}

var ErrInvalidCredentials = errors.New("auth: invalid credentials")
