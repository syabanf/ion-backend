package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TicketChannelRepository implements port.TicketChannelRepository
// against cs.ticket_channels.
type TicketChannelRepository struct {
	pool *pgxpool.Pool
}

func NewTicketChannelRepository(pool *pgxpool.Pool) *TicketChannelRepository {
	return &TicketChannelRepository{pool: pool}
}

var _ port.TicketChannelRepository = (*TicketChannelRepository)(nil)

const channelCols = `
	id, code, name, kind, is_active, COALESCE(config_payload, '{}'::jsonb),
	created_at, updated_at
`

func (r *TicketChannelRepository) Insert(ctx context.Context, c *domain.Channel) error {
	cfg, err := jsonbBytesNullable(c.ConfigPayload)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.channel.marshal", "marshal channel config", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.ticket_channels
			(id, code, name, kind, is_active, config_payload, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		c.ID, c.Code, c.Name, string(c.Kind), c.IsActive, cfg, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.channel", "insert channel")
	}
	return nil
}

func (r *TicketChannelRepository) Update(ctx context.Context, c *domain.Channel) error {
	cfg, err := jsonbBytesNullable(c.ConfigPayload)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.channel.marshal", "marshal channel config", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.ticket_channels SET
			name = $2,
			kind = $3,
			is_active = $4,
			config_payload = $5,
			updated_at = NOW()
		WHERE code = $1
	`,
		c.Code, c.Name, string(c.Kind), c.IsActive, cfg,
	)
	if err != nil {
		return mapDBError(err, "cs.channel", "update channel")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.channel.not_found", "channel not found")
	}
	return nil
}

func (r *TicketChannelRepository) FindByCode(ctx context.Context, code string) (*domain.Channel, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+channelCols+` FROM cs.ticket_channels WHERE code = $1`, code)
	c, err := scanChannel(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *TicketChannelRepository) List(ctx context.Context, onlyActive bool) ([]domain.Channel, error) {
	where := ""
	args := []any{}
	if onlyActive {
		where = " WHERE is_active = TRUE"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+channelCols+` FROM cs.ticket_channels`+where+` ORDER BY code`, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.channel.list", "list channels", err)
	}
	defer rows.Close()
	out := []domain.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func scanChannel(row pgx.Row) (domain.Channel, error) {
	var c domain.Channel
	var kind string
	var cfg []byte
	err := row.Scan(
		&c.ID, &c.Code, &c.Name, &kind, &c.IsActive, &cfg,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, derrors.NotFound("cs.channel.not_found", "channel not found")
	}
	if err != nil {
		return domain.Channel{}, derrors.Wrap(derrors.KindInternal, "cs.channel.scan", "scan channel", err)
	}
	c.Kind = domain.ChannelKind(kind)
	if m, err := unmarshalJSONBMap(cfg); err == nil {
		c.ConfigPayload = m
	}
	return c, nil
}
