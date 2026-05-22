// schema_policy.go — bridge between the platform Schema System v1 and
// the legacy billing.policies row.
//
// The whole point of Schema System v1 is to let ops version + per-
// customer-override the rules that the billing tick reads at runtime.
// Before this file, RunBillingTick read a single hardcoded
// billing.policies row for every customer; commission splits came from
// constants in billing/domain/r2.go. This file is the single
// integration point so callers in r2.go / r3.go don't have to know
// whether the values came from a schema body or the legacy table.
//
// Resolution order for any tick value:
//
//  1. Resolve the schema (kind=billing|commission|suspension) for the
//     customer in scope (or the DEFAULT-coded published schema when
//     no customer is in scope).
//  2. Read the requested key from the resolved body.
//  3. On any of {resolver not wired, NotFound, missing key, type
//     mismatch}: fall back to the corresponding billing.policies field
//     (or the hardcoded DefaultCommissionPercents map).
//
// Step 3 keeps existing customers working even when no schemas are
// seeded; the WARN log on first fallback is the operator-facing
// signal that the Schema System path is dark.
package usecase

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	platformport "github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// schemaPolicyResolver wraps the SchemaResolver with billing-shaped
// accessors. The resolver may be nil — every method is nil-safe and
// just returns the legacy Policy struct in that case. This is the
// invariant the rest of the package leans on.
type schemaPolicyResolver struct {
	resolver platformport.SchemaResolver
	log      *slog.Logger

	// fallback tracking — log exactly once per kind so a misseeded
	// schema doesn't spam the logs every 30-minute tick.
	once     sync.Map // map[string]struct{} keyed by "kind:reason"
}

func newSchemaPolicyResolver(r platformport.SchemaResolver, log *slog.Logger) *schemaPolicyResolver {
	return &schemaPolicyResolver{resolver: r, log: log}
}

// loadBilling fetches the billing schema body for the customer (or
// DEFAULT when customerID is uuid.Nil). Returns nil + nil on any
// fallback path so callers can branch on `if body != nil`.
func (sp *schemaPolicyResolver) loadBilling(ctx context.Context, customerID uuid.UUID) (map[string]any, *uuid.UUID) {
	return sp.load(ctx, customerID, "billing")
}

func (sp *schemaPolicyResolver) loadCommission(ctx context.Context, customerID uuid.UUID) (map[string]any, *uuid.UUID) {
	return sp.load(ctx, customerID, "commission")
}

func (sp *schemaPolicyResolver) loadSuspension(ctx context.Context, customerID uuid.UUID) (map[string]any, *uuid.UUID) {
	return sp.load(ctx, customerID, "suspension")
}

// load is the internal accessor — returns (body, _, error) reduced to
// (body, nil) on fallback paths so callers don't have to error-handle
// the (expected) NotFound path.
//
// The second return is reserved for future schema_id audit logging
// (we currently can't surface it because port.SchemaResolver returns
// the resolved body only — we'd need a richer return shape to thread
// the schema_id through. Tracked separately).
func (sp *schemaPolicyResolver) load(ctx context.Context, customerID uuid.UUID, kind string) (map[string]any, *uuid.UUID) {
	if sp == nil || sp.resolver == nil {
		sp.warnOnce(kind+":not_wired", "schema resolver not wired; falling back to billing.policies", kind)
		return nil, nil
	}
	body, err := sp.resolver.Resolve(ctx, customerID, kind)
	if err != nil {
		if derrors.IsNotFound(err) {
			sp.warnOnce(kind+":not_found", "no schema published for kind; falling back to billing.policies", kind)
			return nil, nil
		}
		sp.warnOnce(kind+":error", "schema resolver error; falling back to billing.policies", kind, "err", err)
		return nil, nil
	}
	if len(body) == 0 {
		sp.warnOnce(kind+":empty", "schema body empty; falling back to billing.policies", kind)
		return nil, nil
	}
	return body, nil
}

// warnOnce logs the message exactly once per (key) for the lifetime
// of this process. The variadic args trail the standard structured
// fields so the kind is always logged.
func (sp *schemaPolicyResolver) warnOnce(key, msg, kind string, kv ...any) {
	if sp == nil || sp.log == nil {
		return
	}
	if _, loaded := sp.once.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	fields := append([]any{"kind", kind}, kv...)
	sp.log.Warn(msg, fields...)
}

// =====================================================================
// Typed accessors — pull individual keys with type-checked fallback.
// =====================================================================

// readInt returns the integer at key from body, or fallback if the
// key is missing / not a number. JSON numbers in Go land as float64
// by default; we accept both float and int representations.
func readInt(body map[string]any, key string, fallback int) int {
	if body == nil {
		return fallback
	}
	v, ok := body[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		// Defensive — encoding/json by default uses float64, but
		// callers that switch to UseNumber would hit this branch.
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
		f, err := n.Float64()
		if err == nil {
			return int(f)
		}
	}
	return fallback
}

// readFloat returns body[key] as float64 with fallback.
func readFloat(body map[string]any, key string, fallback float64) float64 {
	if body == nil {
		return fallback
	}
	v, ok := body[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err == nil {
			return f
		}
	}
	return fallback
}

// =====================================================================
// Policy-shaped accessor — preserves the existing call sites.
// =====================================================================

// resolvedPolicyFor returns a *domain.Policy reflecting the resolved
// billing schema for the customer, with any missing keys filled in
// from the legacy policy. This keeps r2.go / r3.go unchanged — they
// continue to read .LateFeeGraceDays / .SuspendAfterDays / etc.
//
// customerID == uuid.Nil resolves the DEFAULT schema (used by the
// global late-fee / restoration passes that iterate over all
// customers and need a single policy view per tick).
func (sp *schemaPolicyResolver) resolvedPolicyFor(
	ctx context.Context, customerID uuid.UUID, legacy *domain.Policy,
) *domain.Policy {
	body, _ := sp.loadBilling(ctx, customerID)
	if body == nil {
		return legacy
	}
	// Start from the legacy values so any missing schema key falls
	// back transparently.
	out := *legacy
	// The schema uses two name shapes:
	//   * PRD-shape: grace_days / suspend_after_days
	//   * Legacy-shape: late_fee_grace_days / suspend_after_days /
	//     terminate_after_suspended_days / late_fee_amount /
	//     notify_customer_days_before
	// We prefer the legacy-shape when present (drop-in) and fall back
	// to the PRD-shape for the new keys.
	out.LateFeeGraceDays = readInt(body, "late_fee_grace_days",
		readInt(body, "grace_days", legacy.LateFeeGraceDays))
	out.LateFeeAmount = readFloat(body, "late_fee_amount", legacy.LateFeeAmount)
	out.SuspendAfterDays = readInt(body, "suspend_after_days", legacy.SuspendAfterDays)
	out.TerminateAfterSuspendedDays = readInt(body, "terminate_after_suspended_days",
		legacy.TerminateAfterSuspendedDays)
	out.NotifyCustomerDaysBefore = readInt(body, "notify_customer_days_before",
		legacy.NotifyCustomerDaysBefore)
	return &out
}

// =====================================================================
// Commission resolver — fetches the 5-party split for an order's
// customer, with the same legacy fallback.
// =====================================================================

// commissionSplit is the resolved 5-party percentage split (in
// percent-shape, summing to ~100). The runtime accepts schema values
// in either fraction (0.0–1.0) or percent (>1) shapes — fractions
// get multiplied up so downstream math stays simple.
type commissionSplit struct {
	RepPct         float64
	MgrPct         float64
	BranchSalesPct float64
	BranchInfraPct float64
	HoldingPct     float64
}

// resolvedCommissionFor returns the commission split for an order's
// customer. customerID may be uuid.Nil — in that case it falls
// through to the DEFAULT-coded schema.
func (sp *schemaPolicyResolver) resolvedCommissionFor(
	ctx context.Context, customerID uuid.UUID,
) commissionSplit {
	// Legacy default mirrors domain.DefaultCommissionPercents.
	legacy := commissionSplit{
		RepPct:         domain.DefaultCommissionPercents[domain.PartySalesPerson],
		MgrPct:         domain.DefaultCommissionPercents[domain.PartySalesManager],
		BranchSalesPct: domain.DefaultCommissionPercents[domain.PartySalesBranch],
		BranchInfraPct: domain.DefaultCommissionPercents[domain.PartyInfrastructureBranch],
		HoldingPct:     domain.DefaultCommissionPercents[domain.PartyCompany],
	}
	body, _ := sp.loadCommission(ctx, customerID)
	if body == nil {
		return legacy
	}
	out := commissionSplit{
		RepPct:         readFloat(body, "rep_pct", legacy.RepPct),
		MgrPct:         readFloat(body, "mgr_pct", legacy.MgrPct),
		BranchSalesPct: readFloat(body, "branch_sales_pct", legacy.BranchSalesPct),
		BranchInfraPct: readFloat(body, "branch_infra_pct", legacy.BranchInfraPct),
		HoldingPct:     readFloat(body, "holding_pct", legacy.HoldingPct),
	}
	// Schema convention is fractions (0.15 = 15%). Detect by sum: if
	// the total is ≤ 1.5 the body is fraction-shape — scale to
	// percent. Otherwise treat as percent already.
	total := out.RepPct + out.MgrPct + out.BranchSalesPct + out.BranchInfraPct + out.HoldingPct
	if total > 0 && total <= 1.5 {
		out.RepPct *= 100
		out.MgrPct *= 100
		out.BranchSalesPct *= 100
		out.BranchInfraPct *= 100
		out.HoldingPct *= 100
	}
	return out
}
