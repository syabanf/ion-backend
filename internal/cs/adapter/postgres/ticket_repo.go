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

// TicketRepository implements port.TicketRepository against cs.tickets.
type TicketRepository struct {
	pool *pgxpool.Pool
}

func NewTicketRepository(pool *pgxpool.Pool) *TicketRepository {
	return &TicketRepository{pool: pool}
}

var _ port.TicketRepository = (*TicketRepository)(nil)

const ticketCols = `
	id, ticket_no, customer_id, opened_by, opened_via, ticket_type,
	title, COALESCE(description, ''),
	status, priority,
	assigned_user_id, assigned_team_id,
	first_response_at, resolved_at, closed_at, escalated_at, escalation_level,
	related_wo_id, related_invoice_id,
	pause_seconds, paused_since,
	COALESCE(source_metadata, '{}'::jsonb),
	created_at, updated_at,
	sla_matrix_id, sla_first_response_due_at, sla_resolve_due_at,
	sla_breached_first_response, sla_breached_resolve, sla_warned_at
`

func (r *TicketRepository) Create(ctx context.Context, t *domain.Ticket) error {
	meta, err := jsonbBytes(t.SourceMetadata)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.ticket.marshal", "marshal source_metadata", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.tickets
			(id, ticket_no, customer_id, opened_by, opened_via, ticket_type,
			 title, description, status, priority,
			 assigned_user_id, assigned_team_id,
			 first_response_at, resolved_at, closed_at, escalated_at, escalation_level,
			 related_wo_id, related_invoice_id,
			 pause_seconds, paused_since, source_metadata,
			 created_at, updated_at,
			 sla_matrix_id, sla_first_response_due_at, sla_resolve_due_at,
			 sla_breached_first_response, sla_breached_resolve, sla_warned_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
		        $11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
		        $25,$26,$27,$28,$29,$30)
	`,
		t.ID, t.TicketNo, t.CustomerID, t.OpenedBy, string(t.OpenedVia), string(t.TicketType),
		t.Title, nullableString(t.Description), string(t.Status), string(t.Priority),
		t.AssignedUserID, t.AssignedTeamID,
		t.FirstResponseAt, t.ResolvedAt, t.ClosedAt, t.EscalatedAt, t.EscalationLevel,
		t.RelatedWOID, t.RelatedInvoiceID,
		t.PauseSeconds, t.PausedSince, meta,
		t.CreatedAt, t.UpdatedAt,
		t.SLAMatrixID, t.SLAFirstResponseDueAt, t.SLAResolveDueAt,
		t.SLABreachedFirstResponse, t.SLABreachedResolve, t.SLAWarnedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.ticket", "insert ticket")
	}
	return nil
}

func (r *TicketRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Ticket, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM cs.tickets WHERE id = $1`, id)
	t, err := scanTicket(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TicketRepository) List(ctx context.Context, f port.TicketListFilter) ([]domain.Ticket, int, error) {
	var wh []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		wh = append(wh, fmt.Sprintf(cond, len(args)))
	}
	if f.CustomerID != nil {
		add("customer_id = $%d", *f.CustomerID)
	}
	if f.AssignedUserID != nil {
		add("assigned_user_id = $%d", *f.AssignedUserID)
	}
	if f.AssignedTeamID != nil {
		add("assigned_team_id = $%d", *f.AssignedTeamID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Priority != "" {
		add("priority = $%d", f.Priority)
	}
	if f.TicketType != "" {
		add("ticket_type = $%d", f.TicketType)
	}
	if f.OpenedVia != "" {
		add("opened_via = $%d", f.OpenedVia)
	}
	if f.OnlyUnassigned {
		wh = append(wh, "assigned_user_id IS NULL")
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM cs.tickets`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cs.ticket.count", "count tickets", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + ticketCols + ` FROM cs.tickets` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cs.ticket.list", "list tickets", err)
	}
	defer rows.Close()

	out := []domain.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, nil
}

func (r *TicketRepository) Update(ctx context.Context, t *domain.Ticket) error {
	meta, err := jsonbBytes(t.SourceMetadata)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.ticket.marshal", "marshal source_metadata", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.tickets SET
			ticket_type = $2,
			title = $3,
			description = $4,
			status = $5,
			priority = $6,
			assigned_user_id = $7,
			assigned_team_id = $8,
			first_response_at = $9,
			resolved_at = $10,
			closed_at = $11,
			escalated_at = $12,
			escalation_level = $13,
			related_wo_id = $14,
			related_invoice_id = $15,
			pause_seconds = $16,
			paused_since = $17,
			source_metadata = $18,
			sla_matrix_id = $19,
			sla_first_response_due_at = $20,
			sla_resolve_due_at = $21,
			sla_breached_first_response = $22,
			sla_breached_resolve = $23,
			sla_warned_at = $24,
			updated_at = NOW()
		WHERE id = $1
	`,
		t.ID, string(t.TicketType), t.Title, nullableString(t.Description),
		string(t.Status), string(t.Priority),
		t.AssignedUserID, t.AssignedTeamID,
		t.FirstResponseAt, t.ResolvedAt, t.ClosedAt, t.EscalatedAt, t.EscalationLevel,
		t.RelatedWOID, t.RelatedInvoiceID,
		t.PauseSeconds, t.PausedSince, meta,
		t.SLAMatrixID, t.SLAFirstResponseDueAt, t.SLAResolveDueAt,
		t.SLABreachedFirstResponse, t.SLABreachedResolve, t.SLAWarnedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.ticket", "update ticket")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.ticket.not_found", "ticket not found")
	}
	return nil
}

// NextTicketNo computes the next sequential number in TKT-YYYY-NNNNNNNN
// format. Uses a per-year COUNT (cheap on the year-indexed
// ticket_no prefix; acceptable for Phase 1 volumes). The format is
// chosen for the catalog's TC-TKT-008 ("ticket number sequential, no
// gap, unique across system") — collisions are caught by the UNIQUE
// constraint and bubble up as a Conflict.
func (r *TicketRepository) NextTicketNo(ctx context.Context, year int) (string, error) {
	prefix := fmt.Sprintf("TKT-%04d-", year)
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM cs.tickets WHERE ticket_no LIKE $1`,
		prefix+"%",
	).Scan(&count)
	if err != nil {
		return "", derrors.Wrap(derrors.KindInternal, "cs.ticket.next_no", "compute next ticket_no", err)
	}
	return fmt.Sprintf("%s%08d", prefix, count+1), nil
}

// =====================================================================
// AutoCloseRepository — used by the daily cron in usecase.AutoCloseResolved
// =====================================================================

// ListResolvedOlderThan returns resolved tickets older than cutoff for
// auto-close. Implements port.AutoCloseRepository.
func (r *TicketRepository) ListResolvedOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.Ticket, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+ticketCols+`
		  FROM cs.tickets
		 WHERE status = 'resolved'
		   AND resolved_at IS NOT NULL
		   AND resolved_at < $1
		 ORDER BY resolved_at ASC
		 LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.ticket.list_resolved", "list resolved tickets", err)
	}
	defer rows.Close()
	out := []domain.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

var _ port.AutoCloseRepository = (*TicketRepository)(nil)

// =====================================================================
// SLA evaluator support (Wave 124)
//
// ListActiveForSLAEvaluation returns non-terminal tickets that the SLA
// cron loop should examine. Filters out resolved + closed (the
// resolve-budget clock stops there) and tickets without an SLA
// snapshot (pre-Wave-124 rows). Bounded by `limit` to keep a single
// tick cheap.
// =====================================================================

func (r *TicketRepository) ListActiveForSLAEvaluation(ctx context.Context, limit int) ([]domain.Ticket, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+ticketCols+`
		  FROM cs.tickets
		 WHERE status NOT IN ('resolved','closed')
		   AND sla_matrix_id IS NOT NULL
		 ORDER BY sla_resolve_due_at ASC NULLS LAST
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.ticket.list_active_sla", "list active tickets for sla eval", err)
	}
	defer rows.Close()
	out := []domain.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func scanTicket(row pgx.Row) (domain.Ticket, error) {
	var t domain.Ticket
	var status, priority, openedVia, ticketType string
	var meta []byte
	err := row.Scan(
		&t.ID, &t.TicketNo, &t.CustomerID, &t.OpenedBy, &openedVia, &ticketType,
		&t.Title, &t.Description,
		&status, &priority,
		&t.AssignedUserID, &t.AssignedTeamID,
		&t.FirstResponseAt, &t.ResolvedAt, &t.ClosedAt, &t.EscalatedAt, &t.EscalationLevel,
		&t.RelatedWOID, &t.RelatedInvoiceID,
		&t.PauseSeconds, &t.PausedSince,
		&meta,
		&t.CreatedAt, &t.UpdatedAt,
		&t.SLAMatrixID, &t.SLAFirstResponseDueAt, &t.SLAResolveDueAt,
		&t.SLABreachedFirstResponse, &t.SLABreachedResolve, &t.SLAWarnedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Ticket{}, derrors.NotFound("cs.ticket.not_found", "ticket not found")
	}
	if err != nil {
		return domain.Ticket{}, derrors.Wrap(derrors.KindInternal, "cs.ticket.scan", "scan ticket", err)
	}
	t.Status = domain.TicketStatus(status)
	t.Priority = domain.Priority(priority)
	t.OpenedVia = domain.OpenedVia(openedVia)
	t.TicketType = domain.TicketType(ticketType)
	if m, err := unmarshalJSONBMap(meta); err == nil {
		t.SourceMetadata = m
	}
	return t, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
