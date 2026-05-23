// Package postgres — Wave 104 audit query + hash-chain verification.
//
// Extends the existing writer with a read-side surface:
//
//   - Query — list audit entries by subject_type / subject_id / date range
//   - VerifyChain — walk rows in created_at order, recompute row_hash from
//     (prev_hash || payload) and compare to the stored value
//
// The query API closes TC-AU-007 (date-range filter) and the verifier
// closes TC-AU-008 (hash chain) from the Wave-91 audit catalog.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Read-side projection — mirrors identity.audit_logs columns plus the
// Wave 105 hash-chain pair.
// =====================================================================

// QueryEntry is the audit-log row shape returned by Query. The polymorphic
// `subject_*` columns map to the `record_*` columns at the DB layer; the
// rename is a Wave-104 read-side convenience so the API surface speaks
// the audit-doc vocabulary.
type QueryEntry struct {
	ID           uuid.UUID  `json:"id"`
	Timestamp    time.Time  `json:"timestamp"`
	UserID       *uuid.UUID `json:"user_id,omitempty"`
	Module       string     `json:"module"`
	SubjectType  string     `json:"subject_type"`
	SubjectID    string     `json:"subject_id"`
	FieldChanged string     `json:"field_changed,omitempty"`
	Before       string     `json:"before,omitempty"`
	After        string     `json:"after,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	PrevHash     string     `json:"prev_hash"`
	RowHash      string     `json:"row_hash"`
}

// QueryFilter narrows the Query call. All fields optional; an empty
// filter returns the most recent `Limit` rows.
type QueryFilter struct {
	SubjectType string
	SubjectID   string
	Module      string
	UserID      *uuid.UUID
	From        *time.Time
	To          *time.Time
	Limit       int
	Offset      int
}

// ChainVerifyResult is the output of VerifyChain. Verified counts rows
// whose row_hash matches the recomputed value; Broken counts the rest.
// FirstBrokenID is set to the first divergent row encountered when
// walking by created_at ASC, so the operator can pin the tampered
// region.
type ChainVerifyResult struct {
	Verified      int        `json:"verified"`
	Broken        int        `json:"broken"`
	Total         int        `json:"total"`
	FirstBrokenID *uuid.UUID `json:"first_broken_id,omitempty"`
}

// Reader is the read-side companion to Writer. Operates against the same
// pgx pool; the two halves can share a pool because audit traffic is low.
type Reader struct {
	pool *pgxpool.Pool
}

func NewReader(pool *pgxpool.Pool) *Reader {
	return &Reader{pool: pool}
}

// Query — paginated audit-entry list with optional subject + date-range
// filters. Returns rows ordered by timestamp DESC.
func (r *Reader) Query(ctx context.Context, f QueryFilter) ([]QueryEntry, error) {
	if r == nil || r.pool == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "audit.not_configured",
			"audit reader has no pgx pool", nil)
	}

	conditions := []string{"1=1"}
	args := []any{}
	idx := 1

	if f.SubjectType != "" {
		conditions = append(conditions, fmt.Sprintf("record_type = $%d", idx))
		args = append(args, f.SubjectType)
		idx++
	}
	if f.SubjectID != "" {
		conditions = append(conditions, fmt.Sprintf("record_id = $%d", idx))
		args = append(args, f.SubjectID)
		idx++
	}
	if f.Module != "" {
		conditions = append(conditions, fmt.Sprintf("module = $%d", idx))
		args = append(args, f.Module)
		idx++
	}
	if f.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", idx))
		args = append(args, *f.UserID)
		idx++
	}
	if f.From != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", idx))
		args = append(args, *f.From)
		idx++
	}
	if f.To != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", idx))
		args = append(args, *f.To)
		idx++
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	sql := `
		SELECT id, timestamp, user_id, module, record_type, record_id,
		       COALESCE(field_changed, ''), COALESCE(before_value, ''),
		       COALESCE(after_value, ''), COALESCE(reason, ''),
		       COALESCE(prev_hash, ''), COALESCE(row_hash, '')
		FROM identity.audit_logs
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY timestamp DESC, id DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "audit.query", "list audit", err)
	}
	defer rows.Close()

	out := []QueryEntry{}
	for rows.Next() {
		var e QueryEntry
		var uid *uuid.UUID
		if err := rows.Scan(
			&e.ID, &e.Timestamp, &uid,
			&e.Module, &e.SubjectType, &e.SubjectID,
			&e.FieldChanged, &e.Before, &e.After, &e.Reason,
			&e.PrevHash, &e.RowHash,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "audit.scan", "scan audit", err)
		}
		e.UserID = uid
		out = append(out, e)
	}
	return out, nil
}

// VerifyChain walks the audit_logs table in (timestamp ASC, id ASC) order
// across the supplied window, recomputes each row's row_hash from the
// canonical (prev_hash || payload-json) digest, and counts matches vs
// divergences. Backfilled rows pre-Wave-105 (prev_hash='' AND row_hash='')
// are SKIPPED — the chain starts from the first row carrying a non-empty
// row_hash. This matches the migration's documented "back-fill is best-
// effort" semantics.
//
// The verifier does not modify rows. If a row's stored row_hash differs
// from the recomputed value, FirstBrokenID is set (idempotent: only the
// first divergence wins) and Broken increments. Verified counts good
// rows.
func (r *Reader) VerifyChain(ctx context.Context, from, to time.Time) (ChainVerifyResult, error) {
	if r == nil || r.pool == nil {
		return ChainVerifyResult{}, derrors.Wrap(derrors.KindInternal, "audit.not_configured",
			"audit reader has no pgx pool", nil)
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return ChainVerifyResult{}, derrors.Validation("audit.verify.range_invalid",
			"to must be >= from")
	}

	conditions := []string{"row_hash <> ''"}
	args := []any{}
	idx := 1
	if !from.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", idx))
		args = append(args, from)
		idx++
	}
	if !to.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", idx))
		args = append(args, to)
		idx++
	}

	sql := `
		SELECT id, timestamp, user_id, module, record_type, record_id,
		       field_changed, before_value, after_value, reason,
		       COALESCE(prev_hash, ''), COALESCE(row_hash, '')
		FROM identity.audit_logs
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY timestamp ASC, id ASC
	`
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return ChainVerifyResult{}, derrors.Wrap(derrors.KindInternal, "audit.verify.query",
			"verify chain query", err)
	}
	defer rows.Close()

	result := ChainVerifyResult{}
	for rows.Next() {
		var (
			id            uuid.UUID
			ts            time.Time
			uid           *uuid.UUID
			module        string
			recordType    string
			recordID      string
			fieldChanged  *string
			beforeValue   *string
			afterValue    *string
			reason        *string
			prevHash      string
			rowHash       string
		)
		if err := rows.Scan(&id, &ts, &uid, &module, &recordType, &recordID,
			&fieldChanged, &beforeValue, &afterValue, &reason,
			&prevHash, &rowHash); err != nil {
			return result, derrors.Wrap(derrors.KindInternal, "audit.verify.scan",
				"verify chain scan", err)
		}
		result.Total++
		want := computeRowHash(prevHash, rowHashPayload{
			UserID:       uid,
			Module:       module,
			RecordType:   recordType,
			RecordID:     recordID,
			FieldChanged: derefStr(fieldChanged),
			BeforeValue:  derefStr(beforeValue),
			AfterValue:   derefStr(afterValue),
			Reason:       derefStr(reason),
			Timestamp:    ts,
		})
		if want == rowHash {
			result.Verified++
		} else {
			result.Broken++
			if result.FirstBrokenID == nil {
				broken := id
				result.FirstBrokenID = &broken
			}
		}
	}
	return result, nil
}

// rowHashPayload mirrors the jsonb_build_object the BEFORE INSERT
// trigger uses in migration 0070. Keep the keys / casing / order in
// sync with `identity.audit_chain_bi()` — divergence here will mark
// every chain row as broken.
type rowHashPayload struct {
	UserID       *uuid.UUID `json:"user_id"`
	Module       string     `json:"module"`
	RecordType   string     `json:"record_type"`
	RecordID     string     `json:"record_id"`
	FieldChanged string     `json:"field_changed"`
	BeforeValue  string     `json:"before_value"`
	AfterValue   string     `json:"after_value"`
	Reason       string     `json:"reason"`
	Timestamp    time.Time  `json:"timestamp"`
}

// computeRowHash mirrors identity.compute_audit_hash(...) — sha256-hex of
// `prev_hash || '|' || payload::text`. We rely on the Go encoder
// producing the same canonical JSON the postgres `jsonb_build_object`
// would. NOTE: postgres jsonb has stable key ordering AND a specific
// numeric/string normalization; this Go-side recompute is BEST-EFFORT
// for downstream verification — the canonical truth lives in postgres.
// For server-side proofs we'd call identity.compute_audit_hash directly;
// the Go helper exists so an external consumer (audit-only sidecar) can
// run verification without the DB function.
func computeRowHash(prevHash string, p rowHashPayload) string {
	// jsonb_build_object follows the order of its arguments; the trigger
	// supplies keys in this order:
	//   user_id, module, record_type, record_id, field_changed,
	//   before_value, after_value, reason, timestamp
	// We encode an ordered slice of [key, value] pairs to match postgres'
	// jsonb_build_object output. NOT directly compatible with
	// json.Marshal(struct) — struct encoders write keys in declaration
	// order anyway, but the bigger compat issue is null vs ""
	// rendering. We replicate postgres' null-on-empty for the optional
	// columns by emitting a literal null when the source string is empty.
	type kv struct {
		k string
		v any
	}
	pairs := []kv{
		{"user_id", nullableUUID(p.UserID)},
		{"module", p.Module},
		{"record_type", p.RecordType},
		{"record_id", p.RecordID},
		{"field_changed", nullableString(p.FieldChanged)},
		{"before_value", nullableString(p.BeforeValue)},
		{"after_value", nullableString(p.AfterValue)},
		{"reason", nullableString(p.Reason)},
		{"timestamp", p.Timestamp},
	}
	m := make(map[string]any, len(pairs))
	for _, p := range pairs {
		m[p.k] = p.v
	}
	// We can't use json.Marshal directly because Go's map encoder sorts
	// keys alphabetically — but postgres' jsonb output ALSO sorts keys
	// alphabetically by default (jsonb is sorted, json is not). So
	// alphabetical-sort + struct-equivalent value rendering is the
	// faithful match.
	payload, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(prevHash + "|" + string(payload)))
	return hex.EncodeToString(sum[:])
}

func nullableUUID(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return *u
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
