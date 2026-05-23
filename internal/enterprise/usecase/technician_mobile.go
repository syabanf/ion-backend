package usecase

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 103 — Technician Mobile Service
//
// Surface:
//   - ListMyAssignedEWOs   — EWO-Y rows assigned to the technician
//   - GetMyAssignedEWO     — single EWO with 404 on cross-technician
//   - ListChecklistProgress
//   - CompleteChecklistItem — done / skipped / blocked w/ idempotency
//   - ListMyPushLog        — recent pushes for the actor
//   - RecordPush           — internal hook for the cron dispatcher
//
// Authorization model:
//   - All read operations require the caller to be the actor identified
//     by the technicianUserID parameter. The HTTP handler reads
//     claims.UserID and passes it straight through.
//   - The mobile repo enforces the assignment check in SQL, so a
//     cross-technician access fails as NotFound (404), not Forbidden
//     (403). The 404 choice is deliberate: a 403 would confirm the
//     EWO id exists; the 404 is opaque.
//   - CompleteChecklistItem additionally re-fetches the EWO via the
//     mobile repo before persisting the write, so a stale ewoID +
//     idempotency_key pair from an attacker's offline replay can't be
//     used against an EWO they never owned.
// =====================================================================

// TechnicianMobileService bundles the mobile-scoped operations. It's a
// standalone service (not a method receiver on enterprise/Service) so
// the cron can take just the narrow port interfaces it needs without
// pulling in the full enterprise service surface.
type TechnicianMobileService struct {
	ewos         port.EWOMobileRepository
	pushLog      port.EWOPushLogRepository
	checklist    port.EWOChecklistProgressRepository
	auditWriter  audit.Writer
	log          *slog.Logger
}

func NewTechnicianMobileService(
	ewos port.EWOMobileRepository,
	pushLog port.EWOPushLogRepository,
	checklist port.EWOChecklistProgressRepository,
	log *slog.Logger,
) *TechnicianMobileService {
	return &TechnicianMobileService{
		ewos:        ewos,
		pushLog:     pushLog,
		checklist:   checklist,
		auditWriter: audit.Nop{},
		log:         log,
	}
}

// WithAudit attaches the audit writer used for checklist mutations.
// Nil-safe — when missing, audit calls are no-ops.
func (s *TechnicianMobileService) WithAudit(w audit.Writer) *TechnicianMobileService {
	if w != nil {
		s.auditWriter = w
	}
	return s
}

// ListMyAssignedEWOs returns EWO-Y rows scoped to the supplied
// technician. The repo layer hard-codes the side='y' filter so callers
// can't accidentally widen the scope.
func (s *TechnicianMobileService) ListMyAssignedEWOs(
	ctx context.Context,
	technicianUserID uuid.UUID,
	filter port.EWOMobileFilter,
) ([]domain.EWO, error) {
	if s.ewos == nil {
		return nil, errTechnicianMobileNotConfigured()
	}
	if technicianUserID == uuid.Nil {
		return nil, derrors.Validation(
			"technician.user_id_required",
			"technician user id is required",
		)
	}
	return s.ewos.AssignedToTechnician(ctx, technicianUserID, filter)
}

// GetMyAssignedEWO fetches a single EWO IF assigned to the technician.
// Returns NotFound (404) when the row exists but isn't assigned to this
// actor — see file header for the 404-not-403 rationale.
func (s *TechnicianMobileService) GetMyAssignedEWO(
	ctx context.Context,
	technicianUserID, ewoID uuid.UUID,
) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errTechnicianMobileNotConfigured()
	}
	if technicianUserID == uuid.Nil {
		return nil, derrors.Validation(
			"technician.user_id_required",
			"technician user id is required",
		)
	}
	e, err := s.ewos.GetForTechnician(ctx, technicianUserID, ewoID)
	if err != nil {
		// Translate the bare NotFound into a more pointed code so the
		// FE can distinguish "no such ewo" from "you can't access this
		// ewo" if it ever needs to. The HTTP status is the same (404)
		// for both because we never leak existence.
		var derr *derrors.Error
		if asDerror(err, &derr) && derr.Kind == derrors.KindNotFound {
			return nil, derrors.NotFound(
				"ewo.not_assigned",
				"ewo not found or not assigned to you",
			)
		}
		return nil, err
	}
	return e, nil
}

// ListChecklistProgress returns the per-EWO progress rows. Requires the
// technician to be assigned (re-fetches the EWO via the mobile repo to
// run the assignment check; same 404 semantics as GetMyAssignedEWO).
func (s *TechnicianMobileService) ListChecklistProgress(
	ctx context.Context,
	technicianUserID, ewoID uuid.UUID,
) ([]domain.EWOChecklistProgress, error) {
	if s.ewos == nil || s.checklist == nil {
		return nil, errTechnicianMobileNotConfigured()
	}
	if _, err := s.GetMyAssignedEWO(ctx, technicianUserID, ewoID); err != nil {
		return nil, err
	}
	return s.checklist.ListByEWO(ctx, ewoID)
}

// CompleteChecklistInput captures the mobile completion payload.
type CompleteChecklistInput struct {
	EWOID           uuid.UUID
	ChecklistItemID *uuid.UUID
	ItemLabel       string
	Status          domain.ChecklistItemStatus
	PhotoURL        *string
	PhotoHash       *string
	Notes           string
	IdempotencyKey  *string
}

// CompleteChecklistItem is the mobile-side mutation. It:
//
//  1. Re-fetches the EWO via the mobile repo so a cross-technician
//     replay attempt is rejected before any DB write.
//  2. Constructs a fresh EWOChecklistProgress in the requested terminal
//     state via the domain state machine.
//  3. Upserts. On (ewo_id, idempotency_key) conflict the repo silently
//     re-fetches and returns the canonical persisted state.
//  4. If a conflict was detected AND the recorded status diverges from
//     the new request, we surface a Conflict error so the mobile app
//     knows the offline write was already accepted with different
//     semantics — replay must not silently overwrite.
func (s *TechnicianMobileService) CompleteChecklistItem(
	ctx context.Context,
	technicianUserID uuid.UUID,
	in CompleteChecklistInput,
) (*domain.EWOChecklistProgress, error) {
	if s.ewos == nil || s.checklist == nil {
		return nil, errTechnicianMobileNotConfigured()
	}
	if technicianUserID == uuid.Nil {
		return nil, derrors.Validation(
			"technician.user_id_required",
			"technician user id is required",
		)
	}
	// 1. Scope check — same 404-not-403 semantics as the read path.
	if _, err := s.GetMyAssignedEWO(ctx, technicianUserID, in.EWOID); err != nil {
		return nil, err
	}
	// 2. Construct + mutate via the domain state machine.
	p, err := domain.NewEWOChecklistProgress(in.EWOID, in.ChecklistItemID, in.ItemLabel, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	switch in.Status {
	case domain.ChecklistItemDone:
		if err := p.MarkDone(technicianUserID, now, in.PhotoURL, in.PhotoHash); err != nil {
			return nil, err
		}
	case domain.ChecklistItemSkipped:
		if err := p.MarkSkipped(technicianUserID, now, in.Notes); err != nil {
			return nil, err
		}
	case domain.ChecklistItemBlocked:
		if err := p.MarkBlocked(technicianUserID, now, in.Notes); err != nil {
			return nil, err
		}
	default:
		return nil, derrors.Validation(
			"checklist_progress.invalid_status",
			"status must be one of: done, skipped, blocked",
		)
	}

	// 3. Check for an existing row keyed on the idempotency key BEFORE
	// the upsert. If we find one and its status diverges from the new
	// request, surface a clear conflict — silently returning the prior
	// row would hide a bug where two different mobile actions share an
	// idempotency_key.
	if in.IdempotencyKey != nil && *in.IdempotencyKey != "" {
		existing, ferr := s.checklist.FindByIdempotencyKey(ctx, in.EWOID, *in.IdempotencyKey)
		if ferr == nil && existing != nil {
			if existing.Status != p.Status {
				return nil, derrors.Conflict(
					"checklist.replay",
					"idempotency_key already processed with a different status",
				)
			}
			// Same status — treat as a pure no-op replay and return
			// the canonical persisted row.
			return existing, nil
		}
	}

	// 4. Persist. The upsert path is idempotent for the unique-conflict
	// case (returns the existing row); for non-conflict, we have a new
	// row.
	if err := s.checklist.Upsert(ctx, p); err != nil {
		return nil, err
	}

	// 5. Audit. The Reason field carries the photo hash when present —
	// gives auditors a tamper-evidence pointer without exposing the
	// full URL in the log table.
	reasonParts := []string{"checklist_item." + string(p.Status)}
	if p.PhotoHash != nil && *p.PhotoHash != "" {
		reasonParts = append(reasonParts, "photo_hash="+*p.PhotoHash)
	}
	audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
		UserID:     technicianUserID,
		Module:     "enterprise",
		RecordType: "enterprise.ewo",
		RecordID:   in.EWOID.String(),
		After:      string(p.Status),
		Reason:     strings.Join(reasonParts, " "),
	})
	return p, nil
}

// ListMyPushLog returns the recent push notifications targeted at the
// supplied user. Caller authorisation is "you may only see your own
// pushes" — handler enforces via claims.UserID.
func (s *TechnicianMobileService) ListMyPushLog(
	ctx context.Context,
	userID uuid.UUID,
	limit int,
) ([]domain.EWOPushEvent, error) {
	if s.pushLog == nil {
		return nil, errTechnicianMobileNotConfigured()
	}
	if userID == uuid.Nil {
		return nil, derrors.Validation(
			"technician.user_id_required",
			"technician user id is required",
		)
	}
	return s.pushLog.ListForUser(ctx, userID, limit)
}

// RecordPush is the cron-side hook to persist a push notification. It's
// idempotent in the dedup sense — the caller (cron) already consults
// HasSubject before calling this; here we just write the row.
func (s *TechnicianMobileService) RecordPush(
	ctx context.Context,
	ewoID uuid.UUID,
	subject domain.EWOPushSubject,
	targetUserID uuid.UUID,
	payload map[string]any,
) error {
	if s.pushLog == nil {
		return errTechnicianMobileNotConfigured()
	}
	return s.pushLog.Create(ctx, &domain.EWOPushEvent{
		ID:             uuid.New(),
		EWOID:          ewoID,
		Subject:        subject,
		TargetUserID:   targetUserID,
		Payload:        payload,
		SentAt:         time.Now().UTC(),
		DispatchStatus: "sent",
	})
}

// errTechnicianMobileNotConfigured is the canonical response when the
// service is constructed without its dependencies (defence against
// wiring bugs in cmd/main.go). The 500 with typed code makes it cheap
// to find in logs.
func errTechnicianMobileNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal,
		"technician_mobile.not_configured",
		"technician mobile service is not configured", nil)
}

// asDerror unwraps to *derrors.Error if possible.
func asDerror(err error, target **derrors.Error) bool {
	if err == nil {
		return false
	}
	derr, ok := err.(*derrors.Error)
	if !ok {
		return false
	}
	*target = derr
	return true
}
