package domain

import (
	"time"

	"github.com/google/uuid"
)

// PlatformConfig is a single key/value setting from identity.platform_config.
//
// Values are typed by the *reader* — the DB column is text, and consumers
// parse to int / bool / duration / JSON as needed. That tradeoff keeps the
// admin UI uniform (just text fields) and avoids per-key schema migrations
// when we add a new config.
type PlatformConfig struct {
	ID        uuid.UUID
	Key       string
	Value     string
	UpdatedBy *uuid.UUID
	UpdatedAt time.Time
}
