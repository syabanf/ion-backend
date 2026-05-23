package cron

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/notifyx"
)

// =====================================================================
// Wave 103 — Technician push dispatcher
//
// Two workers, both 5-minute ticker:
//
//   1. DispatchAssignmentNotifications
//      scans for EWO-Y rows with an assigned_technician_user_id set,
//      status='pending', and no existing push_log row of subject
//      'assigned' for the (ewo_id, target_user_id) pair. Fires one
//      push + writes the log row.
//
//   2. DispatchRescheduleNotifications
//      scans ewo_schedule_history rows newer than the last tick, fans
//      'reassigned' to the prev technician (cancellation) + new
//      technician (assignment) when the technician changed, and
//      'reschedule' to the current technician when the time window
//      shifted.
//
// Idempotency is keyed off ewo_push_log row existence — replay-safe
// even if the cron interrupts mid-batch.
// =====================================================================

const (
	technicianPushTickInterval = 5 * time.Minute
)

// TechnicianPushDispatcher fans push notifications to assigned
// technicians. The Runner builder method WithTechnicianPushDispatcher
// wires it into Start().
type TechnicianPushDispatcher struct {
	pool     *pgxpool.Pool
	ewos     port.EWOMobileRepository
	pushLog  port.EWOPushLogRepository
	notifier *notifyx.Dispatcher
	log      *slog.Logger
	// lastRescheduleTick is the high-watermark for the reschedule
	// scan; initialised to NOW() at start, advanced after each
	// successful tick. Held in-memory only — on restart we skip
	// catch-up of older rows (the prev/new pair is already reflected
	// on the EWO row itself, so the worst that happens is a missed
	// push, not a stuck state).
	lastRescheduleTick time.Time
}

// NewTechnicianPushDispatcher constructs the dispatcher. The notifier
// arg may be nil — when missing, the dispatcher still writes log rows
// (so the mobile "what got pushed to me?" surface stays populated) but
// the actual push fan-out is skipped.
func NewTechnicianPushDispatcher(
	pool *pgxpool.Pool,
	ewos port.EWOMobileRepository,
	pushLog port.EWOPushLogRepository,
	notifier *notifyx.Dispatcher,
	log *slog.Logger,
) *TechnicianPushDispatcher {
	return &TechnicianPushDispatcher{
		pool:               pool,
		ewos:               ewos,
		pushLog:            pushLog,
		notifier:           notifier,
		log:                log.With("worker", "technician_push"),
		lastRescheduleTick: time.Now().UTC(),
	}
}

// Start spawns the worker goroutine. Caller cancels ctx to stop.
func (d *TechnicianPushDispatcher) Start(ctx context.Context) {
	go d.run(ctx)
}

func (d *TechnicianPushDispatcher) run(ctx context.Context) {
	// One immediate tick on startup so a fresh deploy doesn't wait the
	// full 5 minutes to catch up on backlog.
	d.DispatchAssignmentNotifications(ctx)
	d.DispatchRescheduleNotifications(ctx)
	t := time.NewTicker(technicianPushTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.DispatchAssignmentNotifications(ctx)
			d.DispatchRescheduleNotifications(ctx)
		}
	}
}

// DispatchAssignmentNotifications scans for assigned EWO-Y rows that
// haven't yet been pushed. The query joins against ewo_push_log to skip
// already-notified pairs — that's the idempotency mechanism.
func (d *TechnicianPushDispatcher) DispatchAssignmentNotifications(ctx context.Context) {
	if d.pushLog == nil {
		return
	}
	rows, err := d.pool.Query(ctx, `
		SELECT e.id, e.assigned_technician_user_id,
		       e.scheduled_start_date, e.scheduled_end_date,
		       e.intercompany_po_id, e.executing_subsidiary_id,
		       e.side
		FROM enterprise.ewos e
		WHERE e.side = 'y'
		  AND e.assigned_technician_user_id IS NOT NULL
		  AND e.status = 'pending'
		  AND NOT EXISTS (
		      SELECT 1 FROM enterprise.ewo_push_log l
		      WHERE l.ewo_id = e.id
		        AND l.target_user_id = e.assigned_technician_user_id
		        AND l.subject = 'assigned'
		  )
		ORDER BY e.created_at ASC
		LIMIT 200
	`)
	if err != nil {
		d.log.Warn("assignment_push: query failed", "err", err)
		return
	}
	defer rows.Close()
	type assignment struct {
		ewoID        uuid.UUID
		technicianID uuid.UUID
		schedStart   *time.Time
		schedEnd     *time.Time
		icPOID       *uuid.UUID
		execSubsID   *uuid.UUID
		side         string
	}
	var batch []assignment
	for rows.Next() {
		var a assignment
		var (
			schedStart sql.NullTime
			schedEnd   sql.NullTime
			icPOID     *uuid.UUID
			execSubs   *uuid.UUID
		)
		if err := rows.Scan(&a.ewoID, &a.technicianID,
			&schedStart, &schedEnd, &icPOID, &execSubs, &a.side); err != nil {
			d.log.Warn("assignment_push: scan failed", "err", err)
			continue
		}
		if schedStart.Valid {
			t := schedStart.Time
			a.schedStart = &t
		}
		if schedEnd.Valid {
			t := schedEnd.Time
			a.schedEnd = &t
		}
		a.icPOID = icPOID
		a.execSubsID = execSubs
		batch = append(batch, a)
	}
	if len(batch) == 0 {
		return
	}
	d.log.Info("assignment_push: dispatching", "count", len(batch))
	for _, a := range batch {
		ewo := &domain.EWO{
			ID:                    a.ewoID,
			Side:                  domain.EWOSide(a.side),
			ScheduledStartDate:    a.schedStart,
			ScheduledEndDate:      a.schedEnd,
			IntercompanyPOID:      a.icPOID,
			ExecutingSubsidiaryID: a.execSubsID,
		}
		d.fire(ctx, ewo, domain.EWOPushSubjectAssigned, a.technicianID)
	}
}

// DispatchRescheduleNotifications scans schedule-history rows newer
// than the last successful tick. For each row:
//   - If the technician changed (prev != current), fire 'reassigned'
//     to BOTH prev (cancellation) and current (assignment).
//   - If the schedule window changed AND the technician is still set,
//     fire 'reschedule' to the current technician.
//
// We don't dedup via push_log presence here because 'reschedule' is
// allowed to repeat (a technician may have several reschedules across
// the lifetime of an EWO). 'reassigned' DOES dedup via HasSubject
// since each (prev_user, new_user) handoff should only push once.
func (d *TechnicianPushDispatcher) DispatchRescheduleNotifications(ctx context.Context) {
	if d.pushLog == nil {
		return
	}
	since := d.lastRescheduleTick
	// Pull history rows newer than the watermark + the current
	// scheduled fields on the corresponding EWO. We need both because
	// the history row holds the prev values; the current EWO row holds
	// the new values.
	rows, err := d.pool.Query(ctx, `
		SELECT h.id, h.ewo_id, h.prev_start, h.prev_end,
		       h.prev_team_lead, h.prev_technician, h.changed_at,
		       e.scheduled_start_date, e.scheduled_end_date,
		       e.assigned_technician_user_id, e.side, e.intercompany_po_id,
		       e.executing_subsidiary_id
		FROM enterprise.ewo_schedule_history h
		JOIN enterprise.ewos e ON e.id = h.ewo_id
		WHERE h.changed_at > $1
		  AND e.side = 'y'
		ORDER BY h.changed_at ASC
		LIMIT 200
	`, since)
	if err != nil {
		if err == pgx.ErrNoRows {
			return
		}
		d.log.Warn("reschedule_push: query failed", "err", err)
		return
	}
	defer rows.Close()
	type historyRow struct {
		historyID         uuid.UUID
		ewoID             uuid.UUID
		prevStart         time.Time
		prevEnd           time.Time
		prevTechnician    *uuid.UUID
		changedAt         time.Time
		curStart          *time.Time
		curEnd            *time.Time
		curTechnician     *uuid.UUID
		side              string
		icPOID            *uuid.UUID
		execSubsidiaryID  *uuid.UUID
	}
	var batch []historyRow
	for rows.Next() {
		var (
			hr             historyRow
			prevTL         *uuid.UUID
			prevTec        *uuid.UUID
			curStart       sql.NullTime
			curEnd         sql.NullTime
		)
		if err := rows.Scan(
			&hr.historyID, &hr.ewoID, &hr.prevStart, &hr.prevEnd,
			&prevTL, &prevTec, &hr.changedAt,
			&curStart, &curEnd, &hr.curTechnician, &hr.side,
			&hr.icPOID, &hr.execSubsidiaryID,
		); err != nil {
			d.log.Warn("reschedule_push: scan failed", "err", err)
			continue
		}
		hr.prevTechnician = prevTec
		if curStart.Valid {
			t := curStart.Time
			hr.curStart = &t
		}
		if curEnd.Valid {
			t := curEnd.Time
			hr.curEnd = &t
		}
		batch = append(batch, hr)
	}
	if len(batch) == 0 {
		// Advance watermark even with no rows so we don't re-scan the
		// same window forever.
		d.lastRescheduleTick = time.Now().UTC()
		return
	}
	d.log.Info("reschedule_push: dispatching", "count", len(batch))
	for _, hr := range batch {
		ewo := &domain.EWO{
			ID:                    hr.ewoID,
			Side:                  domain.EWOSide(hr.side),
			ScheduledStartDate:    hr.curStart,
			ScheduledEndDate:      hr.curEnd,
			IntercompanyPOID:      hr.icPOID,
			ExecutingSubsidiaryID: hr.execSubsidiaryID,
		}
		// Technician change?
		prevTech := uuid.Nil
		if hr.prevTechnician != nil {
			prevTech = *hr.prevTechnician
		}
		curTech := uuid.Nil
		if hr.curTechnician != nil {
			curTech = *hr.curTechnician
		}
		if prevTech != curTech {
			if prevTech != uuid.Nil {
				// Cancellation notice to the prev tech.
				d.fire(ctx, ewo, domain.EWOPushSubjectReassigned, prevTech)
			}
			if curTech != uuid.Nil {
				d.fire(ctx, ewo, domain.EWOPushSubjectReassigned, curTech)
			}
			// Watermark advances regardless.
			if hr.changedAt.After(d.lastRescheduleTick) {
				d.lastRescheduleTick = hr.changedAt
			}
			continue
		}
		// Same tech but time window shifted? Compare prev vs current.
		windowShifted := false
		if hr.curStart != nil && !hr.curStart.Equal(hr.prevStart) {
			windowShifted = true
		}
		if hr.curEnd != nil && !hr.curEnd.Equal(hr.prevEnd) {
			windowShifted = true
		}
		if windowShifted && curTech != uuid.Nil {
			d.fire(ctx, ewo, domain.EWOPushSubjectReschedule, curTech)
		}
		if hr.changedAt.After(d.lastRescheduleTick) {
			d.lastRescheduleTick = hr.changedAt
		}
	}
}

// fire writes the push_log row AND fans the notifyx message. The order
// matters: log write first so a notifier failure doesn't lose the
// audit trail; log row uses dispatch_status='sent' on the optimistic
// path and we leave a later corrector cron to flip mismatches if push
// reliability becomes an issue.
func (d *TechnicianPushDispatcher) fire(
	ctx context.Context,
	ewo *domain.EWO,
	subject domain.EWOPushSubject,
	targetUserID uuid.UUID,
) {
	if targetUserID == uuid.Nil {
		return
	}
	// Dedup one-shot subjects (assigned + reassigned + cancelled).
	// Reschedule + reminder are allowed to repeat — skip the lookup
	// for those.
	if subject != domain.EWOPushSubjectReschedule && subject != domain.EWOPushSubjectReminder {
		seen, err := d.pushLog.HasSubject(ctx, ewo.ID, targetUserID, subject)
		if err != nil {
			d.log.Warn("push.dedup_lookup_failed",
				"ewo_id", ewo.ID, "subject", string(subject), "err", err)
		} else if seen {
			return
		}
	}
	payload := domain.BuildPayload(ewo, subject)
	// Enrich payload with the joined fields the cron can fetch cheaply.
	if name, address, ok := d.lookupCustomerSite(ctx, ewo.ID); ok {
		payload["customer_name"] = name
		payload["site_address"] = address
	}
	// Write the log row regardless of whether notifier is wired —
	// the mobile app's "what got pushed to me?" surface always sees
	// fresh entries.
	ev := &domain.EWOPushEvent{
		ID:             uuid.New(),
		EWOID:          ewo.ID,
		Subject:        subject,
		TargetUserID:   targetUserID,
		Payload:        payload,
		SentAt:         time.Now().UTC(),
		DispatchStatus: "sent",
	}
	if err := d.pushLog.Create(ctx, ev); err != nil {
		d.log.Warn("push.log_write_failed",
			"ewo_id", ewo.ID, "subject", string(subject), "err", err)
		// Don't push if the audit row failed — we'd lose dedup.
		return
	}
	// Fan the actual push if a notifier is wired.
	if d.notifier == nil {
		return
	}
	msg := notifyx.Message{
		Title:    pushTitle(subject),
		Body:     pushBody(subject, ewo),
		DeepLink: "ion-tech://ewo/" + ewo.ID.String(),
		Topic:    "ewo_" + string(subject),
		Data: map[string]string{
			"ewo_id":  ewo.ID.String(),
			"subject": string(subject),
		},
	}
	d.notifier.Send(ctx, notifyx.Target{UserID: targetUserID}, msg)
}

// lookupCustomerSite is best-effort enrichment. Walks the join chain
// quotation → opportunity → account_name + project_site.address_text.
// Returns ok=false when either join fails so the payload still ships
// with the empty defaults.
func (d *TechnicianPushDispatcher) lookupCustomerSite(
	ctx context.Context, ewoID uuid.UUID,
) (string, string, bool) {
	var name, address sql.NullString
	err := d.pool.QueryRow(ctx, `
		SELECT
		  COALESCE(o.account_name, ''),
		  COALESCE((SELECT ps.address_text
		            FROM enterprise.project_sites ps
		            JOIN enterprise.projects pr ON pr.id = ps.project_id
		            WHERE pr.opportunity_id = o.id
		            LIMIT 1), '')
		FROM enterprise.ewos e
		JOIN enterprise.opportunities o ON o.id = e.opportunity_id
		WHERE e.id = $1
	`, ewoID).Scan(&name, &address)
	if err != nil {
		return "", "", false
	}
	return name.String, address.String, true
}

func pushTitle(subject domain.EWOPushSubject) string {
	switch subject {
	case domain.EWOPushSubjectAssigned:
		return "New work assigned"
	case domain.EWOPushSubjectReassigned:
		return "Assignment updated"
	case domain.EWOPushSubjectReschedule:
		return "Schedule changed"
	case domain.EWOPushSubjectReminder:
		return "EWO reminder"
	case domain.EWOPushSubjectCancelled:
		return "Assignment cancelled"
	}
	return "EWO update"
}

func pushBody(subject domain.EWOPushSubject, ewo *domain.EWO) string {
	if ewo == nil {
		return ""
	}
	when := ""
	if ewo.ScheduledStartDate != nil {
		when = " — " + ewo.ScheduledStartDate.UTC().Format("Mon Jan 2 15:04")
	}
	switch subject {
	case domain.EWOPushSubjectAssigned:
		return "You have a new EWO assignment" + when
	case domain.EWOPushSubjectReassigned:
		return "Your EWO assignment has changed" + when
	case domain.EWOPushSubjectReschedule:
		return "Your EWO has been rescheduled" + when
	case domain.EWOPushSubjectReminder:
		return "Upcoming EWO" + when
	case domain.EWOPushSubjectCancelled:
		return "An EWO assigned to you was cancelled"
	}
	return "Open the app for details"
}

// =====================================================================
// Runner wiring
// =====================================================================

// WithTechnicianPushDispatcher attaches the technician push dispatcher
// to the Runner. The Runner.Start invokes the dispatcher's own Start so
// the goroutine lifetime is managed alongside the other workers.
//
// Nil-safe — passing nil repos keeps the dispatcher off, leaving the
// existing milestone-invoicer + vendor-metrics chain undisturbed.
func (r *Runner) WithTechnicianPushDispatcher(
	ewos port.EWOMobileRepository,
	pushLog port.EWOPushLogRepository,
	notifier *notifyx.Dispatcher,
) *Runner {
	if ewos == nil || pushLog == nil {
		return r
	}
	r.technicianPush = NewTechnicianPushDispatcher(r.pool, ewos, pushLog, notifier, r.log)
	return r
}
