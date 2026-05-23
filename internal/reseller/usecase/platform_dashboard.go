package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
)

// DashboardService composes counts/sums from subscribers + invoices +
// compliance (cross-context, read-only) into one MTDDashboard.
//
// Compliance read-through: we depend on a port.ComplianceReader
// supplied at construction time. The adapter
// (internal/reseller/adapter/partnership/compliance_reader.go) issues
// a direct SQL query against partnership.compliance_evaluations.
// Failures degrade gracefully:
//   - NotFound  → compliance_status = "unknown" (no eval row yet)
//   - Unavailable → compliance_status = "unavailable" (migration not
//                   applied, connectivity issue, etc.)
// Anything else propagates so a hard infra failure isn't masked.
type DashboardService struct {
	subscribers port.SubscriberRepository
	invoices    port.SubscriberInvoiceRepository
	compliance  port.ComplianceReader
	tenantOf    func(ctx context.Context) uuid.UUID
}

func NewDashboardService(
	subs port.SubscriberRepository,
	invs port.SubscriberInvoiceRepository,
	compliance port.ComplianceReader,
	tenantOf func(ctx context.Context) uuid.UUID,
) *DashboardService {
	return &DashboardService{
		subscribers: subs,
		invoices:    invs,
		compliance:  compliance,
		tenantOf:    tenantOf,
	}
}

var _ port.DashboardUseCase = (*DashboardService)(nil)

func (s *DashboardService) guardTenant(ctx context.Context) (uuid.UUID, error) {
	tenant := s.tenantOf(ctx)
	if tenant == uuid.Nil {
		return uuid.Nil, errors.Unauthorized("session.missing", "tenant not resolved")
	}
	return tenant, nil
}

// MTD computes the dashboard payload "as of now". We pin a single
// `now` value through the call so the counts can't drift between
// queries (e.g. a subscriber added between Count() and List()
// shouldn't shift the result by one).
func (s *DashboardService) MTD(ctx context.Context) (*port.MTDDashboard, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	out := &port.MTDDashboard{
		ComplianceStatus: "unknown",
	}

	// --- subscriber counts ---
	totalAll, err := s.subscribers.Count(ctx, port.SubscriberListFilter{ResellerAccountID: tenant})
	if err != nil {
		return nil, err
	}
	out.SubscribersTotal = totalAll

	active, err := s.subscribers.Count(ctx, port.SubscriberListFilter{
		ResellerAccountID: tenant,
		Status:            "active",
	})
	if err != nil {
		return nil, err
	}
	out.SubscribersActive = active

	// MTD added: we list with the tenant filter then count rows whose
	// created_at >= monthStart. The repo doesn't expose a created_at
	// filter (Wave 102 keeps the surface minimal), so we walk the
	// paged list. Bounded to a large page size so the typical reseller
	// (≤500 subs) fits in one page.
	mtdAdded, mtdTerminated, err := s.countMTDDeltas(ctx, tenant, monthStart, now)
	if err != nil {
		return nil, err
	}
	out.SubscribersAddedMTD = mtdAdded
	out.SubscribersTerminatedMTD = mtdTerminated

	// --- invoice totals ---
	openTotal, err := s.invoices.SumOpen(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out.InvoicesOpenTotal = openTotal

	overdue, err := s.invoices.ListOverdueForReseller(ctx, tenant, now)
	if err != nil {
		return nil, err
	}
	for _, inv := range overdue {
		out.InvoicesOverdueTotal += inv.Amount
	}

	paidMTD, err := s.invoices.SumPaidMTD(ctx, tenant, monthStart, now)
	if err != nil {
		return nil, err
	}
	out.InvoicesPaidMTD = paidMTD

	// --- compliance read-through ---
	if s.compliance != nil {
		snap, err := s.compliance.LatestForReseller(ctx, tenant)
		switch {
		case err == nil:
			out.ComplianceStatus = translateComplianceStatus(snap.Status)
			out.ComplianceAchievedPct = snap.AchievedPct
			out.ComplianceThresholdPct = snap.ThresholdPct
			ev := snap.EvaluatedAt
			out.ComplianceEvaluatedAt = &ev
		case errors.IsNotFound(err):
			// No evaluation row yet → keep "unknown".
		case errors.KindOf(err) == errors.KindUnavailable:
			out.ComplianceStatus = "unavailable"
		default:
			return nil, err
		}
	}

	return out, nil
}

// countMTDDeltas walks the tenant's subscribers and counts how many
// were added (created_at >= monthStart) and how many were terminated
// (terminated_at within [monthStart, asOf]). Returns (added, terminated, err).
// Uses a large page to keep the call to one round-trip; if a tenant
// ever exceeds the page size, the count is approximate (last page
// wins) — acceptable for a dashboard tile.
func (s *DashboardService) countMTDDeltas(ctx context.Context, tenant uuid.UUID, monthStart, asOf time.Time) (int, int, error) {
	items, _, err := s.subscribers.List(ctx, port.SubscriberListFilter{
		ResellerAccountID: tenant,
		Limit:             1000,
		Offset:            0,
	})
	if err != nil {
		return 0, 0, err
	}
	added := 0
	terminated := 0
	for _, sub := range items {
		if !sub.CreatedAt.Before(monthStart) {
			added++
		}
		if sub.TerminatedAt != nil && !sub.TerminatedAt.Before(monthStart) && !sub.TerminatedAt.After(asOf) {
			terminated++
		}
	}
	return added, terminated, nil
}

// translateComplianceStatus maps the partnership-side status string
// to the dashboard chip vocabulary. The partnership statuses are
// `ramp_skipped` / `passed` / `breached`; the dashboard surfaces
// `ramp` / `on_track` / `at_risk` (more product-friendly). Anything
// unrecognized passes through verbatim so a future status doesn't
// silently drop to "unknown".
func translateComplianceStatus(partnershipStatus string) string {
	switch partnershipStatus {
	case "passed":
		return "on_track"
	case "breached":
		return "at_risk"
	case "ramp_skipped":
		return "ramp"
	default:
		return partnershipStatus
	}
}
