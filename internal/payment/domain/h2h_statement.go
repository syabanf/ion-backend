package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// H2HStatementStatus tracks the H2H bank statement ingest pipeline.
//
// State machine:
//
//	parsing → parsed → matching → matched               (clean run)
//	                            ↘ partial               (some lines unmatched)
//	parsing → failed                                     (parse error)
type H2HStatementStatus string

const (
	H2HStatementStatusParsing  H2HStatementStatus = "parsing"
	H2HStatementStatusParsed   H2HStatementStatus = "parsed"
	H2HStatementStatusMatching H2HStatementStatus = "matching"
	H2HStatementStatusMatched  H2HStatementStatus = "matched"
	H2HStatementStatusPartial  H2HStatementStatus = "partial"
	H2HStatementStatusFailed   H2HStatementStatus = "failed"
)

// H2HBankStatement is an uploaded bank statement file. The
// (gateway_id, raw_hash) UNIQUE constraint makes re-uploads idempotent
// — the finance team can drop the same CSV twice without duplicating
// matched lines.
type H2HBankStatement struct {
	ID              uuid.UUID
	GatewayID       uuid.UUID
	StatementDate   *time.Time
	RawFilename     string
	RawHash         string
	LineCount       int
	MatchedCount    int
	UnmatchedCount  int
	Status          H2HStatementStatus
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

// NewH2HBankStatement constructs a fresh statement in the parsing
// state. RawHash is computed by the caller (typically sha256 of the
// file bytes) and used for the idempotency dedup.
func NewH2HBankStatement(gatewayID uuid.UUID, filename string, rawBytes []byte) (*H2HBankStatement, error) {
	if gatewayID == uuid.Nil {
		return nil, errors.Validation("h2h.gateway_required", "gateway_id is required")
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, errors.Validation("h2h.filename_required", "raw_filename is required")
	}
	if len(rawBytes) == 0 {
		return nil, errors.Validation("h2h.content_empty", "statement content is empty")
	}
	sum := sha256.Sum256(rawBytes)
	return &H2HBankStatement{
		ID:          uuid.New(),
		GatewayID:   gatewayID,
		RawFilename: filename,
		RawHash:     hex.EncodeToString(sum[:]),
		Status:      H2HStatementStatusParsing,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// MarkParsed records the line count after the CSV parser finishes.
func (s *H2HBankStatement) MarkParsed(lineCount int) error {
	if s.Status != H2HStatementStatusParsing {
		return errors.Conflict(
			"h2h.cannot_mark_parsed",
			"only parsing statements can be marked parsed",
		)
	}
	if lineCount < 0 {
		return errors.Validation("h2h.line_count_invalid", "line_count must be >= 0")
	}
	s.LineCount = lineCount
	s.Status = H2HStatementStatusParsed
	return nil
}

// StartMatching flips parsed → matching when the matcher run begins.
// Idempotent re-runs allowed from `matched` / `partial` for
// re-reconciliation after new intents land.
func (s *H2HBankStatement) StartMatching() error {
	switch s.Status {
	case H2HStatementStatusParsed, H2HStatementStatusMatched, H2HStatementStatusPartial:
		s.Status = H2HStatementStatusMatching
		return nil
	default:
		return errors.Conflict(
			"h2h.cannot_start_matching",
			"only parsed (or already-matched) statements can be matched",
		)
	}
}

// CompleteMatching flips matching → matched / partial depending on
// whether every line found an intent.
func (s *H2HBankStatement) CompleteMatching(matched, unmatched int) error {
	if s.Status != H2HStatementStatusMatching {
		return errors.Conflict(
			"h2h.cannot_complete_matching",
			"only matching statements can be marked complete",
		)
	}
	s.MatchedCount = matched
	s.UnmatchedCount = unmatched
	now := time.Now().UTC()
	s.CompletedAt = &now
	if unmatched == 0 {
		s.Status = H2HStatementStatusMatched
	} else {
		s.Status = H2HStatementStatusPartial
	}
	return nil
}

// MarkFailed records a parse / matcher crash. Terminal — finance
// re-uploads (which the (gateway, hash) UNIQUE deduplicates against
// after the failure row is cleaned up by ops).
func (s *H2HBankStatement) MarkFailed() {
	s.Status = H2HStatementStatusFailed
	now := time.Now().UTC()
	s.CompletedAt = &now
}

// H2HBankLine is one parsed CSV row from a bank statement.
type H2HBankLine struct {
	ID                uuid.UUID
	StatementID       uuid.UUID
	RawLine           []byte // serialised JSON of the original CSV row
	Amount            *float64
	ValueDate         *time.Time
	ReferenceText     string
	PaymentIntentID   *uuid.UUID
	MatchConfidence   *float64
	MatchMethod       string
	CreatedAt         time.Time
}

// NewH2HBankLine constructs a freshly-parsed line without a match.
func NewH2HBankLine(statementID uuid.UUID, raw []byte) *H2HBankLine {
	return &H2HBankLine{
		ID:          uuid.New(),
		StatementID: statementID,
		RawLine:     raw,
		CreatedAt:   time.Now().UTC(),
	}
}

// AttachMatch records a successful match against a payment intent
// with the confidence + method used.
func (l *H2HBankLine) AttachMatch(intentID uuid.UUID, confidence float64, method string) {
	c := math.Round(confidence*100) / 100 // truncate to 2dp for NUMERIC(3,2)
	l.PaymentIntentID = &intentID
	l.MatchConfidence = &c
	l.MatchMethod = strings.TrimSpace(method)
}

// MatchByReference compares a line's reference + amount + value date
// against a candidate payment intent. Returns a confidence score in
// [0,1] and the method label used to derive it.
//
// Rules:
//   - Exact reference text match → 0.95 + method 'reference_exact'
//   - Substring reference match (line ref contains the intent's
//     short-id, or vice versa) AND amount equal → 0.85 +
//     'reference_substring_amount'
//   - Amount equal AND value date within ±2 days of intent.paid_at
//     → 0.50 + 'amount_date_window' (catches reference-less wire
//     transfers — finance reviews manually)
//   - otherwise → 0 + 'no_match'
func MatchByReference(
	lineRef string, lineAmount float64, lineValueDate time.Time,
	intentRefShort string, intentAmount float64, intentPaidAt *time.Time,
) (float64, string) {
	lineRef = strings.TrimSpace(strings.ToLower(lineRef))
	intentRefShort = strings.TrimSpace(strings.ToLower(intentRefShort))

	if lineRef != "" && lineRef == intentRefShort {
		return 0.95, "reference_exact"
	}
	amountEqual := math.Abs(lineAmount-intentAmount) < 0.01
	if amountEqual && lineRef != "" && intentRefShort != "" &&
		(strings.Contains(lineRef, intentRefShort) || strings.Contains(intentRefShort, lineRef)) {
		return 0.85, "reference_substring_amount"
	}
	if amountEqual && intentPaidAt != nil {
		delta := lineValueDate.Sub(*intentPaidAt)
		if delta < 0 {
			delta = -delta
		}
		if delta <= 48*time.Hour {
			return 0.50, "amount_date_window"
		}
	}
	return 0, "no_match"
}
