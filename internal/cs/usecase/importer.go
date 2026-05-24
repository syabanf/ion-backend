// Wave 128D — TicketImporterService backfills cs.tickets from
// field.tickets, closing the Ticket-SM divergence flagged in Wave 123's
// MIGRATION.md and Wave 127's compliance report §3g.
//
// Why a dedicated port + adapter pair:
//
//   - The category → ticket_type mapping is a domain-level rule
//     (workflow taxonomy vs symptom-oriented enum). Keeping the
//     translation logic in the usecase keeps the rule discoverable
//     next to the rest of the bounded-context code.
//   - The legacy reader + canonical writer are split into two small
//     port interfaces so the usecase is testable with in-memory stubs
//     (importer_test.go) while the production path (cmd/cs-svc/main.go)
//     wires a pgxpool-backed adapter. Same hexagonal pattern the rest
//     of internal/cs/ uses.
//
// Idempotence model:
//
//   - migration 0087 adds cs.tickets.legacy_id (nullable) + a partial
//     unique index. The Insert path on the adapter uses
//     ON CONFLICT (legacy_id) WHERE legacy_id IS NOT NULL DO NOTHING,
//     so re-runs are no-ops on already-imported rows. The usecase also
//     pre-filters via the LegacyTicketReader.ListUnmigrated query so
//     we don't even attempt the INSERT when we don't need to — keeps
//     the AlreadyMigrated counter honest.
//
// Status-mapping decisions (legacy 5 → canonical 7):
//
//	open               → open
//	in_progress        → in_progress
//	pending_customer   → pending_customer
//	resolved           → resolved
//	closed             → closed
//
// The two canonical-only states (`assigned`, `pending_internal`) have
// no inbound mapping; agents can hand-tune post-import if needed.
//
// Category-mapping decisions (symptom-oriented → workflow-oriented):
//
// The prompt enumerated a richer set of legacy categories than the
// actual field.tickets schema (migration 0036) carries. The DB enum
// today is {no_internet, slow_speed, frequent_drops, equipment_damage,
// billing_dispute, other}. We map both the actual DB values AND the
// richer prompt set so the mapping stays correct if the legacy enum
// is ever broadened. Unknown values default to `technical` with a
// logged warning.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// Ports — the importer's driven contracts.
// =====================================================================

// LegacyTicketRow is the wire-level shape of a field.tickets row as
// seen by the importer. The struct lives in usecase (not domain)
// because it's an inter-context bridge type — field.* doesn't belong
// in cs's domain.
type LegacyTicketRow struct {
	ID           uuid.UUID
	TicketNumber string
	CustomerID   uuid.UUID
	Category     string
	Priority     string
	Status       string
	Summary      string
	Description  string
	OpenedBy     *uuid.UUID
	AssignedTo   *uuid.UUID
	WOID         *uuid.UUID
	ResolvedAt   *time.Time
	ClosedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// LegacyTicketReader scopes the legacy-side query surface the importer
// needs. The adapter implements both methods against field.tickets.
type LegacyTicketReader interface {
	// CountAll returns the total field.tickets row count — used for the
	// `TotalLegacy` summary header.
	CountAll(ctx context.Context) (int, error)
	// ListUnmigrated returns legacy rows that are not yet present in
	// cs.tickets.legacy_id, ordered by created_at ASC, bounded by
	// `limit`. Idempotent re-runs hit zero rows once steady-state.
	ListUnmigrated(ctx context.Context, limit int) ([]LegacyTicketRow, error)
}

// CanonicalTicketWriter scopes the cs.tickets-side write surface. The
// adapter implements Insert with ON CONFLICT (legacy_id) DO NOTHING so
// concurrent runners can't produce duplicates. The boolean return
// distinguishes a fresh insert from a no-op conflict.
type CanonicalTicketWriter interface {
	// Insert attempts to write the canonical row. Returns inserted=true
	// when a new row landed; inserted=false on the ON CONFLICT path
	// (already migrated by a concurrent runner).
	Insert(ctx context.Context, t *domain.Ticket) (inserted bool, err error)
}

// =====================================================================
// Service
// =====================================================================

// ImportSummary captures the per-run counters surfaced via the HTTP
// endpoint and used by the cron log line.
type ImportSummary struct {
	TotalLegacy     int      `json:"total_legacy"`
	AlreadyMigrated int      `json:"already_migrated"`
	Imported        int      `json:"imported"`
	Skipped         int      `json:"skipped"`
	Errors          int      `json:"errors"`
	ErrorSamples    []string `json:"error_samples,omitempty"`
}

// TicketImporterService runs a single-pass importer from field.tickets
// to cs.tickets. Safe to call concurrently — every write uses
// ON CONFLICT (legacy_id) DO NOTHING semantics so a race produces no
// duplicate rows; the loser of the race just bumps AlreadyMigrated.
type TicketImporterService struct {
	reader LegacyTicketReader
	writer CanonicalTicketWriter
	log    *slog.Logger

	// errorSampleCap bounds the slice in ImportSummary so a pathological
	// run doesn't produce a megabyte of JSON.
	errorSampleCap int
	// rowBatchLimit bounds how many legacy rows we scan per RunOnce.
	// Daily cron + the catch-up-on-boot pattern means this can be small;
	// we still want a knob in case a one-time backfill needs more.
	rowBatchLimit int
}

// NewTicketImporterService wires the importer with reader + writer
// adapters. nil logger falls back to slog.Default().
func NewTicketImporterService(reader LegacyTicketReader, writer CanonicalTicketWriter, log *slog.Logger) *TicketImporterService {
	if log == nil {
		log = slog.Default()
	}
	return &TicketImporterService{
		reader:         reader,
		writer:         writer,
		log:            log.With("component", "cs.importer"),
		errorSampleCap: 10,
		rowBatchLimit:  500,
	}
}

// WithRowBatchLimit overrides how many legacy rows are scanned per
// RunOnce. Useful when an ad-hoc HTTP trigger drains a large backlog.
func (s *TicketImporterService) WithRowBatchLimit(n int) *TicketImporterService {
	if n > 0 {
		s.rowBatchLimit = n
	}
	return s
}

// RunOnce performs a single importer pass. Returns the summary even on
// partial failure — the caller (HTTP handler or cron tick) decides
// what to do with the Errors / ErrorSamples counts. Top-level errors
// (DB unreachable, schema missing) bubble up as the error return.
func (s *TicketImporterService) RunOnce(ctx context.Context) (ImportSummary, error) {
	var summary ImportSummary
	if s == nil || s.reader == nil || s.writer == nil {
		return summary, errors.New("cs.importer: reader/writer not wired")
	}

	total, err := s.reader.CountAll(ctx)
	if err != nil {
		return summary, fmt.Errorf("cs.importer: count legacy: %w", err)
	}
	summary.TotalLegacy = total

	pending, err := s.reader.ListUnmigrated(ctx, s.rowBatchLimit)
	if err != nil {
		return summary, fmt.Errorf("cs.importer: list unmigrated: %w", err)
	}

	// AlreadyMigrated = totalLegacy - eligible(pending). The arithmetic
	// is a single-snapshot estimate; concurrent writes inside the same
	// RunOnce window may make the counters slightly under-count but
	// never over-count (worst case: a freshly-inserted cs-native row
	// inflates TotalLegacy after the scan; the counters stay
	// non-negative because pending <= TotalLegacy at scan time).
	if len(pending) > summary.TotalLegacy {
		summary.AlreadyMigrated = 0
	} else {
		summary.AlreadyMigrated = summary.TotalLegacy - len(pending)
	}

	for i := range pending {
		r := pending[i]
		t, mapErr := s.mapRowToTicket(r)
		if mapErr != nil {
			summary.Skipped++
			s.log.Warn("cs.importer skip legacy row — invalid mapping",
				"legacy_id", r.ID, "ticket_number", r.TicketNumber, "err", mapErr)
			continue
		}
		inserted, err := s.writer.Insert(ctx, t)
		if err != nil {
			summary.Errors++
			if len(summary.ErrorSamples) < s.errorSampleCap {
				summary.ErrorSamples = append(summary.ErrorSamples,
					fmt.Sprintf("%s: %v", r.ID, err))
			}
			s.log.Warn("cs.importer insert failed",
				"legacy_id", r.ID, "ticket_number", r.TicketNumber, "err", err)
			continue
		}
		if !inserted {
			// Concurrent insert from another runner — count as already
			// migrated, not as a fresh import.
			summary.AlreadyMigrated++
			continue
		}
		summary.Imported++
	}

	if summary.Imported > 0 || summary.Errors > 0 {
		s.log.Info("cs.importer run summary",
			"total_legacy", summary.TotalLegacy,
			"already_migrated", summary.AlreadyMigrated,
			"imported", summary.Imported,
			"skipped", summary.Skipped,
			"errors", summary.Errors,
		)
	}
	return summary, nil
}

// mapRowToTicket applies every legacy → canonical translation in one
// place. Returns a fully-populated *domain.Ticket ready for INSERT.
//
// Note: we bypass domain.NewTicket because:
//   - It generates a fresh ID (we need to keep the row decoupled from
//     the legacy id; legacy_id is the link, not the primary key).
//   - It defaults Status to `open`; we want to preserve the legacy
//     status (`closed` legacy ticket should land as `closed` in cs).
//
// Validation that domain.NewTicket would have done — empty title,
// nil customer_id — is enforced inline below.
func (s *TicketImporterService) mapRowToTicket(r LegacyTicketRow) (*domain.Ticket, error) {
	if r.CustomerID == uuid.Nil {
		return nil, fmt.Errorf("missing customer_id")
	}

	ticketType := mapCategoryToTicketType(r.Category, s.log)
	priority := mapLegacyPriority(r.Priority)
	status := mapLegacyStatus(r.Status)

	title := r.Summary
	if title == "" {
		title = "(legacy ticket " + r.TicketNumber + ")"
	}

	// opened_by NOT NULL in cs.tickets — fall back to the customer id
	// when the legacy row's opened_by is null. Older portal-side rows
	// were created without an agent in the loop; we still need a value
	// for the FK column.
	openedBy := r.CustomerID
	openedVia := domain.OpenedViaPortal
	if r.OpenedBy != nil {
		openedBy = *r.OpenedBy
		openedVia = domain.OpenedViaAgentInternal
	}

	t := &domain.Ticket{
		ID:               uuid.New(),
		TicketNo:         canonicalTicketNo(r.TicketNumber, r.CreatedAt),
		CustomerID:       r.CustomerID,
		OpenedBy:         openedBy,
		OpenedVia:        openedVia,
		TicketType:       ticketType,
		Title:            title,
		Description:      r.Description,
		Status:           status,
		Priority:         priority,
		AssignedUserID:   r.AssignedTo,
		RelatedWOID:      r.WOID,
		ResolvedAt:       r.ResolvedAt,
		ClosedAt:         r.ClosedAt,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		SourceMetadata: map[string]any{
			"legacy_ticket_number": r.TicketNumber,
			"legacy_id":            r.ID.String(),
			"import_wave":          "128D",
		},
	}

	// The adapter writes legacy_id via the per-call argument so the
	// domain.Ticket struct stays unaware of it (legacy_id is a
	// schema-only concept, not a domain attribute). We attach it via
	// SourceMetadata for trace-back so a SELECT on cs.tickets alone
	// can locate the origin row.
	return t, nil
}

// LegacyID extracts the legacy_id stamped in SourceMetadata. Used by
// the postgres adapter at Insert time. Returns uuid.Nil if absent —
// the adapter then writes NULL.
func LegacyID(t *domain.Ticket) uuid.UUID {
	if t == nil || t.SourceMetadata == nil {
		return uuid.Nil
	}
	raw, ok := t.SourceMetadata["legacy_id"]
	if !ok {
		return uuid.Nil
	}
	s, ok := raw.(string)
	if !ok {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// =====================================================================
// Mapping helpers — exported so the e2e + unit tests can assert on the
// same translation that production uses.
// =====================================================================

// mapCategoryToTicketType applies the symptom-oriented → workflow-oriented
// mapping. Unknown values get logged + defaulted to technical (the
// most common bucket per Wave 127 §3g audit).
func mapCategoryToTicketType(category string, log *slog.Logger) domain.TicketType {
	switch category {
	// === Actual legacy enum (migration 0036) ===
	case "no_internet", "slow_speed", "frequent_drops", "equipment_damage":
		return domain.TicketTypeTechnical
	case "billing_dispute":
		return domain.TicketTypeBilling
	case "other":
		// "other" is ambiguous — bias toward technical since the
		// majority of opened tickets on the legacy path were
		// connectivity-related (per Wave 127 §3g audit).
		return domain.TicketTypeTechnical

	// === Prompt-specified richer set (preserved for forward-compat
	//     if the legacy enum is ever broadened) ===
	case "intermittent", "signal_quality", "hardware_failure":
		return domain.TicketTypeTechnical
	case "invoice_dispute", "payment_issue", "refund":
		return domain.TicketTypeBilling
	case "service_quality", "complaint", "escalation":
		return domain.TicketTypeComplaint
	case "cancellation", "plan_change", "address_change":
		return domain.TicketTypeServiceRequest
	case "status_inquiry", "info_request":
		return domain.TicketTypeInformation
	}
	if log != nil {
		log.Warn("cs.importer unmapped category — defaulting to technical",
			"category", category)
	}
	return domain.TicketTypeTechnical
}

// mapLegacyStatus collapses the 5-state legacy SM into the 7-state
// canonical one.
func mapLegacyStatus(s string) domain.TicketStatus {
	switch s {
	case "open":
		return domain.TicketStatusOpen
	case "in_progress":
		return domain.TicketStatusInProgress
	case "pending_customer":
		return domain.TicketStatusPendingCustomer
	case "resolved":
		return domain.TicketStatusResolved
	case "closed":
		return domain.TicketStatusClosed
	}
	// Unknown status (shouldn't happen given the DB CHECK constraint)
	// — fall through to open to keep the row visible.
	return domain.TicketStatusOpen
}

// mapLegacyPriority normalises legacy priority labels onto the canonical
// 4-tier scale. Legacy enum carries only {high, medium, low}; we
// promote medium → normal (the canonical default).
func mapLegacyPriority(p string) domain.Priority {
	switch p {
	case "high":
		return domain.PriorityHigh
	case "medium":
		return domain.PriorityNormal
	case "low":
		return domain.PriorityLow
	case "urgent":
		return domain.PriorityUrgent
	}
	return domain.PriorityNormal
}

// canonicalTicketNo derives a cs.tickets.ticket_no from a legacy
// ticket_number. We deliberately do NOT call repo.NextTicketNo — the
// importer must produce stable IDs so a re-run is byte-identical
// (well, byte-identical except for the ID). Prefix the legacy
// ticket_number with `IMP-` to make the origin obvious in agent UIs.
func canonicalTicketNo(legacyNumber string, createdAt time.Time) string {
	if legacyNumber == "" {
		return fmt.Sprintf("IMP-%04d-%s", createdAt.Year(), uuid.NewString()[:8])
	}
	return "IMP-" + legacyNumber
}
