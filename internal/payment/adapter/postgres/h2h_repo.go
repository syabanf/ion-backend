package postgres

import (
	"context"
	stderrors "errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// H2HRepository implements port.H2HRepository against
// `payment.h2h_bank_statements` + `payment.h2h_bank_lines`.
//
// One repo file handles both tables because the lines are tightly
// coupled to their parent statement (ON DELETE CASCADE) and the use
// cases always operate on a statement + its lines together.
type H2HRepository struct {
	pool *pgxpool.Pool
}

func NewH2HRepository(pool *pgxpool.Pool) *H2HRepository {
	return &H2HRepository{pool: pool}
}

var _ port.H2HRepository = (*H2HRepository)(nil)

const statementCols = `
	id, gateway_id, statement_date, raw_filename, raw_hash,
	line_count, matched_count, unmatched_count, status,
	created_at, completed_at
`

const lineCols = `
	id, statement_id, raw_line::text, amount, value_date,
	COALESCE(reference_text, ''), payment_intent_id,
	match_confidence, COALESCE(match_method, ''),
	created_at
`

func (r *H2HRepository) CreateStatement(ctx context.Context, s *domain.H2HBankStatement) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payment.h2h_bank_statements
			(id, gateway_id, statement_date, raw_filename, raw_hash,
			 line_count, matched_count, unmatched_count, status,
			 created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		s.ID, s.GatewayID, s.StatementDate, s.RawFilename, s.RawHash,
		s.LineCount, s.MatchedCount, s.UnmatchedCount, string(s.Status),
		s.CreatedAt, s.CompletedAt,
	)
	return mapDBError(err, "h2h_statement", "insert h2h statement")
}

func (r *H2HRepository) FindStatementByID(ctx context.Context, id uuid.UUID) (*domain.H2HBankStatement, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+statementCols+` FROM payment.h2h_bank_statements WHERE id = $1`, id)
	s, err := scanStatement(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *H2HRepository) FindStatementByHash(ctx context.Context, gatewayID uuid.UUID, hash string) (*domain.H2HBankStatement, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+statementCols+` FROM payment.h2h_bank_statements WHERE gateway_id = $1 AND raw_hash = $2`,
		gatewayID, hash,
	)
	s, err := scanStatement(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *H2HRepository) ListStatements(ctx context.Context, limit, offset int) ([]domain.H2HBankStatement, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM payment.h2h_bank_statements`,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.h2h_statement_count", "count statements", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+statementCols+`
		FROM payment.h2h_bank_statements
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.h2h_statement_list", "list statements", err)
	}
	defer rows.Close()
	out := []domain.H2HBankStatement{}
	for rows.Next() {
		s, err := scanStatement(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func (r *H2HRepository) UpdateStatement(ctx context.Context, s *domain.H2HBankStatement) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.h2h_bank_statements
		SET statement_date = $2,
		    line_count = $3,
		    matched_count = $4,
		    unmatched_count = $5,
		    status = $6,
		    completed_at = $7
		WHERE id = $1
	`,
		s.ID, s.StatementDate, s.LineCount, s.MatchedCount,
		s.UnmatchedCount, string(s.Status), s.CompletedAt,
	)
	if err != nil {
		return mapDBError(err, "h2h_statement", "update h2h statement")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("h2h_statement.not_found", "h2h statement not found")
	}
	return nil
}

func (r *H2HRepository) InsertLines(ctx context.Context, statementID uuid.UUID, lines []domain.H2HBankLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.h2h_lines_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO payment.h2h_bank_lines
				(id, statement_id, raw_line, amount, value_date,
				 reference_text, payment_intent_id, match_confidence,
				 match_method, created_at)
			VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10)
		`,
			l.ID, statementID, string(l.RawLine), l.Amount, l.ValueDate,
			nullableString(l.ReferenceText), l.PaymentIntentID, l.MatchConfidence,
			nullableString(l.MatchMethod), l.CreatedAt,
		)
		if err != nil {
			return mapDBError(err, "h2h_line", "insert h2h line")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.h2h_lines_commit", "commit h2h lines tx", err)
	}
	return nil
}

func (r *H2HRepository) ListLinesForStatement(ctx context.Context, statementID uuid.UUID) ([]domain.H2HBankLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+lineCols+`
		FROM payment.h2h_bank_lines
		WHERE statement_id = $1
		ORDER BY value_date ASC, id ASC
	`, statementID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.h2h_lines_list", "list lines", err)
	}
	defer rows.Close()
	out := []domain.H2HBankLine{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *H2HRepository) UpdateLineMatch(ctx context.Context, l *domain.H2HBankLine) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.h2h_bank_lines
		SET payment_intent_id = $2,
		    match_confidence = $3,
		    match_method = $4
		WHERE id = $1
	`,
		l.ID, l.PaymentIntentID, l.MatchConfidence, nullableString(l.MatchMethod),
	)
	if err != nil {
		return mapDBError(err, "h2h_line", "update h2h line match")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("h2h_line.not_found", "h2h line not found")
	}
	return nil
}

func (r *H2HRepository) ListUnmatchedLines(ctx context.Context, statementID uuid.UUID) ([]domain.H2HBankLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+lineCols+`
		FROM payment.h2h_bank_lines
		WHERE statement_id = $1 AND payment_intent_id IS NULL
		ORDER BY value_date ASC, id ASC
	`, statementID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.h2h_unmatched", "list unmatched lines", err)
	}
	defer rows.Close()
	out := []domain.H2HBankLine{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func scanStatement(row pgx.Row) (domain.H2HBankStatement, error) {
	var s domain.H2HBankStatement
	var status string
	err := row.Scan(
		&s.ID, &s.GatewayID, &s.StatementDate, &s.RawFilename, &s.RawHash,
		&s.LineCount, &s.MatchedCount, &s.UnmatchedCount, &status,
		&s.CreatedAt, &s.CompletedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.H2HBankStatement{}, derrors.NotFound(
			"h2h_statement.not_found", "h2h statement not found")
	}
	if err != nil {
		return domain.H2HBankStatement{}, derrors.Wrap(
			derrors.KindInternal, "db.h2h_statement_scan", "scan statement", err)
	}
	s.Status = domain.H2HStatementStatus(status)
	return s, nil
}

func scanLine(row pgx.Row) (domain.H2HBankLine, error) {
	var l domain.H2HBankLine
	var rawLine string
	err := row.Scan(
		&l.ID, &l.StatementID, &rawLine, &l.Amount, &l.ValueDate,
		&l.ReferenceText, &l.PaymentIntentID,
		&l.MatchConfidence, &l.MatchMethod,
		&l.CreatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.H2HBankLine{}, derrors.NotFound(
			"h2h_line.not_found", "h2h line not found")
	}
	if err != nil {
		return domain.H2HBankLine{}, derrors.Wrap(
			derrors.KindInternal, "db.h2h_line_scan", "scan line", err)
	}
	l.RawLine = []byte(rawLine)
	return l, nil
}
