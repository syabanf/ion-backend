package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Wave 103 — Technician mobile API ports
//
// The technician mobile surface deliberately defines its own narrow
// interfaces (EWOMobileRepository, EWOPushLogRepository,
// EWOChecklistProgressRepository) instead of widening the existing
// EWORepository. Two reasons:
//
//   1. Scope isolation. The mobile read path NEVER hits EWO-X — the
//      adapter implementation hard-codes side='y' into every query so
//      the load-bearing scope rule is enforced in one place, not on
//      every caller.
//   2. Future split. When this bounded context graduates to its own
//      service binary, the mobile surface is a candidate to move into
//      a dedicated /mobile sub-binary; keeping the interfaces narrow
//      makes that mechanical refactor.
// =====================================================================

// EWOMobileFilter narrows the EWO list to the technician's view.
type EWOMobileFilter struct {
	StatusIn []domain.EWOStatus
	// From/To restrict to EWOs whose [ScheduledStart, ScheduledEnd]
	// overlaps the supplied window. Either or both can be nil.
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

// EWOMobileRepository is the technician-scoped read surface. Every
// method MUST enforce side='y' AND assigned_technician_user_id =
// technicianID in the underlying SQL — leaking an EWO-X row to a
// technician would expose commercial details the head-office doesn't
// want field staff to see.
type EWOMobileRepository interface {
	// AssignedToTechnician returns EWO-Y rows scoped to the supplied
	// technician. The filter narrows by status + scheduled-window range;
	// callers that don't supply a filter get all assigned rows.
	AssignedToTechnician(
		ctx context.Context,
		technicianID uuid.UUID,
		filter EWOMobileFilter,
	) ([]domain.EWO, error)

	// GetForTechnician returns one EWO IF it is assigned to the
	// supplied technician (and is side='y'). Returns NotFound when the
	// row exists but is assigned to someone else — DO NOT leak the
	// existence of the EWO id to a non-assigned actor.
	GetForTechnician(
		ctx context.Context,
		technicianID, ewoID uuid.UUID,
	) (*domain.EWO, error)
}

// EWOChecklistProgressRepository persists per-EWO checklist instance
// data for the mobile workflow.
type EWOChecklistProgressRepository interface {
	// ListByEWO returns all progress rows for the supplied EWO.
	ListByEWO(ctx context.Context, ewoID uuid.UUID) ([]domain.EWOChecklistProgress, error)

	// FindByIdempotencyKey returns the existing progress row for a
	// given (ewo_id, idempotency_key) pair. Returns NotFound when no
	// such row exists — used by the upsert path to detect replay.
	FindByIdempotencyKey(
		ctx context.Context,
		ewoID uuid.UUID,
		idempotencyKey string,
	) (*domain.EWOChecklistProgress, error)

	// Upsert inserts a new progress row OR returns the existing row if
	// the (ewo_id, idempotency_key) conflict fires. The implementation
	// is responsible for the ON CONFLICT … DO NOTHING + re-fetch dance
	// so the caller gets a consistent "this row is now the persisted
	// state" guarantee.
	Upsert(ctx context.Context, p *domain.EWOChecklistProgress) error
}

// EWOPushLogRepository writes the audit + dedup log for the push cron.
type EWOPushLogRepository interface {
	Create(ctx context.Context, e *domain.EWOPushEvent) error

	// ListForUser returns the most recent N pushes targeted at a given
	// user. Mobile clients read this for an in-app "what did you send
	// me" surface; the cron uses HasSubject for dedup.
	ListForUser(
		ctx context.Context,
		userID uuid.UUID,
		limit int,
	) ([]domain.EWOPushEvent, error)

	// HasSubject reports whether a push of the given subject for
	// (ewo_id, target_user_id) was already recorded. Used by the cron
	// to keep one-shot subjects idempotent.
	HasSubject(
		ctx context.Context,
		ewoID, targetUserID uuid.UUID,
		subject domain.EWOPushSubject,
	) (bool, error)
}
