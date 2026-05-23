// Package port — vendor metrics updater seam.
//
// Wave 107 introduces the vendor bounded context's provider registry.
// When an IntercompanyPO is accepted, we need to increment the
// provider's lifetime job + revenue counters. To avoid cross-importing
// internal/vendormgmt from internal/enterprise (which would couple the
// two bounded contexts together at compile time), enterprise declares a
// narrow seam here and main.go wires the vendor postgres repo as the
// implementation. The vendor schema may not exist in legacy deployments
// — in that case main.go passes nil and the enterprise hook
// short-circuits without erroring.
package port

import (
	"context"

	"github.com/google/uuid"
)

// VendorMetricsUpdater is the narrow surface the enterprise IC-PO-accept
// hook calls into after a successful recognition. Implementations live
// in internal/vendormgmt/adapter/postgres/provider_repo.go (the
// IncrementCompletedJob method on *ProviderRepository).
//
// Best-effort: the hook logs failures + does NOT roll back the IC-PO
// accept. The metrics deriver cron's daily run will rebuild the rating
// from the metric_daily table on the next tick, so a transient miss
// here doesn't permanently desync.
type VendorMetricsUpdater interface {
	IncrementCompletedJob(ctx context.Context, providerID uuid.UUID, revenue float64) error
}
