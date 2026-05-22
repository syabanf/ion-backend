// M5 r3 — identity service additions: HRIS availability stub.
package usecase

import (
	"context"
	"time"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SetAvailability upserts the per-(user, date) status row.
func (s *Service) SetAvailability(ctx context.Context, in port.SetAvailabilityInput) error {
	if s.availability == nil {
		return derrors.New(derrors.KindInternal, "identity.r3_not_wired",
			"availability repo not configured")
	}
	if !domain.IsValidAvailabilityStatus(string(in.Status)) {
		return derrors.Validation("availability.status_invalid",
			"status must be one of available/leave/sick/training/off")
	}
	by := in.UpdatedBy
	a := &domain.UserAvailability{
		UserID:    in.UserID,
		Date:      in.Date,
		Status:    in.Status,
		Notes:     in.Notes,
		UpdatedBy: &by,
		UpdatedAt: time.Now().UTC(),
	}
	a.NormalizeNotes()
	return s.availability.Upsert(ctx, a)
}

// ListRoster returns the roster for the given date, optionally scoped
// to a branch + role list.
func (s *Service) ListRoster(ctx context.Context, in port.RosterFilter) ([]port.RosterRow, error) {
	if s.availability == nil {
		return nil, derrors.New(derrors.KindInternal, "identity.r3_not_wired",
			"availability repo not configured")
	}
	if in.Date.IsZero() {
		in.Date = time.Now().UTC()
	}
	return s.availability.ListRosterForDate(ctx, in.Date, in.BranchID, in.Roles)
}
