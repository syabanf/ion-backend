package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AvailabilityRepository struct {
	pool *pgxpool.Pool
}

func NewAvailabilityRepository(pool *pgxpool.Pool) *AvailabilityRepository {
	return &AvailabilityRepository{pool: pool}
}

var _ port.AvailabilityRepository = (*AvailabilityRepository)(nil)

// Upsert writes (or overwrites) the row for (user_id, date).
func (r *AvailabilityRepository) Upsert(ctx context.Context, a *domain.UserAvailability) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity.user_availability (user_id, date, status, notes, updated_by, updated_at)
		VALUES ($1, $2::date, $3, $4, $5, NOW())
		ON CONFLICT (user_id, date) DO UPDATE
		   SET status = EXCLUDED.status,
		       notes = EXCLUDED.notes,
		       updated_by = EXCLUDED.updated_by,
		       updated_at = NOW()
	`,
		a.UserID, a.Date, string(a.Status), nullableString(a.Notes), a.UpdatedBy,
	)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "availability.upsert", "set user availability", err)
	}
	return nil
}

// ListRosterForDate returns one row per active user that has a role in
// any of the given role names (or all roles if roleNames is empty),
// with their availability for the given date. Missing rows default to
// 'available' so the roster always renders a complete list.
func (r *AvailabilityRepository) ListRosterForDate(ctx context.Context, date time.Time, branchID *uuid.UUID, roleNames []string) ([]port.RosterRow, error) {
	// We anchor on identity.users LEFT JOIN identity.user_availability
	// for the given date, so anyone without a row appears as 'available'.
	q := `
		SELECT u.id, u.full_name, u.email, u.employee_id, u.branch_id,
		       COALESCE(b.code,'') AS branch_code,
		       COALESCE(a.status, 'available') AS status,
		       COALESCE(a.notes, '') AS notes,
		       a.updated_at
		FROM identity.users u
		LEFT JOIN identity.user_availability a
		       ON a.user_id = u.id AND a.date = $1::date
		LEFT JOIN identity.branches b ON b.id = u.branch_id
		WHERE u.active = TRUE
	`
	args := []any{date}
	if branchID != nil {
		args = append(args, *branchID)
		q += ` AND u.branch_id = $2`
	}
	if len(roleNames) > 0 {
		// Add a role filter via EXISTS so duplicate role assignments
		// don't multiply rows.
		args = append(args, roleNames)
		q += ` AND EXISTS (
			SELECT 1 FROM identity.user_roles ur
			JOIN identity.roles r ON r.id = ur.role_id
			WHERE ur.user_id = u.id AND r.name = ANY($` + itoa(len(args)) + `)
		)`
	}
	q += ` ORDER BY u.full_name`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.roster_list", "list roster", err)
	}
	defer rows.Close()
	out := []port.RosterRow{}
	for rows.Next() {
		var (
			row     port.RosterRow
			status  string
			updated *time.Time
		)
		if err := rows.Scan(&row.UserID, &row.FullName, &row.Email, &row.EmployeeID,
			&row.BranchID, &row.BranchCode, &status, &row.Notes, &updated); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.roster_scan", "scan roster", err)
		}
		row.Status = domain.AvailabilityStatus(status)
		row.UpdatedAt = updated
		out = append(out, row)
	}
	return out, nil
}

// itoa is a tiny stringifier — avoids strconv import.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
