package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type DocumentRepository struct {
	pool *pgxpool.Pool
}

func NewDocumentRepository(pool *pgxpool.Pool) *DocumentRepository {
	return &DocumentRepository{pool: pool}
}

var _ port.DocumentRepository = (*DocumentRepository)(nil)

// Update patches a single document slot. Only the three writable fields are
// supported in round 1; sending only one (e.g. just `submitted=true`) is
// enough to satisfy a required slot, which the convert gate then accepts.
func (r *DocumentRepository) Update(ctx context.Context, id uuid.UUID, in port.UpdateDocumentInput) (*domain.OrderDocument, error) {
	// We read-modify-write so the caller can omit fields without losing them.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		SELECT id, lead_id, doc_key, label, required, submitted,
		       COALESCE(file_url,''), COALESCE(notes,''), created_at, updated_at
		  FROM crm.order_documents WHERE id = $1 FOR UPDATE
	`, id)
	var d domain.OrderDocument
	if err := row.Scan(&d.ID, &d.LeadID, &d.DocKey, &d.Label, &d.Required,
		&d.Submitted, &d.FileURL, &d.Notes, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("doc.not_found", "document not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.doc_get", "read doc", err)
	}

	if in.Submitted != nil {
		d.Submitted = *in.Submitted
	}
	if in.FileURL != nil {
		d.FileURL = *in.FileURL
	}
	if in.Notes != nil {
		d.Notes = *in.Notes
	}

	if _, err := tx.Exec(ctx, `
		UPDATE crm.order_documents
		   SET submitted = $2, file_url = $3, notes = $4, updated_at = NOW()
		 WHERE id = $1
	`, d.ID, d.Submitted, nullableString(d.FileURL), nullableString(d.Notes)); err != nil {
		return nil, mapDBError(err, "doc.update", "update document")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return &d, nil
}

func (r *DocumentRepository) ListForLead(ctx context.Context, leadID uuid.UUID) ([]domain.OrderDocument, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, lead_id, doc_key, label, required, submitted,
		       COALESCE(file_url,''), COALESCE(notes,''), created_at, updated_at
		  FROM crm.order_documents
		 WHERE lead_id = $1
		 ORDER BY required DESC, doc_key
	`, leadID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.doc_list", "list docs", err)
	}
	defer rows.Close()
	out := []domain.OrderDocument{}
	for rows.Next() {
		var d domain.OrderDocument
		if err := rows.Scan(&d.ID, &d.LeadID, &d.DocKey, &d.Label, &d.Required,
			&d.Submitted, &d.FileURL, &d.Notes, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.doc_scan", "scan doc", err)
		}
		out = append(out, d)
	}
	return out, nil
}
