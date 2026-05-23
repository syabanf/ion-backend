package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CommunicationRepository implements port.CommunicationRepository
// against cs.communications.
type CommunicationRepository struct {
	pool *pgxpool.Pool
}

func NewCommunicationRepository(pool *pgxpool.Pool) *CommunicationRepository {
	return &CommunicationRepository{pool: pool}
}

var _ port.CommunicationRepository = (*CommunicationRepository)(nil)

const commCols = `
	id, ticket_id, kind, direction,
	counterparty_kind, counterparty_id, COALESCE(counterparty_label,''),
	COALESCE(subject,''), COALESCE(body,''),
	COALESCE(attachments, '[]'::jsonb),
	COALESCE(external_message_id,''),
	sent_at, delivered_at, read_at,
	COALESCE(error_msg,''), created_at
`

func (r *CommunicationRepository) Insert(ctx context.Context, c *domain.Communication) error {
	att, err := json.Marshal(c.Attachments)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.comm.marshal", "marshal attachments", err)
	}
	if len(att) == 0 || string(att) == "null" {
		att = []byte("[]")
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.communications
			(id, ticket_id, kind, direction,
			 counterparty_kind, counterparty_id, counterparty_label,
			 subject, body, attachments, external_message_id,
			 sent_at, delivered_at, read_at,
			 error_msg, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	`,
		c.ID, c.TicketID, string(c.Kind), string(c.Direction),
		string(c.CounterpartyKind), c.CounterpartyID, nullableString(c.CounterpartyLabel),
		nullableString(c.Subject), nullableString(c.Body),
		att, nullableString(c.ExternalMessageID),
		c.SentAt, c.DeliveredAt, c.ReadAt,
		nullableString(c.ErrorMsg), c.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.comm", "insert communication")
	}
	return nil
}

func (r *CommunicationRepository) Update(ctx context.Context, c *domain.Communication) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.communications SET
			delivered_at = $2,
			read_at      = $3,
			error_msg    = $4
		WHERE id = $1
	`, c.ID, c.DeliveredAt, c.ReadAt, nullableString(c.ErrorMsg))
	if err != nil {
		return mapDBError(err, "cs.comm", "update communication")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.comm.not_found", "communication not found")
	}
	return nil
}

func (r *CommunicationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Communication, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+commCols+` FROM cs.communications WHERE id = $1`, id)
	c, err := scanComm(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CommunicationRepository) FindByExternalMessageID(ctx context.Context, mid string) (*domain.Communication, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+commCols+` FROM cs.communications WHERE external_message_id = $1`, mid)
	c, err := scanComm(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CommunicationRepository) ListByTicket(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.Communication, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+commCols+`
		  FROM cs.communications
		 WHERE ticket_id = $1
		 ORDER BY sent_at DESC
		 LIMIT $2 OFFSET $3
	`, ticketID, limit, offset)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.comm.list", "list communications", err)
	}
	defer rows.Close()
	out := []domain.Communication{}
	for rows.Next() {
		c, err := scanComm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func scanComm(row pgx.Row) (domain.Communication, error) {
	var c domain.Communication
	var kind, direction, counterKind string
	var attachments []byte
	err := row.Scan(
		&c.ID, &c.TicketID, &kind, &direction,
		&counterKind, &c.CounterpartyID, &c.CounterpartyLabel,
		&c.Subject, &c.Body,
		&attachments, &c.ExternalMessageID,
		&c.SentAt, &c.DeliveredAt, &c.ReadAt,
		&c.ErrorMsg, &c.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Communication{}, derrors.NotFound("cs.comm.not_found", "communication not found")
	}
	if err != nil {
		return domain.Communication{}, derrors.Wrap(derrors.KindInternal, "cs.comm.scan", "scan communication", err)
	}
	c.Kind = domain.CommunicationKind(kind)
	c.Direction = domain.CommDirection(direction)
	c.CounterpartyKind = domain.CounterpartyKind(counterKind)
	if len(attachments) > 0 {
		_ = json.Unmarshal(attachments, &c.Attachments)
	}
	return c, nil
}
