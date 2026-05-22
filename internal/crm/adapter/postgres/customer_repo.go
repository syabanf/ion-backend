package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/cryptutil"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type CustomerRepository struct {
	pool   *pgxpool.Pool
	sealer *cryptutil.Sealer // optional; when set, NIK is stored encrypted
}

func NewCustomerRepository(pool *pgxpool.Pool) *CustomerRepository {
	return &CustomerRepository{pool: pool}
}

// WithSealer enables at-rest encryption of NIK. New rows write the
// ciphertext into `nik_encrypted` and leave the plain `nik` column
// NULL; reads decrypt the bytes when the sealer is wired, falling
// back to the plain column for rows that haven't been backfilled yet.
func (r *CustomerRepository) WithSealer(s *cryptutil.Sealer) *CustomerRepository {
	r.sealer = s
	return r
}

var _ port.CustomerRepository = (*CustomerRepository)(nil)

// Round-3: migration 0018 dropped the plaintext `nik` column. Reads +
// writes now go exclusively through `nik_encrypted`. A repo without a
// sealer wired refuses to persist NIK rather than silently losing it.
const customerSelect = `
SELECT id, customer_number, customer_type, full_name, phone,
       COALESCE(email,''), nik_encrypted,
       address, gps_lat, gps_lng,
       branch_id, installation_node_id, status, created_at, updated_at
FROM crm.customers
`

func (r *CustomerRepository) Create(ctx context.Context, c *domain.Customer) error {
	encNIK, err := r.sealNIK(c.NIK)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO crm.customers (
			id, customer_number, customer_type, full_name, phone, email, nik_encrypted,
			address, gps_lat, gps_lng, branch_id, installation_node_id, status,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
	`,
		c.ID, c.CustomerNumber, string(c.CustomerType), c.FullName, c.Phone,
		nullableString(c.Email), encNIK,
		c.Address, c.GPSLat, c.GPSLng,
		c.BranchID, c.InstallationNodeID, string(c.Status), c.CreatedAt,
	)
	return mapDBError(err, "customer.create", "create customer")
}

// sealNIK encrypts a NIK for storage. Empty plaintext writes NULL.
// Without a sealer the repo refuses — KTP_ENC_KEY must be wired for
// any binary that creates customer rows.
func (r *CustomerRepository) sealNIK(nik string) ([]byte, error) {
	if nik == "" {
		return nil, nil
	}
	if r.sealer == nil {
		return nil, derrors.New(derrors.KindInternal,
			"customer.ktp_sealer_missing",
			"KTP_ENC_KEY is required to persist NIK")
	}
	return r.sealer.Seal(nik)
}

func (r *CustomerRepository) List(ctx context.Context, status string, limit, offset int) ([]domain.Customer, int, error) {
	args := []any{}
	where := ""
	if status != "" {
		where = " WHERE status = $1"
		args = append(args, status)
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM crm.customers"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.customer_count", "count customers", err)
	}
	if limit <= 0 {
		limit = 50
	}
	sql := customerSelect + where + " ORDER BY created_at DESC LIMIT $" + itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.customer_list", "list customers", err)
	}
	defer rows.Close()
	out := []domain.Customer{}
	for rows.Next() {
		c, err := r.scanCustomer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, nil
}

func (r *CustomerRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Customer, error) {
	row := r.pool.QueryRow(ctx, customerSelect+" WHERE id = $1", id)
	return r.scanCustomer(row)
}

// scanCustomer is the single scan path post-0018. We only read
// `nik_encrypted`; the plaintext column is gone. When the bytea is
// empty (legacy rows that were already NULL before 0017 or rows
// written by binaries without a sealer) we leave NIK as empty string.
func (r *CustomerRepository) scanCustomer(row pgx.Row) (*domain.Customer, error) {
	var (
		c         domain.Customer
		ctype, st string
		encNIK    []byte
	)
	err := row.Scan(&c.ID, &c.CustomerNumber, &ctype, &c.FullName, &c.Phone,
		&c.Email, &encNIK,
		&c.Address, &c.GPSLat, &c.GPSLng,
		&c.BranchID, &c.InstallationNodeID, &st, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("customer.not_found", "customer not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.customer_scan", "scan customer", err)
	}
	c.CustomerType = domain.CustomerType(ctype)
	c.Status = domain.CustomerStatus(st)
	if len(encNIK) > 0 && r.sealer != nil {
		if dec, err := r.sealer.Open(encNIK); err == nil {
			c.NIK = dec
		}
		// On decrypt failure we keep whatever plain column had —
		// usually empty for sealed rows, but the surface remains
		// graceful instead of throwing on the read path.
	}
	return &c, nil
}
