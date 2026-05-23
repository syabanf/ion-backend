// Package gateway implements the HRIS external gateway adapter.
//
// Two implementations:
//   - StubGateway: returns canned data; default unless HRIS_GATEWAY_ENABLED=true.
//   - (future) RESTGateway: pulls from a real HRIS via HTTPS polling or webhook.
//
// The svc binary chooses based on the env var so dev/integration tests can
// run without a counterparty.
package gateway

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
)

// StubGateway returns canned data. Useful for tests + dev.
type StubGateway struct {
	employees []port.EmployeeRecord
	events    []*domain.EmployeeEvent
}

// NewStubGateway builds a stub seeded with one active sales rep, one TL,
// and one resigned sales rep — matching the migration seed.
func NewStubGateway() *StubGateway {
	hire1 := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	hire2 := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	hire3 := time.Date(2022, 4, 12, 0, 0, 0, 0, time.UTC)
	resign3 := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	resignEv, _ := domain.NewEmployeeEvent(
		"EMP00003",
		domain.EventKindResigned,
		map[string]any{"reason": "personal", "final_day": "2026-03-31"},
		time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC),
		"stub",
	)
	// Stable id so re-poll is idempotent.
	resignEv.ID = uuid.MustParse("00000000-0000-0000-0000-00000000e003")

	return &StubGateway{
		employees: []port.EmployeeRecord{
			{
				EmployeeNo:   "EMP00001",
				FullName:     "Andi Pratama",
				Email:        "andi.pratama@ion.example",
				Phone:        "+62811000001",
				Department:   "Sales",
				Position:     "Sales Rep",
				HireDate:     &hire1,
				Status:       domain.EmployeeStatusActive,
				KYCCompleted: true,
			},
			{
				EmployeeNo:   "EMP00002",
				FullName:     "Bunga Lestari",
				Email:        "bunga.lestari@ion.example",
				Phone:        "+62811000002",
				Department:   "Operations",
				Position:     "Team Lead",
				HireDate:     &hire2,
				Status:       domain.EmployeeStatusActive,
				KYCCompleted: true,
			},
			{
				EmployeeNo:   "EMP00003",
				FullName:     "Cipto Wijaya",
				Email:        "cipto.wijaya@ion.example",
				Phone:        "+62811000003",
				Department:   "Sales",
				Position:     "Senior Sales Rep",
				HireDate:     &hire3,
				ResignDate:   &resign3,
				Status:       domain.EmployeeStatusResigned,
				KYCCompleted: true,
			},
		},
		events: []*domain.EmployeeEvent{resignEv},
	}
}

var _ port.HRISGateway = (*StubGateway)(nil)

// FetchEmployees ignores `since` and returns the canned set. Real adapter
// would use `since` to request a delta.
func (g *StubGateway) FetchEmployees(_ context.Context, _ time.Time) ([]port.EmployeeRecord, error) {
	out := make([]port.EmployeeRecord, len(g.employees))
	copy(out, g.employees)
	return out, nil
}

// FetchEvents ignores `since` and returns the canned event set. Real adapter
// would honour `since` so a re-poll returns a strict delta.
func (g *StubGateway) FetchEvents(_ context.Context, _ time.Time) ([]*domain.EmployeeEvent, error) {
	out := make([]*domain.EmployeeEvent, len(g.events))
	copy(out, g.events)
	return out, nil
}
