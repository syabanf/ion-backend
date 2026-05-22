package postgres

import (
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/auth"
)

// BcryptHasher implements port.PasswordHasher using pkg/auth.
// Lives in the postgres adapter package only because it's wired alongside
// the repo at startup — feel free to move to its own adapter folder if it
// grows beyond a thin shim.
type BcryptHasher struct{}

func NewBcryptHasher() *BcryptHasher { return &BcryptHasher{} }

var _ port.PasswordHasher = (*BcryptHasher)(nil)

func (BcryptHasher) Hash(plain string) (string, error)         { return auth.HashPassword(plain) }
func (BcryptHasher) Compare(hash, plain string) error          { return auth.ComparePassword(hash, plain) }
