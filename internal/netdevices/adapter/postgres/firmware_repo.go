package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// FirmwareVersionRepository — netdev.firmware_versions
// =====================================================================

type FirmwareVersionRepository struct {
	pool *pgxpool.Pool
}

func NewFirmwareVersionRepository(pool *pgxpool.Pool) *FirmwareVersionRepository {
	return &FirmwareVersionRepository{pool: pool}
}

var _ port.FirmwareVersionRepository = (*FirmwareVersionRepository)(nil)

const firmwareVersionCols = `
	id, kind, model, version,
	COALESCE(release_notes, ''),
	is_recommended, is_critical,
	released_at, supported_until,
	created_at
`

func (r *FirmwareVersionRepository) Create(ctx context.Context, v *domain.FirmwareVersion) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.firmware_versions
			(id, kind, model, version, release_notes, is_recommended, is_critical,
			 released_at, supported_until, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`,
		v.ID, string(v.Kind), v.Model, v.Version,
		nullableString(v.ReleaseNotes),
		v.IsRecommended, v.IsCritical,
		v.ReleasedAt, v.SupportedUntil,
		v.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "firmware_version", "insert firmware version")
	}
	return nil
}

func (r *FirmwareVersionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareVersion, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+firmwareVersionCols+` FROM netdev.firmware_versions WHERE id = $1`, id)
	v, err := scanFirmwareVersion(row)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// FindRecommended returns the row marked is_recommended=true for the
// (kind, model) pair. Multiple recommended rows would be a config bug —
// we pick the most-recently-created one rather than error.
func (r *FirmwareVersionRepository) FindRecommended(ctx context.Context, kind domain.DeviceKind, model string) (*domain.FirmwareVersion, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+firmwareVersionCols+`
		FROM netdev.firmware_versions
		WHERE kind = $1 AND model = $2 AND is_recommended = TRUE
		ORDER BY created_at DESC LIMIT 1`, string(kind), model)
	v, err := scanFirmwareVersion(row)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *FirmwareVersionRepository) List(ctx context.Context, kind, model string) ([]domain.FirmwareVersion, error) {
	args := []any{}
	clause := "1=1"
	if kind != "" {
		args = append(args, kind)
		clause += " AND kind = $1"
	}
	if model != "" {
		args = append(args, model)
		if len(args) == 1 {
			clause += " AND model = $1"
		} else {
			clause += " AND model = $2"
		}
	}
	sql := `SELECT ` + firmwareVersionCols + ` FROM netdev.firmware_versions WHERE ` + clause + ` ORDER BY released_at DESC NULLS LAST, created_at DESC`
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "firmware_version.list", "list firmware versions", err)
	}
	defer rows.Close()
	out := []domain.FirmwareVersion{}
	for rows.Next() {
		v, err := scanFirmwareVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func scanFirmwareVersion(row pgx.Row) (domain.FirmwareVersion, error) {
	var v domain.FirmwareVersion
	var kind string
	err := row.Scan(
		&v.ID, &kind, &v.Model, &v.Version,
		&v.ReleaseNotes,
		&v.IsRecommended, &v.IsCritical,
		&v.ReleasedAt, &v.SupportedUntil,
		&v.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FirmwareVersion{}, derrors.NotFound("firmware_version.not_found", "firmware version not found")
	}
	if err != nil {
		return domain.FirmwareVersion{}, derrors.Wrap(derrors.KindInternal, "firmware_version.scan", "scan firmware version", err)
	}
	v.Kind = domain.DeviceKind(kind)
	return v, nil
}

// =====================================================================
// FirmwareUpgradeJobRepository — netdev.firmware_upgrade_jobs
// =====================================================================

type FirmwareUpgradeJobRepository struct {
	pool *pgxpool.Pool
}

func NewFirmwareUpgradeJobRepository(pool *pgxpool.Pool) *FirmwareUpgradeJobRepository {
	return &FirmwareUpgradeJobRepository{pool: pool}
}

var _ port.FirmwareUpgradeJobRepository = (*FirmwareUpgradeJobRepository)(nil)

const upgradeJobCols = `
	id, device_id, target_firmware_id,
	scheduled_at, started_at, completed_at,
	status, retry_count, max_retries,
	COALESCE(error_msg, ''),
	COALESCE(previous_firmware, ''),
	created_by, created_at, updated_at
`

func (r *FirmwareUpgradeJobRepository) Create(ctx context.Context, j *domain.FirmwareUpgradeJob) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.firmware_upgrade_jobs
			(id, device_id, target_firmware_id, scheduled_at, started_at, completed_at,
			 status, retry_count, max_retries, error_msg, previous_firmware,
			 created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`,
		j.ID, j.DeviceID, j.TargetFirmwareID,
		j.ScheduledAt, j.StartedAt, j.CompletedAt,
		string(j.Status), j.RetryCount, j.MaxRetries,
		nullableString(j.ErrorMsg), nullableString(j.PreviousFirmware),
		j.CreatedBy, j.CreatedAt, j.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "firmware_upgrade_job", "insert firmware upgrade job")
	}
	return nil
}

func (r *FirmwareUpgradeJobRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareUpgradeJob, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+upgradeJobCols+` FROM netdev.firmware_upgrade_jobs WHERE id = $1`, id)
	j, err := scanUpgradeJob(row)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *FirmwareUpgradeJobRepository) UpdateLifecycle(ctx context.Context, j *domain.FirmwareUpgradeJob) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE netdev.firmware_upgrade_jobs SET
			target_firmware_id = $2,
			scheduled_at = $3,
			started_at = $4,
			completed_at = $5,
			status = $6,
			retry_count = $7,
			max_retries = $8,
			error_msg = $9,
			previous_firmware = $10,
			updated_at = NOW()
		WHERE id = $1
	`,
		j.ID,
		j.TargetFirmwareID, j.ScheduledAt, j.StartedAt, j.CompletedAt,
		string(j.Status), j.RetryCount, j.MaxRetries,
		nullableString(j.ErrorMsg), nullableString(j.PreviousFirmware),
	)
	if err != nil {
		return mapDBError(err, "firmware_upgrade_job", "update firmware upgrade job")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("firmware_upgrade_job.not_found", "firmware upgrade job not found")
	}
	return nil
}

func (r *FirmwareUpgradeJobRepository) ListPendingForDevice(ctx context.Context, deviceID uuid.UUID) ([]domain.FirmwareUpgradeJob, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+upgradeJobCols+`
		FROM netdev.firmware_upgrade_jobs
		WHERE device_id = $1
		  AND status IN ('scheduled','staged','in_progress')
		ORDER BY started_at DESC NULLS LAST, scheduled_at DESC NULLS LAST
	`, deviceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "firmware_upgrade_job.list_pending", "list pending jobs", err)
	}
	defer rows.Close()
	out := []domain.FirmwareUpgradeJob{}
	for rows.Next() {
		j, err := scanUpgradeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func scanUpgradeJob(row pgx.Row) (domain.FirmwareUpgradeJob, error) {
	var j domain.FirmwareUpgradeJob
	var status string
	err := row.Scan(
		&j.ID, &j.DeviceID, &j.TargetFirmwareID,
		&j.ScheduledAt, &j.StartedAt, &j.CompletedAt,
		&status, &j.RetryCount, &j.MaxRetries,
		&j.ErrorMsg, &j.PreviousFirmware,
		&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FirmwareUpgradeJob{}, derrors.NotFound("firmware_upgrade_job.not_found", "firmware upgrade job not found")
	}
	if err != nil {
		return domain.FirmwareUpgradeJob{}, derrors.Wrap(derrors.KindInternal, "firmware_upgrade_job.scan", "scan firmware upgrade job", err)
	}
	j.Status = domain.UpgradeJobStatus(status)
	return j, nil
}
