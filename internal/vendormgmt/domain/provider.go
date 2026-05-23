// Package domain holds the vendor bounded context's entities and
// value objects.
//
// Rules (same as identity / crm / warehouse / enterprise / reseller):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map
//     them to the right HTTP status without inspecting strings.
//
// Wave 107 scope: Provider, ProviderCapability, InputSubmission,
// DailyMetric. The four aggregates together cover the Provider &
// Vendor Input + provider-metrics TC families called out in the Wave
// 91 audit.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ProviderStatus is the lifecycle state of a provider in the registry.
//
// Lifecycle:
//
//	pending → active (requires KYC complete)
//	active  ↔ suspended (with reason)
//	any non-terminal → blacklisted (terminal)
//
// The DB enforces the enum via CHECK; the domain enforces legal
// transitions in the methods below. A blacklisted provider cannot be
// reactivated — the operator must onboard a fresh row.
type ProviderStatus string

const (
	ProviderStatusPending     ProviderStatus = "pending"
	ProviderStatusActive      ProviderStatus = "active"
	ProviderStatusSuspended   ProviderStatus = "suspended"
	ProviderStatusBlacklisted ProviderStatus = "blacklisted"
)

// Provider is one row in the vendor registry. Capabilities is the
// jsonb mirror of vendor.provider_capabilities — kept on the header
// for cheap "what can this provider do?" reads.
//
// RatingScore / TotalCompletedJobs / TotalRevenue are denormalised
// aggregates: the daily metrics deriver + the IC-PO-accept hook in
// enterprise both write here.
type Provider struct {
	ID                 uuid.UUID
	Name               string
	NPWP               string
	ContactEmail       string
	ContactPhone       string
	Status             ProviderStatus
	KYCCompleted       bool
	Capabilities       []string // mirror of the jsonb column
	RatingScore        float64
	TotalCompletedJobs int
	TotalRevenue       float64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	SuspendedAt        *time.Time
	SuspendedReason    string
}

// NewProvider constructs a pending provider. The default state mirrors
// the reseller flow: nothing transacts until KYC clears + an operator
// flips Activate(). Capabilities default to an empty slice (not nil)
// so JSON serialisation produces `[]` not `null`.
func NewProvider(name, npwp, contactEmail, contactPhone string) (*Provider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.Validation("provider.name_required", "name is required")
	}
	now := time.Now().UTC()
	return &Provider{
		ID:           uuid.New(),
		Name:         name,
		NPWP:         strings.TrimSpace(npwp),
		ContactEmail: strings.TrimSpace(contactEmail),
		ContactPhone: strings.TrimSpace(contactPhone),
		Status:       ProviderStatusPending,
		Capabilities: []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// CompleteKYC flips kyc_completed → true. Idempotent — calling twice is
// safe. Does NOT transition state (that's Activate's job); a provider
// can stay pending after KYC if no operator has activated yet.
func (p *Provider) CompleteKYC() {
	if p.KYCCompleted {
		return
	}
	p.KYCCompleted = true
	p.UpdatedAt = time.Now().UTC()
}

// Activate moves pending → active. Requires KYC complete. Idempotent
// on already-active; rejects suspended (use Reactivate) and the
// terminal blacklisted state.
func (p *Provider) Activate() error {
	if p.Status == ProviderStatusActive {
		return nil
	}
	if p.Status != ProviderStatusPending {
		return errors.Conflict(
			"provider.cannot_activate",
			"only pending providers can be activated — use Reactivate for suspended rows",
		)
	}
	if !p.KYCCompleted {
		return errors.Validation(
			"provider.kyc_required",
			"KYC must be completed before activation",
		)
	}
	p.Status = ProviderStatusActive
	p.SuspendedAt = nil
	p.SuspendedReason = ""
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// Suspend pauses an active provider. Reason is required so the
// provider portal can show a human-readable explanation rather than
// just "access denied". Idempotent on already-suspended (refreshes
// the reason); rejects pending and the terminal blacklisted state.
func (p *Provider) Suspend(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("provider.suspend_reason_required", "reason is required")
	}
	if p.Status == ProviderStatusSuspended {
		// Refresh the reason — operator can update the suspension note
		// without flipping the state machine.
		p.SuspendedReason = reason
		p.UpdatedAt = time.Now().UTC()
		return nil
	}
	if p.Status != ProviderStatusActive {
		return errors.Conflict(
			"provider.cannot_suspend",
			"only active providers can be suspended",
		)
	}
	now := time.Now().UTC()
	p.Status = ProviderStatusSuspended
	p.SuspendedAt = &now
	p.SuspendedReason = reason
	p.UpdatedAt = now
	return nil
}

// Reactivate moves suspended → active. Clears the suspension note +
// timestamp; rejects everything else.
func (p *Provider) Reactivate() error {
	if p.Status == ProviderStatusActive {
		return nil
	}
	if p.Status != ProviderStatusSuspended {
		return errors.Conflict(
			"provider.cannot_reactivate",
			"only suspended providers can be reactivated",
		)
	}
	p.Status = ProviderStatusActive
	p.SuspendedAt = nil
	p.SuspendedReason = ""
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// Blacklist is the irreversible exit. Allowed from any non-terminal
// state; idempotent on already-blacklisted (refreshes the reason).
// Reason is required so the audit row is informative.
func (p *Provider) Blacklist(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("provider.blacklist_reason_required", "reason is required")
	}
	if p.Status == ProviderStatusBlacklisted {
		p.SuspendedReason = reason
		p.UpdatedAt = time.Now().UTC()
		return nil
	}
	p.Status = ProviderStatusBlacklisted
	p.SuspendedReason = reason
	now := time.Now().UTC()
	p.SuspendedAt = &now
	p.UpdatedAt = now
	return nil
}

// UpdateRating bulk-applies a fresh score + lifetime counters. Called
// by the metrics deriver cron + the IC-PO-accept hook in enterprise.
// We accept the score as-is (no clamping) so the caller can decide
// whether 0..5 or 0..10 is the canonical range; the DB column is
// numeric(3,2) so values ≥ 10 will be rejected at the storage layer.
func (p *Provider) UpdateRating(score float64, jobsCompleted int, revenue float64) {
	p.RatingScore = score
	p.TotalCompletedJobs = jobsCompleted
	p.TotalRevenue = revenue
	p.UpdatedAt = time.Now().UTC()
}

// AddCompletedJob is the cross-context entry point used by the
// enterprise IC-PO-accept hook. Increments the job + revenue counters
// without touching the rating (which is recomputed by the metrics
// deriver on its own cadence).
func (p *Provider) AddCompletedJob(revenue float64) {
	p.TotalCompletedJobs++
	p.TotalRevenue += revenue
	p.UpdatedAt = time.Now().UTC()
}

// IsOperational reports whether the provider can be picked + booked.
// Suspended + blacklisted rows can't transact; pending rows can be
// listed in the picker but the BOQ flow refuses to assign them.
func (p *Provider) IsOperational() bool {
	return p.Status == ProviderStatusActive
}
