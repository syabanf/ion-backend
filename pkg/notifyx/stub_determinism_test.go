// Wave 121E — notifyx StubPush determinism tests.
//
// The StubPush provider is the default push.Push implementation. It
// logs the payload + returns nil — useful in dev where no FCM / APNS
// credentials are wired.
//
// What the stub guarantees:
//   - Name() returns "stub" (operators filter outbox rows by provider).
//   - Send() returns nil for every Target shape (UserID / CustomerID /
//     UserIDs / empty).
//   - Send() does not panic on nil context, nil logger, empty message.
//
// What this DOES NOT validate:
//   - Real FCM delivery acknowledgements
//   - APNS feedback service handling
//   - WhatsApp Business API template versioning
//   - The Dispatcher's DB outbox write (requires pgx — covered in
//     notifyx_e2e_test.go against a real DB)
package notifyx

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// =====================================================================
// 1) Name() is stable and equals "stub".
// =====================================================================

func TestStubPush_Name(t *testing.T) {
	stub := NewStubPush(slog.Default())
	if stub.Name() != "stub" {
		t.Errorf("Name() = %q, want %q", stub.Name(), "stub")
	}
}

// =====================================================================
// 2) Send returns nil for every Target shape (UserID / CustomerID /
// bulk / empty).
//
// These are the four shapes the recordToOutbox switch handles. The
// stub provider treats all four identically — just logs + nil.
// =====================================================================

func TestStubPush_SendAllTargetShapes(t *testing.T) {
	stub := NewStubPush(slog.Default())
	ctx := context.Background()
	msg := Message{
		Title:    "Test",
		Body:     "Test body",
		DeepLink: "/x",
		Topic:    "test",
		Data:     map[string]string{"k": "v"},
	}

	userID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	customerID := uuid.MustParse("99999999-9999-9999-9999-999999999999")

	cases := []struct {
		name   string
		target Target
	}{
		{"UserID", Target{UserID: userID}},
		{"CustomerID", Target{CustomerID: customerID}},
		{"UserIDs", Target{UserIDs: []uuid.UUID{userID, customerID}}},
		{"empty", Target{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := stub.Send(ctx, c.target, msg); err != nil {
				t.Errorf("Send returned err: %v", err)
			}
		})
	}
}

// =====================================================================
// 3) Send is idempotent — N calls with the same payload return nil and
// never panic.
// =====================================================================

func TestStubPush_SendIdempotent(t *testing.T) {
	stub := NewStubPush(slog.Default())
	ctx := context.Background()
	target := Target{UserID: uuid.New()}
	msg := Message{Title: "T", Body: "B", Topic: "x"}

	for i := 0; i < 5; i++ {
		if err := stub.Send(ctx, target, msg); err != nil {
			t.Errorf("call %d returned err: %v", i, err)
		}
	}
}
