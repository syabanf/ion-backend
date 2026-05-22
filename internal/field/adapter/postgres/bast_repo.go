package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type BASTRepository struct {
	pool *pgxpool.Pool
}

func NewBASTRepository(pool *pgxpool.Pool) *BASTRepository {
	return &BASTRepository{pool: pool}
}

var _ port.BASTRepository = (*BASTRepository)(nil)

func (r *BASTRepository) Create(ctx context.Context, b *domain.BAST) error {
	compiled := b.CompiledData
	if len(compiled) == 0 {
		compiled = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.bast_records (
			id, wo_id, customer_id, compiled_data,
			sign_off_mode, customer_sig_url, otp_used, otp_code, otp_verified_at,
			sign_off_at, sign_off_gps_lat, sign_off_gps_lng,
			submitted_by, submitted_at, noc_status
		) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`,
		b.ID, b.WOID, b.CustomerID, compiled,
		string(b.SignOffMode), nullableString(b.CustomerSigURL), b.OTPUsed,
		nullableString(b.OTPCode), b.OTPVerifiedAt,
		b.SignOffAt, b.SignOffGPSLat, b.SignOffGPSLng,
		b.SubmittedBy, b.SubmittedAt, string(b.NOCStatus),
	)
	return mapDBError(err, "bast.create", "create bast")
}

// VerifyOTP marks the OTP as verified by stamping otp_verified_at. The
// caller (service) has already checked the plaintext against otp_code.
func (r *BASTRepository) VerifyOTP(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE field.bast_records SET otp_verified_at = NOW() WHERE id = $1`,
		id)
	if err != nil {
		return mapDBError(err, "bast.otp_verify", "mark otp verified")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bast.not_found", "bast not found")
	}
	return nil
}

const bastSelect = `
SELECT id, wo_id, customer_id, compiled_data,
       sign_off_mode, COALESCE(customer_sig_url,''), otp_used, COALESCE(otp_code,''), otp_verified_at,
       sign_off_at, sign_off_gps_lat, sign_off_gps_lng,
       submitted_by, submitted_at, noc_status,
       noc_verified_by, noc_verified_at, COALESCE(noc_notes,'')
FROM field.bast_records
`

func (r *BASTRepository) FindActiveForWO(ctx context.Context, woID uuid.UUID) (*domain.BAST, error) {
	row := r.pool.QueryRow(ctx, bastSelect+`
		WHERE wo_id = $1 AND noc_status <> 'rejected'
		LIMIT 1
	`, woID)
	return scanBAST(row)
}

func (r *BASTRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BAST, error) {
	row := r.pool.QueryRow(ctx, bastSelect+" WHERE id = $1", id)
	return scanBAST(row)
}

func (r *BASTRepository) MarkVerified(ctx context.Context, id uuid.UUID, status domain.NOCStatus, by uuid.UUID, notes string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE field.bast_records
		   SET noc_status = $2,
		       noc_verified_by = $3,
		       noc_verified_at = NOW(),
		       noc_notes = $4
		 WHERE id = $1
	`, id, string(status), by, nullableString(notes))
	if err != nil {
		return mapDBError(err, "bast.verify", "mark bast verified")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bast.not_found", "bast not found")
	}
	return nil
}

func scanBAST(row pgx.Row) (*domain.BAST, error) {
	var (
		b      domain.BAST
		mode   string
		status string
	)
	err := row.Scan(&b.ID, &b.WOID, &b.CustomerID, &b.CompiledData,
		&mode, &b.CustomerSigURL, &b.OTPUsed, &b.OTPCode, &b.OTPVerifiedAt,
		&b.SignOffAt, &b.SignOffGPSLat, &b.SignOffGPSLng,
		&b.SubmittedBy, &b.SubmittedAt, &status,
		&b.NOCVerifiedBy, &b.NOCVerifiedAt, &b.NOCNotes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // signals "no active BAST" — service treats this as benign
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.bast_scan", "scan bast", err)
	}
	b.SignOffMode = domain.SignOffMode(mode)
	b.NOCStatus = domain.NOCStatus(status)
	return &b, nil
}
