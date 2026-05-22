package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PortRepository struct {
	pool *pgxpool.Pool
}

func NewPortRepository(pool *pgxpool.Pool) *PortRepository {
	return &PortRepository{pool: pool}
}

var _ port.PortRepository = (*PortRepository)(nil)

const portCols = `id, node_id, port_number, port_role, max_capacity, active_connections, status, customer_id, reserved_for, reserved_until, activated_at, created_at`

func (r *PortRepository) ListForNode(ctx context.Context, nodeID uuid.UUID) ([]domain.Port, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+portCols+` FROM network.ports WHERE node_id = $1 ORDER BY port_number`, nodeID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.port_list", "list ports", err)
	}
	defer rows.Close()

	out := []domain.Port{}
	for rows.Next() {
		p, err := scanPort(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

func (r *PortRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Port, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+portCols+` FROM network.ports WHERE id = $1`, id)
	return scanPort(row)
}

// Save is upsert-style for the state-machine fields. We don't allow the
// caller to change node_id, port_number, or port_role here — those are
// schema-level facts, not lifecycle state.
func (r *PortRepository) Save(ctx context.Context, p *domain.Port) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE network.ports
		SET status = $2,
		    customer_id = $3,
		    reserved_for = $4,
		    reserved_until = $5,
		    activated_at = $6,
		    active_connections = $7
		WHERE id = $1
	`, p.ID, string(p.Status), p.CustomerID, p.ReservedFor, p.ReservedUntil, p.ActivatedAt, p.ActiveConnections)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.port_update", "update port", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("port.not_found", "port not found")
	}
	return nil
}

func scanPort(row pgx.Row) (*domain.Port, error) {
	var (
		p      domain.Port
		role   string
		status string
	)
	err := row.Scan(
		&p.ID, &p.NodeID, &p.PortNumber, &role, &p.MaxCapacity, &p.ActiveConnections,
		&status, &p.CustomerID, &p.ReservedFor, &p.ReservedUntil, &p.ActivatedAt, &p.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("port.not_found", "port not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.port_scan", "scan port", err)
	}
	p.Role = domain.PortRole(role)
	p.Status = domain.PortStatus(status)
	return &p, nil
}
