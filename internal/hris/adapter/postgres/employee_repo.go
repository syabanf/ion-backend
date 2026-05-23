package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// EmployeeRepository implements port.EmployeeRepository against
// `hris.employees`.
type EmployeeRepository struct {
	pool *pgxpool.Pool
}

func NewEmployeeRepository(pool *pgxpool.Pool) *EmployeeRepository {
	return &EmployeeRepository{pool: pool}
}

var _ port.EmployeeRepository = (*EmployeeRepository)(nil)

const employeeCols = `
	id, employee_no, full_name,
	COALESCE(email, ''), COALESCE(phone, ''),
	COALESCE(department, ''), COALESCE(position, ''),
	COALESCE(manager_employee_no, ''),
	hire_date, resign_date,
	status,
	kyc_completed,
	COALESCE(npwp, ''), COALESCE(bank_account_no, ''),
	branch_id,
	COALESCE(role_recommendations, '[]'::jsonb),
	created_at, updated_at
`

// Upsert inserts a new employee row, or updates the existing one keyed
// on employee_no. The id stays stable across updates — callers can hold
// a UUID reference forever.
func (r *EmployeeRepository) Upsert(ctx context.Context, e *domain.Employee) error {
	roleRecs, err := json.Marshal(e.RoleRecommendations)
	if err != nil {
		roleRecs = []byte("[]")
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO hris.employees
			(id, employee_no, full_name, email, phone, department, position,
			 manager_employee_no, hire_date, resign_date, status,
			 kyc_completed, npwp, bank_account_no, branch_id,
			 role_recommendations, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (employee_no) DO UPDATE
		SET full_name = EXCLUDED.full_name,
			email = EXCLUDED.email,
			phone = EXCLUDED.phone,
			department = EXCLUDED.department,
			position = EXCLUDED.position,
			manager_employee_no = EXCLUDED.manager_employee_no,
			hire_date = EXCLUDED.hire_date,
			resign_date = EXCLUDED.resign_date,
			status = EXCLUDED.status,
			kyc_completed = EXCLUDED.kyc_completed,
			npwp = EXCLUDED.npwp,
			bank_account_no = EXCLUDED.bank_account_no,
			branch_id = EXCLUDED.branch_id,
			role_recommendations = EXCLUDED.role_recommendations,
			updated_at = EXCLUDED.updated_at
	`,
		e.ID, e.EmployeeNo, e.FullName,
		nullableString(e.Email), nullableString(e.Phone),
		nullableString(e.Department), nullableString(e.Position),
		nullableString(e.ManagerEmployeeNo),
		e.HireDate, e.ResignDate, string(e.Status),
		e.KYCCompleted, nullableString(e.NPWP), nullableString(e.BankAccountNo),
		e.BranchID, roleRecs, e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "hris.employee", "upsert employee")
	}
	return nil
}

func (r *EmployeeRepository) FindByEmployeeNo(ctx context.Context, employeeNo string) (*domain.Employee, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+employeeCols+` FROM hris.employees WHERE employee_no = $1`,
		strings.TrimSpace(employeeNo),
	)
	e, err := scanEmployee(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

func (r *EmployeeRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Employee, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+employeeCols+` FROM hris.employees WHERE id = $1`, id,
	)
	e, err := scanEmployee(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

func (r *EmployeeRepository) List(ctx context.Context, f port.EmployeeFilter) ([]domain.Employee, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, string(f.Status))
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		wh = append(wh, fmt.Sprintf("branch_id = $%d", len(args)))
	}
	if f.Department != "" {
		args = append(args, f.Department)
		wh = append(wh, fmt.Sprintf("department = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		idx := len(args)
		wh = append(wh, fmt.Sprintf(
			"(LOWER(employee_no) LIKE $%d OR LOWER(full_name) LIKE $%d OR LOWER(COALESCE(email,'')) LIKE $%d)",
			idx, idx, idx,
		))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit)
	limitIdx := len(args)
	args = append(args, offset)
	offsetIdx := len(args)

	query := `SELECT ` + employeeCols + ` FROM hris.employees` + where +
		` ORDER BY updated_at DESC, employee_no ASC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", limitIdx, offsetIdx)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, mapDBError(err, "hris.employee", "list employees")
	}
	defer rows.Close()
	var out []domain.Employee
	for rows.Next() {
		e, err := scanEmployee(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapDBError(err, "hris.employee", "iterate employees")
	}
	// Total count — separate scalar query.
	var total int
	countQuery := "SELECT COUNT(*) FROM hris.employees" + where
	// reuse original args minus the limit/offset
	countArgs := args[:len(args)-2]
	if err := r.pool.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, mapDBError(err, "hris.employee", "count employees")
	}
	return out, total, nil
}

// scanRow is the common interface satisfied by both pgx.Row and pgx.Rows.
type scanRow interface {
	Scan(dest ...any) error
}

func scanEmployee(r scanRow) (domain.Employee, error) {
	var e domain.Employee
	var status string
	var roleRecs []byte
	if err := r.Scan(
		&e.ID, &e.EmployeeNo, &e.FullName,
		&e.Email, &e.Phone, &e.Department, &e.Position,
		&e.ManagerEmployeeNo,
		&e.HireDate, &e.ResignDate,
		&status,
		&e.KYCCompleted,
		&e.NPWP, &e.BankAccountNo,
		&e.BranchID,
		&roleRecs,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return e, err
		}
		return e, derrors.Wrap(derrors.KindInternal, "hris.employee", "scan employee row", err)
	}
	e.Status = domain.EmployeeStatus(status)
	if len(roleRecs) > 0 {
		var arr []string
		if err := json.Unmarshal(roleRecs, &arr); err == nil {
			e.RoleRecommendations = arr
		}
	}
	return e, nil
}
