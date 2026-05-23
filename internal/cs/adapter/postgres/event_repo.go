package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TicketEventRepository implements port.TicketEventRepository against
// cs.ticket_events.
type TicketEventRepository struct {
	pool *pgxpool.Pool
}

func NewTicketEventRepository(pool *pgxpool.Pool) *TicketEventRepository {
	return &TicketEventRepository{pool: pool}
}

var _ port.TicketEventRepository = (*TicketEventRepository)(nil)

func (r *TicketEventRepository) Insert(ctx context.Context, ev *domain.TicketEvent) error {
	payload, err := jsonbBytesNullable(ev.Payload)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.event.marshal", "marshal event payload", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.ticket_events
			(id, ticket_id, kind, payload, actor_id, actor_role, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		ev.ID, ev.TicketID, string(ev.Kind), payload, ev.ActorID, nullableString(ev.ActorRole),
		ev.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.event", "insert ticket event")
	}
	return nil
}

func (r *TicketEventRepository) List(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.TicketEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, ticket_id, kind, COALESCE(payload, '{}'::jsonb), actor_id, COALESCE(actor_role, ''), created_at
		  FROM cs.ticket_events
		 WHERE ticket_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2 OFFSET $3
	`, ticketID, limit, offset)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.event.list", "list ticket events", err)
	}
	defer rows.Close()
	out := []domain.TicketEvent{}
	for rows.Next() {
		ev, err := scanTicketEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func scanTicketEvent(row pgx.Row) (domain.TicketEvent, error) {
	var ev domain.TicketEvent
	var kind string
	var payload []byte
	err := row.Scan(
		&ev.ID, &ev.TicketID, &kind, &payload, &ev.ActorID, &ev.ActorRole, &ev.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TicketEvent{}, derrors.NotFound("cs.event.not_found", "ticket event not found")
	}
	if err != nil {
		return domain.TicketEvent{}, derrors.Wrap(derrors.KindInternal, "cs.event.scan", "scan ticket event", err)
	}
	ev.Kind = domain.EventKind(kind)
	if m, err := unmarshalJSONBMap(payload); err == nil {
		ev.Payload = m
	}
	return ev, nil
}
