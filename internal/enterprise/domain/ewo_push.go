package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// Wave 103 — EWO push notification log
//
// EWOPushEvent is an append-only record of a push notification fired
// from the enterprise/cron technician dispatcher. The cron consults this
// table before sending a duplicate push (one-shot subjects only —
// reminders are allowed to repeat).
// =====================================================================

// EWOPushSubject classifies the push. Mirrors the migration check
// constraint exactly.
type EWOPushSubject string

const (
	EWOPushSubjectAssigned   EWOPushSubject = "assigned"
	EWOPushSubjectReassigned EWOPushSubject = "reassigned"
	EWOPushSubjectReschedule EWOPushSubject = "reschedule"
	EWOPushSubjectReminder   EWOPushSubject = "reminder"
	EWOPushSubjectCancelled  EWOPushSubject = "cancelled"
)

// EWOPushEvent persists in enterprise.ewo_push_log.
type EWOPushEvent struct {
	ID             uuid.UUID
	EWOID          uuid.UUID
	Subject        EWOPushSubject
	TargetUserID   uuid.UUID
	Payload        map[string]any
	SentAt         time.Time
	DispatchStatus string // "sent", "failed", "skipped"
	ErrorMsg       string
}

// BuildPayload renders the deep-link payload for a push.
//
// Shape:
//
//	{
//	  ewo_id              : "...",
//	  side                : "y",
//	  scheduled_start     : "2026-…",   // optional
//	  scheduled_end       : "2026-…",   // optional
//	  intercompany_po_id  : "...",      // optional
//	  customer_name       : "",          // populated by caller if available
//	  site_address        : "",          // populated by caller if available
//	  subject             : "assigned",
//	  deep_link           : "ion-tech://ewo/<ewo_id>"
//	}
//
// Customer name + site address aren't on the EWO row; the cron resolves
// them from related tables (opportunity → account name, project_site →
// address) before calling BuildPayload. We accept the empty-string
// defaults so the function is callable with just an EWO + subject.
func BuildPayload(ewo *EWO, subject EWOPushSubject) map[string]any {
	if ewo == nil {
		return map[string]any{}
	}
	p := map[string]any{
		"ewo_id":    ewo.ID.String(),
		"side":      string(ewo.Side),
		"subject":   string(subject),
		"deep_link": fmt.Sprintf("ion-tech://ewo/%s", ewo.ID.String()),
	}
	if ewo.ScheduledStartDate != nil {
		p["scheduled_start"] = ewo.ScheduledStartDate.UTC().Format(time.RFC3339)
	}
	if ewo.ScheduledEndDate != nil {
		p["scheduled_end"] = ewo.ScheduledEndDate.UTC().Format(time.RFC3339)
	}
	if ewo.IntercompanyPOID != nil {
		p["intercompany_po_id"] = ewo.IntercompanyPOID.String()
	}
	if ewo.ExecutingSubsidiaryID != nil {
		p["executing_subsidiary_id"] = ewo.ExecutingSubsidiaryID.String()
	}
	// Customer + site fields default empty — the cron populates them
	// when it has the joins available; the mobile app tolerates blanks.
	p["customer_name"] = ""
	p["site_address"] = ""
	return p
}
