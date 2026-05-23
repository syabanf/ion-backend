package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 108 — Edge #7: IC-PO accept before issue
//
// A draft IC-PO must NOT accept-jump straight from draft to accepted —
// the issued step is the gate the receiving sister sees. Accepting a
// still-Draft IC-PO returns Conflict with the canonical
// `intercompany_po.invalid_state_transition` code. The state machine
// pins this in domain (see intercompany_po.go::Accept); this test
// proves the service layer surfaces the same error without converting
// it into something softer like Validation or a 200/no-op.
// =====================================================================

func TestIntercompanyPOAcceptBeforeIssue_ReturnsConflict(t *testing.T) {
	po, err := domain.NewIntercompanyPO(
		uuid.New(), uuid.New(),
		uuid.New(), uuid.New(),
		"ICPO-DRAFT",
	)
	if err != nil {
		t.Fatalf("NewIntercompanyPO: %v", err)
	}
	if po.Status != domain.IntercompanyPOStatusDraft {
		t.Fatalf("setup status = %q, want draft", po.Status)
	}

	repo := &stubIntercompanyPORepo{row: po}
	svc := (&Service{}).WithIntercompanyPOs(repo, nil)

	uid := uuid.New()
	// Skip the Issue step entirely — the catalog edge case (#7) is that a
	// receiving sister tries to short-circuit acceptance and the system
	// refuses.
	_, err = svc.AcceptIntercompanyPO(context.Background(), po.ID, &uid)
	if err == nil {
		t.Fatal("AcceptIntercompanyPO on draft must fail with Conflict")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "intercompany_po.invalid_state_transition" {
		t.Errorf("code = %q, want intercompany_po.invalid_state_transition", de.Code)
	}
	// The row must remain in draft — failed transitions must NOT mutate
	// the in-memory row past the domain rejection point.
	if po.Status != domain.IntercompanyPOStatusDraft {
		t.Errorf("status after failed accept = %q, want draft", po.Status)
	}
}

// TestIntercompanyPOAcceptBeforeIssue_OnRejectedAlsoFails — once the
// row is in a terminal state (rejected here), Accept must still be a
// Conflict, NOT a "draft only" specific message. The state-machine
// contract is "accept only from issued"; everything else is a Conflict.
func TestIntercompanyPOAcceptBeforeIssue_OnRejectedAlsoFails(t *testing.T) {
	po, err := domain.NewIntercompanyPO(
		uuid.New(), uuid.New(),
		uuid.New(), uuid.New(),
		"ICPO-REJ",
	)
	if err != nil {
		t.Fatalf("NewIntercompanyPO: %v", err)
	}
	if err := po.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := po.Reject("decline"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	repo := &stubIntercompanyPORepo{row: po}
	svc := (&Service{}).WithIntercompanyPOs(repo, nil)

	uid := uuid.New()
	_, err = svc.AcceptIntercompanyPO(context.Background(), po.ID, &uid)
	if err == nil {
		t.Fatal("AcceptIntercompanyPO on rejected must fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict (got %v)", de.Kind, de.Kind)
	}
}
