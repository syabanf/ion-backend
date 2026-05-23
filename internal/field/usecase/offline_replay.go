// Wave 118 — Offline-queue replay idempotency helper (TC-WO-* / TC-SAP-* / TC-CAP-*).
//
// The technician app + customer app produce actions offline (BAST submits,
// check-ins, photo uploads, etc.) and replay them when connectivity returns.
// Without an idempotency-key gate, the same action submitted twice produces
// two rows — for BAST, that means two physical-paperwork claims; for check-
// in, two clock-in audit entries. The audit (Wave 110) flagged this as a
// regression edge.
//
// This file is a pure-domain helper. Callers compute an idempotency-key
// (typically: actor_id + action_kind + client-generated nonce), look up
// any prior submission with the same key, and either return the canonical
// stored result OR record the new submission.
//
// Production note: the enterprise EWOChecklistProgress already implements
// the same pattern in internal/enterprise/usecase/technician_mobile.go
// (UNIQUE (ewo_id, idempotency_key) constraint, see migration 0069).
// This helper exists for callers that don't yet have a typed table-level
// constraint — they reach for the in-memory check instead.

package usecase

import (
	"strings"

	"github.com/google/uuid"
)

// IdempotencyOutcome is the result of a replay-aware submission.
type IdempotencyOutcome string

const (
	// IdempotencyFirstSubmit means the caller can persist a new row.
	IdempotencyFirstSubmit IdempotencyOutcome = "first_submit"
	// IdempotencyReplayMatch means the same idempotency_key has been
	// processed before; the caller must return the canonical stored
	// row instead of writing a new one.
	IdempotencyReplayMatch IdempotencyOutcome = "replay_match"
	// IdempotencyReplayConflict means the same idempotency_key was used
	// before with a DIFFERENT payload — surfaces a 409 to the client so
	// they don't silently overwrite.
	IdempotencyReplayConflict IdempotencyOutcome = "replay_conflict"
)

// IdempotencyRecord is the projection a caller passes to EvaluateReplay.
// Production callers fetch this from the per-aggregate repository by
// (actor_id, idempotency_key).
type IdempotencyRecord struct {
	ActorID        uuid.UUID
	IdempotencyKey string
	// PayloadDigest is a stable hash of the prior submission's payload.
	// For BAST that might be sha256(bast_kind || total_otc || items_hash);
	// for check-in, sha256(location || timestamp_minute). The exact hash
	// is the caller's choice — the helper just compares bytes.
	PayloadDigest string
}

// EvaluateReplay returns the outcome for a fresh submission. If existing
// is nil, the helper returns first_submit. Otherwise it compares the
// idempotency_key + payload digest:
//   - same key + same digest → replay_match (return canonical row)
//   - same key + different digest → replay_conflict (409)
//
// Empty idempotencyKey → first_submit unconditionally (no replay protection).
func EvaluateReplay(actorID uuid.UUID, idempotencyKey, payloadDigest string, existing *IdempotencyRecord) IdempotencyOutcome {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return IdempotencyFirstSubmit
	}
	if existing == nil {
		return IdempotencyFirstSubmit
	}
	if existing.IdempotencyKey != idempotencyKey {
		// Caller passed the wrong existing row — treat as fresh.
		return IdempotencyFirstSubmit
	}
	if existing.ActorID != actorID {
		// Different actor reusing a key — definitely a conflict, hint at
		// possible idempotency-key collision across users.
		return IdempotencyReplayConflict
	}
	if existing.PayloadDigest != payloadDigest {
		return IdempotencyReplayConflict
	}
	return IdempotencyReplayMatch
}
