package usecase

import (
	"context"
	"time"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
)

// RunVendorSLAReminderSweep is the cron-callable ticker for Edge #5 / E4.
//
// For every BOQ line where:
//   - vendor_due_at IS NOT NULL
//   - vendor_unit_cost IS NULL (vendor hasn't filled it in)
//   - parent BOQ is still draft/revision_draft (line is mutable)
//
// we evaluate the remaining time and fire (once per bucket):
//   T-24h    — remaining is 24–48h from due_at
//   T-8h     — remaining is 0–24h from due_at
//   overdue  — due_at has passed
//
// Dedup via the boq_line_reminders table — the repo uses
// ON CONFLICT (boq_line_id, bucket) DO NOTHING so re-runs are idempotent.
//
// Returns the count of fresh reminders fired this run.
func (s *Service) RunVendorSLAReminderSweep(ctx context.Context) (int, error) {
	if s.boqLines == nil {
		return 0, nil
	}
	sweeper, ok := s.boqLines.(port.VendorSLASweeper)
	if !ok {
		// Repo implementation doesn't expose the sweep helper — silent
		// no-op so a future swap to a different repo doesn't break the
		// scheduler.
		return 0, nil
	}
	now := time.Now().UTC()
	due, err := sweeper.ListVendorDueLines(ctx)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, d := range due {
		remaining := d.VendorDueAt.Sub(now)
		var bucket, title, body string
		var severity domain.NotificationSeverity
		switch {
		case remaining > 48*time.Hour:
			continue
		case remaining > 24*time.Hour:
			bucket = "t_minus_24h"
			title = "Vendor cost due in ~24h"
			body = "Submit vendor_unit_cost on BOQ line " + d.SKU + " — SLA window closes soon."
			severity = domain.NotificationSeverityInfo
		case remaining > 0:
			bucket = "t_minus_8h"
			title = "Vendor cost due soon"
			body = "Less than 24 hours remain to submit vendor_unit_cost on BOQ line " + d.SKU + "."
			severity = domain.NotificationSeverityWarn
		default:
			bucket = "overdue"
			title = "Vendor cost overdue"
			body = "BOQ line " + d.SKU + " is past its SLA window. The line will be flagged on the next review."
			severity = domain.NotificationSeverityCritical
		}
		ok, err := sweeper.RecordVendorReminder(ctx, d.LineID, bucket)
		if err != nil {
			if s.log != nil {
				s.log.Warn("vendor sla reminder write failed",
					"line_id", d.LineID.String(), "err", err.Error())
			}
			continue
		}
		if !ok {
			continue // already fired for this bucket
		}
		if d.ProviderUserID != nil {
			s.Notify(ctx, domain.NewNotification(
				*d.ProviderUserID,
				"boq_line.vendor_due_"+bucket,
				"boq_line", d.LineID,
				title, body, severity,
			))
		}
		fired++
	}
	return fired, nil
}
