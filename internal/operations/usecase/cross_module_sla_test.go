package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
)

type stubXMSnapRepo struct {
	rows    []*domain.SLASnapshot
	latest  []domain.SLASnapshot
	history []domain.SLASnapshot
}

func (s *stubXMSnapRepo) Create(_ context.Context, snap *domain.SLASnapshot) error {
	s.rows = append(s.rows, snap)
	return nil
}
func (s *stubXMSnapRepo) LatestForModule(_ context.Context, _ domain.SLAModule) (*domain.SLASnapshot, error) {
	return nil, nil
}
func (s *stubXMSnapRepo) ListLatest(_ context.Context) ([]domain.SLASnapshot, error) {
	return s.latest, nil
}
func (s *stubXMSnapRepo) History(_ context.Context, _ domain.SLAModule, _, _ time.Time, _ int) ([]domain.SLASnapshot, error) {
	return s.history, nil
}

type stubModuleReader struct {
	name  domain.SLAModule
	stats domain.ModuleSLAStats
}

func (r *stubModuleReader) ModuleName() domain.SLAModule { return r.name }
func (r *stubModuleReader) SLAStats(_ context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error) {
	r.stats.Module = r.name
	r.stats.PeriodStart = periodStart
	r.stats.PeriodEnd = periodEnd
	return r.stats, nil
}

func TestCrossModuleSLA_AggregateAll_PersistsSnapshots(t *testing.T) {
	repo := &stubXMSnapRepo{}
	r1 := &stubModuleReader{name: domain.ModuleCS, stats: domain.ModuleSLAStats{TotalAtRisk: 3, TotalBreached: 1}}
	r2 := &stubModuleReader{name: domain.ModuleField, stats: domain.ModuleSLAStats{TotalAtRisk: 1, TotalBreached: 0}}
	svc := NewCrossModuleSLAService(CrossModuleSLADeps{
		Repo:    repo,
		Readers: []port.ModuleSLAReader{r1, r2},
	})
	n, err := svc.AggregateAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 modules persisted, got %d", n)
	}
	if len(repo.rows) != 2 {
		t.Errorf("expected 2 rows in repo, got %d", len(repo.rows))
	}
}

func TestCrossModuleSLA_LatestUnified_Rollup(t *testing.T) {
	now := time.Now()
	repo := &stubXMSnapRepo{
		latest: []domain.SLASnapshot{
			{Module: domain.ModuleCS, AggregatedAt: now, TotalAtRisk: 2, TotalBreached: 1,
				TopBreachers: []domain.TopBreacherEntry{{Label: "T-A", MinutesLate: 30}}},
			{Module: domain.ModuleBilling, AggregatedAt: now, TotalAtRisk: 0, TotalBreached: 4,
				TopBreachers: []domain.TopBreacherEntry{{Label: "INV-9", MinutesLate: 1440}}},
		},
	}
	svc := NewCrossModuleSLAService(CrossModuleSLADeps{Repo: repo})
	view, err := svc.LatestUnified(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if view.TotalAtRisk != 2 || view.TotalBreached != 5 {
		t.Errorf("rollup wrong: %+v", view)
	}
	if len(view.TopBreachersGlobal) != 2 || view.TopBreachersGlobal[0].Label != "INV-9" {
		t.Errorf("expected INV-9 first, got %+v", view.TopBreachersGlobal)
	}
}
