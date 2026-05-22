// M4 round-2 service methods + helpers: onboarding schemas (list/get)
// and the sales dashboard summary. The schema-driven onboarding hook
// and sales-type enforcement on CreateLead live in service.go itself.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// salesTypeMatchesBroadband returns true when a sales rep with the given
// sales_type is allowed to own a broadband lead. PRD §M4 only allows
// 'broadband' or 'both'; 'enterprise'-only reps are rejected.
func salesTypeMatchesBroadband(salesType string) bool {
	return salesType == "broadband" || salesType == "both"
}

// =====================================================================
// Onboarding schemas
// =====================================================================

func (s *Service) ListOnboardingSchemas(ctx context.Context) ([]domain.OnboardingSchema, error) {
	if s.schemas == nil {
		return nil, derrors.New(derrors.KindInternal, "crm.r2_not_wired",
			"onboarding schema repo not configured")
	}
	return s.schemas.List(ctx)
}

func (s *Service) GetOnboardingSchema(ctx context.Context, id uuid.UUID) (*domain.OnboardingSchema, error) {
	if s.schemas == nil {
		return nil, derrors.New(derrors.KindInternal, "crm.r2_not_wired",
			"onboarding schema repo not configured")
	}
	return s.schemas.FindByID(ctx, id)
}

// =====================================================================
// Sales dashboard
//
// Round-2 deliberately composes its result from the existing list APIs
// rather than introducing a bespoke aggregate query — keeps the data
// path simple and reuses the same denormalised projections the UI
// already understands. When we hit per-user volume that makes this
// hot, we can swap in a single dashboard query without touching the
// HTTP shape.
// =====================================================================

func (s *Service) SalesDashboard(ctx context.Context, in port.SalesDashboardInput) (*port.SalesDashboardView, error) {
	statuses := []domain.LeadStatus{
		domain.LeadStatusNew,
		domain.LeadStatusQualified,
		domain.LeadStatusPotential,
		domain.LeadStatusRejected,
		domain.LeadStatusConverted,
		domain.LeadStatusLost,
	}
	byStatus := map[domain.LeadStatus]int{}
	for _, st := range statuses {
		_, total, err := s.leads.List(ctx, port.LeadListFilter{
			Status:  string(st),
			SalesID: in.MineUserID,
			Limit:   1,
		})
		if err != nil {
			return nil, err
		}
		byStatus[st] = total
	}

	// Recent (open) leads — top 5 sorted desc by created_at, all statuses.
	recent, _, err := s.leads.List(ctx, port.LeadListFilter{
		SalesID: in.MineUserID,
		Limit:   5,
	})
	if err != nil {
		return nil, err
	}

	// Recent conversions: orders created this month. The order repo
	// doesn't filter by sales_id in r1, so we fetch a small page and
	// post-filter — fine at our scale.
	allOrders, _, err := s.orders.List(ctx, "", 50, 0)
	if err != nil {
		return nil, err
	}
	startOfMonth := startOfMonth(time.Now().UTC())
	convertedThisMonth := 0
	totalThisMonth := 0.0
	recentOrders := []domain.Order{}
	for _, o := range allOrders {
		if o.CreatedAt.Before(startOfMonth) {
			continue
		}
		if in.MineUserID != nil && (o.SalesID == nil || *o.SalesID != *in.MineUserID) {
			continue
		}
		convertedThisMonth++
		totalThisMonth += o.OTCPrice + o.MonthlyPrice
		if len(recentOrders) < 5 {
			recentOrders = append(recentOrders, o)
		}
	}

	return &port.SalesDashboardView{
		LeadsByStatus:      byStatus,
		ConvertedThisMonth: convertedThisMonth,
		OrdersThisMonth:    convertedThisMonth,
		TotalOTCMonth:      totalThisMonth,
		RecentLeads:        recent,
		RecentConversions:  recentOrders,
	}, nil
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
