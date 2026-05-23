// Wave 118 — Offline-queue replay idempotency tests (TC-WO-* / TC-SAP-* / TC-CAP-*).

package usecase

import (
	"testing"

	"github.com/google/uuid"
)

func TestEvaluateReplay_FirstSubmit(t *testing.T) {
	actor := uuid.New()
	got := EvaluateReplay(actor, "k1", "digest-a", nil)
	if got != IdempotencyFirstSubmit {
		t.Fatalf("fresh submit: want first_submit, got %s", got)
	}
}

func TestEvaluateReplay_SameKeyAndDigest_ReplayMatch(t *testing.T) {
	actor := uuid.New()
	existing := &IdempotencyRecord{ActorID: actor, IdempotencyKey: "k1", PayloadDigest: "digest-a"}
	got := EvaluateReplay(actor, "k1", "digest-a", existing)
	if got != IdempotencyReplayMatch {
		t.Fatalf("same key+digest: want replay_match, got %s", got)
	}
}

func TestEvaluateReplay_SameKey_DifferentDigest_Conflict(t *testing.T) {
	actor := uuid.New()
	existing := &IdempotencyRecord{ActorID: actor, IdempotencyKey: "k1", PayloadDigest: "digest-a"}
	got := EvaluateReplay(actor, "k1", "digest-DIFFERENT", existing)
	if got != IdempotencyReplayConflict {
		t.Fatalf("same key, different digest: want replay_conflict, got %s", got)
	}
}

func TestEvaluateReplay_DifferentKey_FirstSubmit(t *testing.T) {
	actor := uuid.New()
	// Different key in the existing record → treat as a wrong-row-passed-in.
	// We rely on the caller's lookup; an "existing" with a key that
	// doesn't match means the caller didn't actually find a prior
	// submission for THIS key → first_submit.
	existing := &IdempotencyRecord{ActorID: actor, IdempotencyKey: "k1", PayloadDigest: "digest-a"}
	got := EvaluateReplay(actor, "k2", "digest-b", existing)
	if got != IdempotencyFirstSubmit {
		t.Fatalf("different key: want first_submit, got %s", got)
	}
}

func TestEvaluateReplay_DifferentActor_Conflict(t *testing.T) {
	actor1 := uuid.New()
	actor2 := uuid.New()
	existing := &IdempotencyRecord{ActorID: actor1, IdempotencyKey: "k1", PayloadDigest: "digest-a"}
	got := EvaluateReplay(actor2, "k1", "digest-a", existing)
	if got != IdempotencyReplayConflict {
		t.Fatalf("cross-actor key reuse: want replay_conflict, got %s", got)
	}
}

func TestEvaluateReplay_EmptyKey_FirstSubmit(t *testing.T) {
	actor := uuid.New()
	got := EvaluateReplay(actor, "", "digest-a", &IdempotencyRecord{
		ActorID: actor, IdempotencyKey: "k-old", PayloadDigest: "digest-a",
	})
	if got != IdempotencyFirstSubmit {
		t.Fatalf("empty key disables replay protection: want first_submit, got %s", got)
	}
}
