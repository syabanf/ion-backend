package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type BranchRepository struct {
	pool *pgxpool.Pool
}

func NewBranchRepository(pool *pgxpool.Pool) *BranchRepository {
	return &BranchRepository{pool: pool}
}

var _ port.BranchRepository = (*BranchRepository)(nil)

const branchColumns = `id, name, code, level, parent_id, active, created_at, updated_at`

func (r *BranchRepository) List(ctx context.Context) ([]domain.Branch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+branchColumns+`
		FROM identity.branches
		ORDER BY level, name
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.branch_list", "list branches", err)
	}
	defer rows.Close()

	out := []domain.Branch{}
	for rows.Next() {
		b, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *BranchRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Branch, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+branchColumns+` FROM identity.branches WHERE id = $1`, id)
	b, err := scanBranch(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *BranchRepository) Create(ctx context.Context, b *domain.Branch) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity.branches (id, name, code, level, parent_id, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, b.ID, b.Name, b.Code, string(b.Level), b.ParentID, b.Active, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		if containsSQLState(err.Error(), "23505") {
			return derrors.Conflict("branch.code_taken", "branch code already in use")
		}
		return derrors.Wrap(derrors.KindInternal, "db.branch_insert", "insert branch", err)
	}
	return nil
}

func (r *BranchRepository) Update(ctx context.Context, b *domain.Branch) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE identity.branches
		SET name = $2, active = $3
		WHERE id = $1
	`, b.ID, b.Name, b.Active)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.branch_update", "update branch", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("branch.not_found", "branch not found")
	}
	return nil
}

// ApplyBranchPatch performs the Wave 68 sparse PATCH covering the
// geo + per-branch operational config columns. We build the UPDATE
// dynamically: only the columns whose input pointer is non-nil OR
// whose corresponding *Clear pointer is true get written.
//
// geo_shape lives as a PostGIS geometry(MultiPolygon, 4326); the
// caller passes raw GeoJSON which we cast with
// ST_Multi(ST_GeomFromGeoJSON(...)) so a Polygon is promoted to
// MultiPolygon on the fly.
func (r *BranchRepository) ApplyBranchPatch(ctx context.Context, in port.UpdateBranchInput) error {
	sets := make([]string, 0, 12)
	args := []any{in.ID}
	idx := 2

	appendNullable := func(col string, val any, clear *bool, valSet bool) {
		if clear != nil && *clear {
			sets = append(sets, col+" = NULL")
			return
		}
		if !valSet {
			return
		}
		sets = append(sets, col+" = $"+strconv.Itoa(idx))
		args = append(args, val)
		idx++
	}

	if in.GeoShapeClear != nil && *in.GeoShapeClear {
		sets = append(sets, "geo_shape = NULL")
	} else if in.GeoShapeGeoJSON != nil && *in.GeoShapeGeoJSON != "" {
		sets = append(sets,
			"geo_shape = ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($"+
				strconv.Itoa(idx)+"), 4326))")
		args = append(args, *in.GeoShapeGeoJSON)
		idx++
	}

	if in.ODPStrategy != nil {
		appendNullable("odp_strategy", *in.ODPStrategy, in.ODPStrategyClear, true)
	} else if in.ODPStrategyClear != nil && *in.ODPStrategyClear {
		appendNullable("odp_strategy", nil, in.ODPStrategyClear, false)
	}
	if in.CableDistance != nil {
		appendNullable("cable_distance", *in.CableDistance, in.CableDistanceClear, true)
	} else if in.CableDistanceClear != nil && *in.CableDistanceClear {
		appendNullable("cable_distance", nil, in.CableDistanceClear, false)
	}
	if in.WOAutoAssign != nil {
		appendNullable("wo_auto_assign", *in.WOAutoAssign, in.WOAutoAssignClear, true)
	} else if in.WOAutoAssignClear != nil && *in.WOAutoAssignClear {
		appendNullable("wo_auto_assign", nil, in.WOAutoAssignClear, false)
	}

	if in.SLAAssignmentMinutes != nil {
		appendNullable("sla_assignment_minutes", *in.SLAAssignmentMinutes,
			in.SLAAssignmentMinutesClear, true)
	} else if in.SLAAssignmentMinutesClear != nil && *in.SLAAssignmentMinutesClear {
		appendNullable("sla_assignment_minutes", nil,
			in.SLAAssignmentMinutesClear, false)
	}
	if in.SLADispatchMinutes != nil {
		appendNullable("sla_dispatch_minutes", *in.SLADispatchMinutes,
			in.SLADispatchMinutesClear, true)
	} else if in.SLADispatchMinutesClear != nil && *in.SLADispatchMinutesClear {
		appendNullable("sla_dispatch_minutes", nil,
			in.SLADispatchMinutesClear, false)
	}
	if in.SLAInstallMinutes != nil {
		appendNullable("sla_install_minutes", *in.SLAInstallMinutes,
			in.SLAInstallMinutesClear, true)
	} else if in.SLAInstallMinutesClear != nil && *in.SLAInstallMinutesClear {
		appendNullable("sla_install_minutes", nil,
			in.SLAInstallMinutesClear, false)
	}

	if len(sets) == 0 {
		return nil // No patch fields set; cheap no-op.
	}
	sets = append(sets, "updated_at = NOW()")
	q := "UPDATE identity.branches SET " + strings.Join(sets, ", ") +
		" WHERE id = $1"
	tag, err := r.pool.Exec(ctx, q, args...)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.branch_patch",
			"apply branch patch", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("branch.not_found", "branch not found")
	}
	return nil
}

func (r *BranchRepository) FindConfig(ctx context.Context, id uuid.UUID) (*port.BranchConfigView, error) {
	var v port.BranchConfigView
	err := r.pool.QueryRow(ctx, `
		SELECT
		  CASE WHEN geo_shape IS NULL THEN NULL
		       ELSE ST_AsGeoJSON(geo_shape)::text END,
		  CASE WHEN odp_strategy IS NULL THEN NULL
		       ELSE odp_strategy::text END,
		  CASE WHEN cable_distance IS NULL THEN NULL
		       ELSE cable_distance::text END,
		  CASE WHEN wo_auto_assign IS NULL THEN NULL
		       ELSE wo_auto_assign::text END,
		  sla_assignment_minutes,
		  sla_dispatch_minutes,
		  sla_install_minutes
		FROM identity.branches
		WHERE id = $1
	`, id).Scan(&v.GeoShapeGeoJSON, &v.ODPStrategy, &v.CableDistance,
		&v.WOAutoAssign, &v.SLAAssignmentMinutes, &v.SLADispatchMinutes,
		&v.SLAInstallMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("branch.not_found", "branch not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.branch_config_read",
			"read branch config", err)
	}
	return &v, nil
}

func (r *BranchRepository) CountByLevel(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT level, COUNT(*) FROM identity.branches
		WHERE active = TRUE
		GROUP BY level
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.branch_count", "count branches", err)
	}
	defer rows.Close()
	out := map[string]int{"regional": 0, "area": 0, "sub_area": 0}
	for rows.Next() {
		var level string
		var n int
		if err := rows.Scan(&level, &n); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.branch_count_scan", "scan", err)
		}
		out[level] = n
	}
	return out, nil
}

// --- helpers ---

func scanBranch(scanner pgx.Row) (domain.Branch, error) {
	var (
		b     domain.Branch
		level string
	)
	err := scanner.Scan(&b.ID, &b.Name, &b.Code, &level, &b.ParentID, &b.Active, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Branch{}, derrors.NotFound("branch.not_found", "branch not found")
	}
	if err != nil {
		return domain.Branch{}, derrors.Wrap(derrors.KindInternal, "db.branch_scan", "scan branch", err)
	}
	b.Level = domain.BranchLevel(level)
	return b, nil
}
