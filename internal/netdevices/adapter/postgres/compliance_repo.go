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

// ComplianceRepository — netdev.firmware_compliance_runs
type ComplianceRepository struct {
	pool *pgxpool.Pool
}

func NewComplianceRepository(pool *pgxpool.Pool) *ComplianceRepository {
	return &ComplianceRepository{pool: pool}
}

var _ port.ComplianceRepository = (*ComplianceRepository)(nil)

func (r *ComplianceRepository) Create(ctx context.Context, run *domain.FirmwareComplianceRun) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.firmware_compliance_runs
			(id, started_at, scope)
		VALUES ($1, $2, $3)
	`, run.ID, run.StartedAt, run.Scope)
	if err != nil {
		return mapDBError(err, "compliance_run", "insert compliance run")
	}
	return nil
}

func (r *ComplianceRepository) UpdateFinish(ctx context.Context, run *domain.FirmwareComplianceRun) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE netdev.firmware_compliance_runs SET
			finished_at = $2,
			total_devices = $3,
			compliant = $4,
			non_compliant = $5,
			critical_pending = $6,
			report_payload = $7
		WHERE id = $1
	`,
		run.ID,
		run.FinishedAt,
		run.TotalDevices,
		run.Compliant,
		run.NonCompliant,
		run.CriticalPending,
		run.ReportPayload,
	)
	if err != nil {
		return mapDBError(err, "compliance_run", "update compliance run")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("compliance_run.not_found", "compliance run not found")
	}
	return nil
}

func (r *ComplianceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareComplianceRun, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, started_at, finished_at, scope,
		       COALESCE(total_devices, 0),
		       COALESCE(compliant, 0),
		       COALESCE(non_compliant, 0),
		       COALESCE(critical_pending, 0),
		       report_payload
		FROM netdev.firmware_compliance_runs
		WHERE id = $1
	`, id)
	run, err := scanComplianceRun(row)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func scanComplianceRun(row pgx.Row) (domain.FirmwareComplianceRun, error) {
	var run domain.FirmwareComplianceRun
	err := row.Scan(
		&run.ID, &run.StartedAt, &run.FinishedAt, &run.Scope,
		&run.TotalDevices, &run.Compliant, &run.NonCompliant, &run.CriticalPending,
		&run.ReportPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FirmwareComplianceRun{}, derrors.NotFound("compliance_run.not_found", "compliance run not found")
	}
	if err != nil {
		return domain.FirmwareComplianceRun{}, derrors.Wrap(derrors.KindInternal, "compliance_run.scan", "scan compliance run", err)
	}
	return run, nil
}
