package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SupplierRepository implements `port.SupplierRepository` against
// `warehouse.suppliers` (migration 0022). The shape mirrors
// WarehouseRepository — small surface (List/FindByID/FindByCode/
// Create/Update), the partial Update path handled in a single
// dynamic-SET query so we don't need a getter+merge round-trip.
type SupplierRepository struct {
	pool *pgxpool.Pool
}

func NewSupplierRepository(pool *pgxpool.Pool) *SupplierRepository {
	return &SupplierRepository{pool: pool}
}

var _ port.SupplierRepository = (*SupplierRepository)(nil)

const supplierCols = `id, code, company_name,
	COALESCE(contact_person,''),
	COALESCE(phone,''),
	COALESCE(email,''),
	COALESCE(address,''),
	COALESCE(payment_terms,''),
	COALESCE(npwp,''),
	COALESCE(nib,''),
	COALESCE(category_tags, '{}'::text[]),
	COALESCE(notes,''),
	active, onboarded_at, created_at, updated_at`

func (r *SupplierRepository) List(ctx context.Context, f port.SupplierListFilter) ([]domain.Supplier, int, error) {
	// Build the WHERE clause defensively — we accumulate predicates so
	// callers don't have to know the SQL.
	var wh []string
	var args []any
	if f.ActiveOnly && !f.IncludeInactive {
		wh = append(wh, "active = TRUE")
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		// Case-insensitive prefix-ish match across company_name + code.
		// ILIKE is fine here — the table will rarely exceed a few
		// thousand rows; an FTS index would be overkill for Phase 1.
		args = append(args, "%"+s+"%")
		wh = append(wh, fmt.Sprintf("(company_name ILIKE $%d OR code ILIKE $%d)", len(args), len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	// Count first so the FE can paginate. Cheap — a count over a
	// few-thousand-row table is sub-millisecond.
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.suppliers`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.supplier_count", "count suppliers", err)
	}

	// Then the page itself.
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + supplierCols + ` FROM warehouse.suppliers` + where +
		` ORDER BY company_name LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.supplier_list", "list suppliers", err)
	}
	defer rows.Close()

	out := []domain.Supplier{}
	for rows.Next() {
		s, err := scanSupplier(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func (r *SupplierRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Supplier, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+supplierCols+` FROM warehouse.suppliers WHERE id = $1`, id)
	s, err := scanSupplier(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SupplierRepository) FindByCode(ctx context.Context, code string) (*domain.Supplier, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+supplierCols+` FROM warehouse.suppliers WHERE code = $1`, code)
	s, err := scanSupplier(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SupplierRepository) Create(ctx context.Context, s *domain.Supplier) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.suppliers
			(id, code, company_name, contact_person, phone, email, address,
			 payment_terms, npwp, nib, category_tags, notes,
			 active, onboarded_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		s.ID, s.Code, s.CompanyName, s.ContactPerson, s.Phone, s.Email, s.Address,
		s.PaymentTerms, s.NPWP, s.NIB, s.CategoryTags, s.Notes,
		s.Active, s.OnboardedAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "supplier", "insert supplier")
	}
	return nil
}

func (r *SupplierRepository) Update(ctx context.Context, in port.UpdateSupplierInput) (*domain.Supplier, error) {
	// Dynamic SET — only the fields the caller provided. We always
	// touch updated_at so the timestamp tracks the last edit.
	sets := []string{"updated_at = NOW()"}
	var args []any
	push := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.CompanyName != nil {
		push("company_name", *in.CompanyName)
	}
	if in.ContactPerson != nil {
		push("contact_person", *in.ContactPerson)
	}
	if in.Phone != nil {
		push("phone", *in.Phone)
	}
	if in.Email != nil {
		push("email", *in.Email)
	}
	if in.Address != nil {
		push("address", *in.Address)
	}
	if in.PaymentTerms != nil {
		push("payment_terms", *in.PaymentTerms)
	}
	if in.NPWP != nil {
		push("npwp", *in.NPWP)
	}
	if in.NIB != nil {
		push("nib", *in.NIB)
	}
	if in.CategoryTags != nil {
		push("category_tags", *in.CategoryTags)
	}
	if in.Notes != nil {
		push("notes", *in.Notes)
	}
	if in.Active != nil {
		push("active", *in.Active)
	}

	// id parameter goes last so its position is stable.
	args = append(args, in.ID)
	sql := `UPDATE warehouse.suppliers SET ` + strings.Join(sets, ", ") +
		` WHERE id = $` + fmt.Sprint(len(args)) +
		` RETURNING ` + supplierCols

	row := r.pool.QueryRow(ctx, sql, args...)
	s, err := scanSupplier(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// scanSupplier centralizes the column→struct mapping so the column
// order stays in one place (matches `supplierCols`).
func scanSupplier(row pgx.Row) (domain.Supplier, error) {
	var s domain.Supplier
	err := row.Scan(
		&s.ID, &s.Code, &s.CompanyName,
		&s.ContactPerson, &s.Phone, &s.Email, &s.Address,
		&s.PaymentTerms, &s.NPWP, &s.NIB,
		&s.CategoryTags, &s.Notes,
		&s.Active, &s.OnboardedAt, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Supplier{}, derrors.NotFound("supplier.not_found", "supplier not found")
	}
	if err != nil {
		return domain.Supplier{}, derrors.Wrap(derrors.KindInternal, "db.supplier_scan", "scan supplier", err)
	}
	return s, nil
}
