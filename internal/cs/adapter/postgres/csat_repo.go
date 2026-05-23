package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CSATRepository implements port.CSATRepository against cs.csat_responses.
type CSATRepository struct {
	pool *pgxpool.Pool
}

func NewCSATRepository(pool *pgxpool.Pool) *CSATRepository {
	return &CSATRepository{pool: pool}
}

var _ port.CSATRepository = (*CSATRepository)(nil)

const csatCols = `
	id, ticket_id, customer_id, rating, COALESCE(comment,''),
	channel, requested_at, responded_at
`

func (r *CSATRepository) Insert(ctx context.Context, c *domain.CSATResponse) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.csat_responses
			(id, ticket_id, customer_id, rating, comment, channel,
			 requested_at, responded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (ticket_id) DO UPDATE SET
			rating       = EXCLUDED.rating,
			comment      = EXCLUDED.comment,
			channel      = EXCLUDED.channel,
			responded_at = EXCLUDED.responded_at
	`,
		c.ID, c.TicketID, c.CustomerID, c.Rating, nullableString(c.Comment),
		string(c.Channel), c.RequestedAt, c.RespondedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.csat", "insert csat response")
	}
	return nil
}

func (r *CSATRepository) FindByTicket(ctx context.Context, ticketID uuid.UUID) (*domain.CSATResponse, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+csatCols+` FROM cs.csat_responses WHERE ticket_id = $1`, ticketID)
	c, err := scanCSAT(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CSATRepository) Aggregations(ctx context.Context, f port.CSATAggregationFilter) (port.CSATAggregations, error) {
	var wh []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		wh = append(wh, fmt.Sprintf(cond, len(args)))
	}
	wh = append(wh, "responded_at IS NOT NULL")
	if f.From != nil {
		add("responded_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("responded_at <= $%d", *f.To)
	}
	where := " WHERE " + strings.Join(wh, " AND ")
	var agg port.CSATAggregations
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COALESCE(AVG(rating::numeric), 0)::float8,
			COUNT(*) FILTER (WHERE rating = 5),
			COUNT(*) FILTER (WHERE rating = 4),
			COUNT(*) FILTER (WHERE rating BETWEEN 1 AND 2),
			COUNT(*) FILTER (WHERE rating = 3)
		  FROM cs.csat_responses
	`+where, args...).Scan(
		&agg.Count, &agg.AvgRating, &agg.Promoters, &agg.Passives, &agg.Detractors, &agg.Neutrals,
	)
	if err != nil {
		return port.CSATAggregations{}, derrors.Wrap(derrors.KindInternal, "cs.csat.agg", "aggregate csat", err)
	}
	if agg.Count > 0 {
		agg.PromoterPct = float64(agg.Promoters) / float64(agg.Count) * 100
		agg.DetractorPct = float64(agg.Detractors) / float64(agg.Count) * 100
		agg.NPSScore = agg.PromoterPct - agg.DetractorPct
	}
	return agg, nil
}

func (r *CSATRepository) ListTicketsNeedingFollowupInvite(ctx context.Context, resolvedSince, resolvedBefore time.Time, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT t.id
		  FROM cs.tickets t
		  LEFT JOIN cs.csat_responses c ON c.ticket_id = t.id
		 WHERE t.status IN ('resolved','closed')
		   AND t.resolved_at IS NOT NULL
		   AND t.resolved_at BETWEEN $1 AND $2
		   AND c.id IS NULL
		 ORDER BY t.resolved_at ASC
		 LIMIT $3
	`, resolvedSince, resolvedBefore, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.csat.list_followup", "list csat followup invites", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "cs.csat.scan_followup", "scan followup id", err)
		}
		out = append(out, id)
	}
	return out, nil
}

func scanCSAT(row pgx.Row) (domain.CSATResponse, error) {
	var c domain.CSATResponse
	var channel string
	err := row.Scan(
		&c.ID, &c.TicketID, &c.CustomerID, &c.Rating, &c.Comment,
		&channel, &c.RequestedAt, &c.RespondedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CSATResponse{}, derrors.NotFound("cs.csat.not_found", "csat response not found")
	}
	if err != nil {
		return domain.CSATResponse{}, derrors.Wrap(derrors.KindInternal, "cs.csat.scan", "scan csat", err)
	}
	c.Channel = domain.CSATChannel(channel)
	return c, nil
}
