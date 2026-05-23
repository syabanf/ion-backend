package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// FakturPajakRepository implements `port.FakturPajakRepository`
// against `tax.faktur_pajak_records`.
type FakturPajakRepository struct {
	pool *pgxpool.Pool
}

func NewFakturPajakRepository(pool *pgxpool.Pool) *FakturPajakRepository {
	return &FakturPajakRepository{pool: pool}
}

var _ port.FakturPajakRepository = (*FakturPajakRepository)(nil)

const fakturCols = `
	id, invoice_id, subsidiary_id,
	COALESCE(nomor_seri,''),
	jenis_faktur, tanggal_faktur,
	COALESCE(npwp_lawan_transaksi,''),
	dpp, ppn, status,
	djp_response_payload,
	created_at, updated_at,
	tax_snapshot_hash, dpp_decoded
`

func (r *FakturPajakRepository) Create(ctx context.Context, f *domain.FakturPajak) error {
	var nomorSeri any
	if f.NomorSeri == "" {
		nomorSeri = nil
	} else {
		nomorSeri = f.NomorSeri
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tax.faktur_pajak_records
			(id, invoice_id, subsidiary_id, nomor_seri,
			 jenis_faktur, tanggal_faktur, npwp_lawan_transaksi,
			 dpp, ppn, status, djp_response_payload,
			 created_at, updated_at,
			 tax_snapshot_hash, dpp_decoded)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`,
		f.ID, f.InvoiceID, f.SubsidiaryID, nomorSeri,
		string(f.JenisFaktur), f.TanggalFaktur, f.NPWPLawanTransaksi,
		f.DPP, f.PPN, string(f.Status), f.DJPResponsePayload,
		f.CreatedAt, f.UpdatedAt,
		f.TaxSnapshotHash, f.DPPDecoded,
	)
	if err != nil {
		return mapDBError(err, "faktur", "insert faktur pajak record")
	}
	return nil
}

func (r *FakturPajakRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FakturPajak, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+fakturCols+` FROM tax.faktur_pajak_records WHERE id = $1`, id)
	f, err := scanFaktur(row)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateStatus persists status + nomor_seri + tanggal_faktur + djp
// payload. The usecase layer is responsible for calling TransitionTo
// before invoking this — the repo just flushes.
func (r *FakturPajakRepository) UpdateStatus(ctx context.Context, f *domain.FakturPajak) error {
	var nomorSeri any
	if f.NomorSeri == "" {
		nomorSeri = nil
	} else {
		nomorSeri = f.NomorSeri
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE tax.faktur_pajak_records
		SET status = $2,
		    nomor_seri = $3,
		    tanggal_faktur = $4,
		    djp_response_payload = $5,
		    tax_snapshot_hash = $6,
		    dpp_decoded = $7,
		    updated_at = NOW()
		WHERE id = $1
	`,
		f.ID, string(f.Status), nomorSeri, f.TanggalFaktur, f.DJPResponsePayload,
		f.TaxSnapshotHash, f.DPPDecoded,
	)
	if err != nil {
		return mapDBError(err, "faktur", "update faktur pajak status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("faktur.not_found", "faktur pajak not found")
	}
	return nil
}

func (r *FakturPajakRepository) FindByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.FakturPajak, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+fakturCols+`
		 FROM tax.faktur_pajak_records
		 WHERE invoice_id = $1
		 ORDER BY created_at DESC`,
		invoiceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"db.faktur_by_invoice", "list faktur by invoice", err)
	}
	defer rows.Close()

	out := []domain.FakturPajak{}
	for rows.Next() {
		f, err := scanFaktur(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func scanFaktur(row pgx.Row) (domain.FakturPajak, error) {
	var f domain.FakturPajak
	var jenis, status string
	err := row.Scan(
		&f.ID, &f.InvoiceID, &f.SubsidiaryID, &f.NomorSeri,
		&jenis, &f.TanggalFaktur, &f.NPWPLawanTransaksi,
		&f.DPP, &f.PPN, &status, &f.DJPResponsePayload,
		&f.CreatedAt, &f.UpdatedAt,
		&f.TaxSnapshotHash, &f.DPPDecoded,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FakturPajak{}, derrors.NotFound(
			"faktur.not_found", "faktur pajak not found")
	}
	if err != nil {
		return domain.FakturPajak{}, derrors.Wrap(derrors.KindInternal,
			"db.faktur_scan", "scan faktur pajak", err)
	}
	f.JenisFaktur = domain.JenisFaktur(jenis)
	f.Status = domain.FakturStatus(status)
	return f, nil
}
