package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AuditRepository struct {
	pool *pgxpool.Pool
}

func NewAuditRepository(pool *pgxpool.Pool) *AuditRepository {
	return &AuditRepository{pool: pool}
}

var _ port.AuditRepository = (*AuditRepository)(nil)

// List paginates audit entries with optional filters. Returns (rows, total).
func (r *AuditRepository) List(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEntry, int, error) {
	// Build the WHERE clause dynamically. We use positional args; the order
	// must match between the count query and the list query.
	conditions := []string{"1=1"}
	args := []any{}
	idx := 1

	if f.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("al.user_id = $%d", idx))
		args = append(args, *f.UserID)
		idx++
	}
	if f.Module != "" {
		conditions = append(conditions, fmt.Sprintf("al.module = $%d", idx))
		args = append(args, f.Module)
		idx++
	}
	if f.RecordType != "" {
		conditions = append(conditions, fmt.Sprintf("al.record_type = $%d", idx))
		args = append(args, f.RecordType)
		idx++
	}
	if f.From != nil {
		conditions = append(conditions, fmt.Sprintf("al.timestamp >= $%d", idx))
		args = append(args, *f.From)
		idx++
	}
	if f.To != nil {
		conditions = append(conditions, fmt.Sprintf("al.timestamp <= $%d", idx))
		args = append(args, *f.To)
		idx++
	}

	where := strings.Join(conditions, " AND ")

	// Total
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.audit_logs al WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.audit_count", "count audit", err)
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	listSQL := `
		SELECT al.id, al.timestamp, al.user_id, COALESCE(u.full_name, '(system)') AS user_name,
		       al.module, al.record_type, al.record_id,
		       COALESCE(al.field_changed, ''), COALESCE(al.before_value, ''),
		       COALESCE(al.after_value, ''), COALESCE(al.description, ''),
		       COALESCE(al.reason, '')
		FROM identity.audit_logs al
		LEFT JOIN identity.users u ON u.id = al.user_id
		WHERE ` + where + `
		ORDER BY al.timestamp DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)

	args = append(args, f.Limit, f.Offset)

	rows, err := r.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.audit_list", "list audit", err)
	}
	defer rows.Close()

	out := []domain.AuditEntry{}
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(
			&e.ID, &e.Timestamp, &e.UserID, &e.UserFullName,
			&e.Module, &e.RecordType, &e.RecordID,
			&e.FieldChanged, &e.Before, &e.After, &e.Description, &e.Reason,
		); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "db.audit_scan", "scan audit", err)
		}
		out = append(out, e)
	}
	return out, total, nil
}
