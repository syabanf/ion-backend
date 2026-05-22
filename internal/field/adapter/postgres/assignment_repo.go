package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AssignmentRepository struct {
	pool *pgxpool.Pool
}

func NewAssignmentRepository(pool *pgxpool.Pool) *AssignmentRepository {
	return &AssignmentRepository{pool: pool}
}

var _ port.AssignmentRepository = (*AssignmentRepository)(nil)

// UpsertPair wipes existing assignments for the WO and writes the new pair.
// Wrapped in a transaction so the WO is never seen with a half-applied pair.
//
// Why delete-then-insert instead of MERGE? Round 1: re-assigning is the
// rare path; the simpler, deterministic write semantics outweigh perf.
func (r *AssignmentRepository) UpsertPair(ctx context.Context, woID uuid.UUID, lead domain.Assignment, observer *domain.Assignment) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM field.wo_assignments WHERE wo_id = $1`, woID); err != nil {
		return mapDBError(err, "assign.wipe", "wipe existing assignments")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO field.wo_assignments (id, wo_id, technician_id, grade, wo_role, assigned_by, assigned_at)
		VALUES ($1,$2,$3,$4,'lead',$5,$6)
	`, lead.ID, woID, lead.TechnicianID, string(lead.Grade), lead.AssignedBy, lead.AssignedAt); err != nil {
		return mapDBError(err, "assign.lead", "create lead assignment")
	}

	if observer != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.wo_assignments (id, wo_id, technician_id, grade, wo_role, assigned_by, assigned_at)
			VALUES ($1,$2,$3,$4,'observer',$5,$6)
		`, observer.ID, woID, observer.TechnicianID, string(observer.Grade), observer.AssignedBy, observer.AssignedAt); err != nil {
			return mapDBError(err, "assign.observer", "create observer assignment")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

func (r *AssignmentRepository) ListForWO(ctx context.Context, woID uuid.UUID) ([]port.AssignmentView, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.wo_id, a.technician_id, a.grade, a.wo_role,
		       a.assigned_by, a.assigned_at,
		       COALESCE(u.full_name,''), COALESCE(u.email,'')
		FROM field.wo_assignments a
		LEFT JOIN identity.users u ON u.id = a.technician_id
		WHERE a.wo_id = $1
		ORDER BY a.wo_role = 'lead' DESC, a.assigned_at
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.assign_list", "list assignments", err)
	}
	defer rows.Close()
	out := []port.AssignmentView{}
	for rows.Next() {
		var (
			a     domain.Assignment
			grade string
			role  string
			v     port.AssignmentView
		)
		if err := rows.Scan(&a.ID, &a.WOID, &a.TechnicianID, &grade, &role,
			&a.AssignedBy, &a.AssignedAt, &v.TechnicianName, &v.TechnicianEmail); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.assign_scan", "scan assignment", err)
		}
		a.Grade = domain.TechGrade(grade)
		a.WORole = domain.WORole(role)
		v.Assignment = a
		out = append(out, v)
	}
	return out, nil
}
