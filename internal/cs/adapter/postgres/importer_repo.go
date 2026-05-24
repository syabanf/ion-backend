// Wave 128D — postgres adapters for the cs ticket importer.
//
// LegacyTicketReader queries field.tickets (the legacy 5-state SM
// store) via an anti-join against cs.tickets.legacy_id so re-runs see
// only un-migrated rows.
//
// CanonicalTicketWriter performs the per-row INSERT into cs.tickets,
// passing the legacy_id through as a dedicated column (not via
// SourceMetadata — the unique partial index needs a real column). The
// ON CONFLICT clause keys on the partial unique index added in
// migration 0087 so concurrent runners + re-runs are no-ops on
// already-imported rows.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	csusecase "github.com/ion-core/backend/internal/cs/usecase"
)

// =====================================================================
// LegacyTicketReader
// =====================================================================

// LegacyTicketReader implements usecase.LegacyTicketReader against the
// live field.tickets table.
type LegacyTicketReader struct {
	pool *pgxpool.Pool
}

func NewLegacyTicketReader(pool *pgxpool.Pool) *LegacyTicketReader {
	return &LegacyTicketReader{pool: pool}
}

var _ csusecase.LegacyTicketReader = (*LegacyTicketReader)(nil)

// CountAll returns SELECT COUNT(*) FROM field.tickets. Surfaced as a
// typed error so the cron tick can distinguish "schema missing"
// (field.tickets doesn't exist on this DB) from a transient failure.
func (r *LegacyTicketReader) CountAll(ctx context.Context) (int, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("cs.importer.legacy_reader: no db pool")
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM field.tickets`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("cs.importer.legacy_reader.count: %w", err)
	}
	return n, nil
}

// ListUnmigrated returns the next batch of legacy rows that don't yet
// have a sibling cs.tickets row keyed by legacy_id. Ordered by
// created_at ASC so successive runs always make forward progress on
// the oldest rows first.
func (r *LegacyTicketReader) ListUnmigrated(ctx context.Context, limit int) ([]csusecase.LegacyTicketRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("cs.importer.legacy_reader: no db pool")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT ft.id, ft.ticket_number, ft.customer_id, ft.category, ft.priority,
		       ft.status, ft.summary, COALESCE(ft.description, ''),
		       ft.opened_by, ft.assigned_to, ft.wo_id,
		       ft.resolved_at, ft.closed_at, ft.created_at, ft.updated_at
		  FROM field.tickets ft
		 WHERE NOT EXISTS (
		         SELECT 1 FROM cs.tickets ct
		          WHERE ct.legacy_id = ft.id
		       )
		 ORDER BY ft.created_at ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("cs.importer.legacy_reader.list: %w", err)
	}
	defer rows.Close()

	out := make([]csusecase.LegacyTicketRow, 0, limit)
	for rows.Next() {
		var row csusecase.LegacyTicketRow
		var openedBy, assignedTo, woID *uuid.UUID
		var resolvedAt, closedAt *time.Time
		if err := rows.Scan(
			&row.ID, &row.TicketNumber, &row.CustomerID, &row.Category, &row.Priority,
			&row.Status, &row.Summary, &row.Description,
			&openedBy, &assignedTo, &woID,
			&resolvedAt, &closedAt, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("cs.importer.legacy_reader.scan: %w", err)
		}
		row.OpenedBy = openedBy
		row.AssignedTo = assignedTo
		row.WOID = woID
		row.ResolvedAt = resolvedAt
		row.ClosedAt = closedAt
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cs.importer.legacy_reader.iter: %w", err)
	}
	return out, nil
}

// =====================================================================
// CanonicalTicketWriter
// =====================================================================

// CanonicalImporterWriter implements usecase.CanonicalTicketWriter
// against cs.tickets. The Insert uses ON CONFLICT on the partial
// unique index from migration 0087 so concurrent runners can't
// duplicate rows.
type CanonicalImporterWriter struct {
	pool *pgxpool.Pool
}

func NewCanonicalImporterWriter(pool *pgxpool.Pool) *CanonicalImporterWriter {
	return &CanonicalImporterWriter{pool: pool}
}

var _ csusecase.CanonicalTicketWriter = (*CanonicalImporterWriter)(nil)

// Insert writes a single cs.tickets row, stamping legacy_id from
// SourceMetadata. Returns inserted=false when the row already exists
// (ON CONFLICT no-op).
func (w *CanonicalImporterWriter) Insert(ctx context.Context, t *domain.Ticket) (bool, error) {
	if w == nil || w.pool == nil {
		return false, errors.New("cs.importer.writer: no db pool")
	}
	if t == nil {
		return false, errors.New("cs.importer.writer: nil ticket")
	}
	legacyID := csusecase.LegacyID(t)
	if legacyID == uuid.Nil {
		return false, errors.New("cs.importer.writer: missing legacy_id in source_metadata")
	}

	meta, err := json.Marshal(t.SourceMetadata)
	if err != nil {
		return false, fmt.Errorf("cs.importer.writer.marshal: %w", err)
	}

	tag, err := w.pool.Exec(ctx, `
		INSERT INTO cs.tickets (
			id, ticket_no, customer_id, opened_by, opened_via, ticket_type,
			title, description, status, priority,
			assigned_user_id, assigned_team_id,
			first_response_at, resolved_at, closed_at, escalated_at, escalation_level,
			related_wo_id, related_invoice_id,
			pause_seconds, paused_since, source_metadata,
			created_at, updated_at, legacy_id
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, NULL,
			NULL, $12, $13, NULL, 0,
			$14, NULL,
			0, NULL, $15,
			$16, $17, $18
		)
		ON CONFLICT (legacy_id) WHERE legacy_id IS NOT NULL DO NOTHING
	`,
		t.ID, t.TicketNo, t.CustomerID, t.OpenedBy, string(t.OpenedVia), string(t.TicketType),
		t.Title, nullableImporterText(t.Description), string(t.Status), string(t.Priority),
		t.AssignedUserID,
		t.ResolvedAt, t.ClosedAt,
		t.RelatedWOID,
		meta,
		t.CreatedAt, t.UpdatedAt, legacyID,
	)
	if err != nil {
		return false, fmt.Errorf("cs.importer.writer.insert: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func nullableImporterText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
