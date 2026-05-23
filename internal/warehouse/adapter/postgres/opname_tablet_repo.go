// Wave 117 — Opname tablet session repository.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type OpnameTabletSessionRepository struct {
	pool *pgxpool.Pool
}

func NewOpnameTabletSessionRepository(pool *pgxpool.Pool) *OpnameTabletSessionRepository {
	return &OpnameTabletSessionRepository{pool: pool}
}

var _ port.OpnameTabletSessionRepository = (*OpnameTabletSessionRepository)(nil)

const opnameTabletCols = `id, opname_session_id, device_id, technician_user_id,
	started_at, completed_at, total_scans, sync_status,
	COALESCE(offline_payload_hash,''), last_synced_at, COALESCE(notes,''),
	created_at, updated_at`

func (r *OpnameTabletSessionRepository) Create(ctx context.Context, s *domain.OpnameTabletSession) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.opname_tablet_sessions
			(id, opname_session_id, device_id, technician_user_id,
			 started_at, completed_at, total_scans, sync_status,
			 offline_payload_hash, last_synced_at, notes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, s.ID, s.OpnameSessionID, s.DeviceID, s.TechnicianUserID,
		s.StartedAt, s.CompletedAt, s.TotalScans, string(s.SyncStatus),
		nullableString(s.OfflinePayloadHash), s.LastSyncedAt, nullableString(s.Notes),
		s.CreatedAt, s.UpdatedAt)
	return mapDBError(err, "opname_tablet.create", "create opname tablet session")
}

func (r *OpnameTabletSessionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.OpnameTabletSession, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+opnameTabletCols+` FROM warehouse.opname_tablet_sessions WHERE id=$1`, id)
	return scanOpnameTabletSession(row)
}

func (r *OpnameTabletSessionRepository) FindByPayloadHash(ctx context.Context, opnameSessionID uuid.UUID, hash string) (*domain.OpnameTabletSession, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+opnameTabletCols+` FROM warehouse.opname_tablet_sessions
		 WHERE opname_session_id=$1 AND offline_payload_hash=$2
	`, opnameSessionID, hash)
	return scanOpnameTabletSession(row)
}

func (r *OpnameTabletSessionRepository) ListForOpnameSession(ctx context.Context, opnameSessionID uuid.UUID) ([]domain.OpnameTabletSession, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+opnameTabletCols+` FROM warehouse.opname_tablet_sessions
		 WHERE opname_session_id=$1 ORDER BY started_at DESC
	`, opnameSessionID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.opname_tablet_list", "list opname tablet sessions", err)
	}
	defer rows.Close()
	out := []domain.OpnameTabletSession{}
	for rows.Next() {
		s, err := scanOpnameTabletSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

func (r *OpnameTabletSessionRepository) UpdateStatus(ctx context.Context, s *domain.OpnameTabletSession) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.opname_tablet_sessions
		   SET sync_status=$2, offline_payload_hash=$3, last_synced_at=$4,
		       total_scans=$5, completed_at=$6, notes=$7, updated_at=NOW()
		 WHERE id=$1
	`, s.ID, string(s.SyncStatus), nullableString(s.OfflinePayloadHash),
		s.LastSyncedAt, s.TotalScans, s.CompletedAt, nullableString(s.Notes))
	if err != nil {
		return mapDBError(err, "opname_tablet.update", "update opname tablet session")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("opname_tablet.not_found", "opname tablet session not found")
	}
	return nil
}

func scanOpnameTabletSession(row pgx.Row) (*domain.OpnameTabletSession, error) {
	var s domain.OpnameTabletSession
	var status string
	err := row.Scan(&s.ID, &s.OpnameSessionID, &s.DeviceID, &s.TechnicianUserID,
		&s.StartedAt, &s.CompletedAt, &s.TotalScans, &status,
		&s.OfflinePayloadHash, &s.LastSyncedAt, &s.Notes,
		&s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("opname_tablet.not_found", "opname tablet session not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.opname_tablet_scan", "scan opname tablet session", err)
	}
	s.SyncStatus = domain.OpnameTabletSyncStatus(status)
	return &s, nil
}
