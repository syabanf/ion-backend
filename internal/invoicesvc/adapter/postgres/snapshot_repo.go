package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// InvoiceSnapshotRepository implements port.InvoiceSnapshotRepository.
type InvoiceSnapshotRepository struct {
	pool *pgxpool.Pool
}

func NewInvoiceSnapshotRepository(pool *pgxpool.Pool) *InvoiceSnapshotRepository {
	return &InvoiceSnapshotRepository{pool: pool}
}

var _ port.InvoiceSnapshotRepository = (*InvoiceSnapshotRepository)(nil)

const snapCols = `
	id, invoice_id, customer_id, plan_id, schema_snapshot_id,
	snapshotted_at, COALESCE(total_amount, 0), line_items::text,
	COALESCE(status_at_snapshot, ''), COALESCE(source_module, 'billing')
`

func (r *InvoiceSnapshotRepository) Create(ctx context.Context, snap *domain.InvoiceSnapshot) error {
	if snap == nil {
		return derrors.Validation("snapshot.nil", "snapshot is nil")
	}
	if snap.InvoiceID == uuid.Nil {
		return derrors.Validation("snapshot.invoice_required", "invoice_id is required")
	}
	lineJSON, err := snap.LineItemsJSON()
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "snapshot.marshal", "marshal line items", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO invoicesvc.invoice_snapshots
			(id, invoice_id, customer_id, plan_id, schema_snapshot_id,
			 snapshotted_at, total_amount, line_items,
			 status_at_snapshot, source_module)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
	`,
		snap.ID, snap.InvoiceID, snap.CustomerID, snap.PlanID, snap.SchemaSnapshotID,
		snap.SnapshottedAt, snap.TotalAmount, string(lineJSON),
		nullableString(snap.StatusAtSnapshot), string(snap.SourceModule),
	)
	if err != nil {
		return mapDBError(err, "snapshot", "insert invoice snapshot")
	}
	return nil
}

func (r *InvoiceSnapshotRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.InvoiceSnapshot, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+snapCols+` FROM invoicesvc.invoice_snapshots WHERE id = $1`, id)
	s, err := scanSnapshot(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *InvoiceSnapshotRepository) ListByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoiceSnapshot, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+snapCols+` FROM invoicesvc.invoice_snapshots
		 WHERE invoice_id = $1
		 ORDER BY snapshotted_at DESC`,
		invoiceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "snapshot.list", "list snapshots", err)
	}
	defer rows.Close()
	out := []domain.InvoiceSnapshot{}
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *InvoiceSnapshotRepository) FindLatestByInvoice(ctx context.Context, invoiceID uuid.UUID) (*domain.InvoiceSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+snapCols+` FROM invoicesvc.invoice_snapshots
		 WHERE invoice_id = $1
		 ORDER BY snapshotted_at DESC
		 LIMIT 1`,
		invoiceID)
	s, err := scanSnapshot(row)
	if stderrors.Is(err, pgx.ErrNoRows) || derrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *InvoiceSnapshotRepository) ExistsForInvoice(ctx context.Context, invoiceID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM invoicesvc.invoice_snapshots WHERE invoice_id = $1)`,
		invoiceID).Scan(&exists)
	if err != nil {
		return false, derrors.Wrap(derrors.KindInternal, "snapshot.exists", "check snapshot exists", err)
	}
	return exists, nil
}

func scanSnapshot(row pgx.Row) (domain.InvoiceSnapshot, error) {
	var (
		s              domain.InvoiceSnapshot
		lineRaw        string
		sourceStr      string
		snappedAt      time.Time
	)
	err := row.Scan(
		&s.ID, &s.InvoiceID, &s.CustomerID, &s.PlanID, &s.SchemaSnapshotID,
		&snappedAt, &s.TotalAmount, &lineRaw,
		&s.StatusAtSnapshot, &sourceStr,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceSnapshot{}, derrors.NotFound("snapshot.not_found", "snapshot not found")
	}
	if err != nil {
		return domain.InvoiceSnapshot{}, derrors.Wrap(derrors.KindInternal, "snapshot.scan", "scan snapshot", err)
	}
	s.SnapshottedAt = snappedAt
	s.SourceModule = domain.SourceModule(sourceStr)
	if lineRaw != "" {
		_ = json.Unmarshal([]byte(lineRaw), &s.LineItems)
	}
	if s.LineItems == nil {
		s.LineItems = []domain.SnapshotLineItem{}
	}
	return s, nil
}
