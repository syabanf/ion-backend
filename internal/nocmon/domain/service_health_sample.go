package domain

import (
	"time"

	"github.com/google/uuid"
)

// HealthSample is one row on service_health_samples — append-only.
// Status is computed by ServiceProbe.Evaluate at insert time so the
// dashboard can render a per-row chip without recomputing.
//
// Idempotency: the DB carries a UNIQUE (probe_id, sampled_at) so a
// re-emit from the cron runner (e.g. after a crash) inserts at most
// once. The repo translates that conflict to a no-op rather than an
// error.
type HealthSample struct {
	ID        uuid.UUID
	ProbeID   uuid.UUID
	SampledAt time.Time
	Value     *float64
	Status    SampleStatus
}
