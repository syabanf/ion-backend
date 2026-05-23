// Wave 126 — domain projection of operations.cross_module_sla_snapshots
// + the helper that rolls multiple per-module snapshots into the
// unified view the Ops dashboard renders.
package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// SLAModule enumerates the contexts that the Cross-Module SLA Ops View
// rolls up. New modules just need a snapshot row + a ModuleSLAReader
// registration — no domain change.
type SLAModule string

const (
	ModuleCS         SLAModule = "cs"
	ModuleField      SLAModule = "field"
	ModuleEnterprise SLAModule = "enterprise"
	ModuleBilling    SLAModule = "billing"
	ModuleWarehouse  SLAModule = "warehouse"
	ModuleNOCMon     SLAModule = "nocmon"
)

// ModuleSLAStats is what each ModuleSLAReader returns. The values feed
// straight into the snapshot row.
type ModuleSLAStats struct {
	Module              SLAModule
	PeriodStart         time.Time
	PeriodEnd           time.Time
	TotalAtRisk         int
	TotalBreached       int
	P50RemainingMinutes int
	P95RemainingMinutes int
	TopBreachers        []TopBreacherEntry
}

// TopBreacherEntry is a single row in the per-module top-N list shown
// in the Ops drill-down. Kept generic on purpose — the dashboard
// renders {label, severity, link_kind, link_id, minutes_late}.
type TopBreacherEntry struct {
	Label       string    `json:"label"`
	Severity    string    `json:"severity"` // 'warn' | 'breached'
	LinkKind    string    `json:"link_kind"`
	LinkID      uuid.UUID `json:"link_id,omitempty"`
	MinutesLate int       `json:"minutes_late"`
}

// SLASnapshot is the persistent record (operations.cross_module_sla_snapshots).
type SLASnapshot struct {
	ID                  uuid.UUID
	Module              SLAModule
	AggregatedAt        time.Time
	PeriodStart         *time.Time
	PeriodEnd           *time.Time
	TotalAtRisk         int
	TotalBreached       int
	P50RemainingMinutes int
	P95RemainingMinutes int
	TopBreachers        []TopBreacherEntry
}

// UnifiedSLAView is the response shape for GET /api/ops/sla/cross-module/latest.
// Each per-module snapshot is included plus a rolled-up total.
type UnifiedSLAView struct {
	AggregatedAt       time.Time         `json:"aggregated_at"`
	TotalAtRisk        int               `json:"total_at_risk"`
	TotalBreached      int               `json:"total_breached"`
	Modules            []SLASnapshot     `json:"modules"`
	TopBreachersGlobal []TopBreacherEntry `json:"top_breachers_global"`
}

// Rollup merges a slice of per-module snapshots into the unified view.
// Picks the newest aggregated_at as the "as-of" time; sums the counts;
// merges + sorts top breachers by minutes_late desc and truncates to N.
func Rollup(snaps []SLASnapshot, topN int) UnifiedSLAView {
	out := UnifiedSLAView{
		Modules: snaps,
	}
	if topN <= 0 {
		topN = 10
	}
	for _, s := range snaps {
		out.TotalAtRisk += s.TotalAtRisk
		out.TotalBreached += s.TotalBreached
		if s.AggregatedAt.After(out.AggregatedAt) {
			out.AggregatedAt = s.AggregatedAt
		}
		out.TopBreachersGlobal = append(out.TopBreachersGlobal, s.TopBreachers...)
	}
	sort.SliceStable(out.TopBreachersGlobal, func(i, j int) bool {
		return out.TopBreachersGlobal[i].MinutesLate > out.TopBreachersGlobal[j].MinutesLate
	})
	if len(out.TopBreachersGlobal) > topN {
		out.TopBreachersGlobal = out.TopBreachersGlobal[:topN]
	}
	return out
}
