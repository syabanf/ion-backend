package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — Subscriber state-machine contract tests (TC-SM-SUB-*)
//
//	active ↔ suspended → terminated  (terminal)
//
// Suspend / Reactivate / Terminate are idempotent on the destination
// state. Terminate is a one-way arrow from any non-terminated state.
// =====================================================================

func newSubscriberAt(t *testing.T, status SubscriberStatus) *Subscriber {
	t.Helper()
	s, err := NewSubscriber(uuid.New(), "Sub Name", "x@y.z", "+62", 100)
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	now := time.Now()
	switch status {
	case SubscriberStatusActive:
	case SubscriberStatusSuspended:
		if err := s.Suspend("non-payment", now); err != nil {
			t.Fatalf("Suspend: %v", err)
		}
	case SubscriberStatusTerminated:
		if err := s.Terminate(now); err != nil {
			t.Fatalf("Terminate: %v", err)
		}
	}
	return s
}

func TestSubscriberSM_ValidTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		from       SubscriberStatus
		action     func(*Subscriber) error
		wantStatus SubscriberStatus
	}{
		{"active -> suspended", SubscriberStatusActive, func(s *Subscriber) error { return s.Suspend("breach", now) }, SubscriberStatusSuspended},
		{"suspended -> active (Reactivate)", SubscriberStatusSuspended, func(s *Subscriber) error { return s.Reactivate(now) }, SubscriberStatusActive},
		{"active -> terminated", SubscriberStatusActive, func(s *Subscriber) error { return s.Terminate(now) }, SubscriberStatusTerminated},
		{"suspended -> terminated", SubscriberStatusSuspended, func(s *Subscriber) error { return s.Terminate(now) }, SubscriberStatusTerminated},
		// Idempotent destinations
		{"suspended -> suspended (re-Suspend updates reason)", SubscriberStatusSuspended, func(s *Subscriber) error { return s.Suspend("new reason", now) }, SubscriberStatusSuspended},
		{"active -> active (Reactivate)", SubscriberStatusActive, func(s *Subscriber) error { return s.Reactivate(now) }, SubscriberStatusActive},
		{"terminated -> terminated (idempotent)", SubscriberStatusTerminated, func(s *Subscriber) error { return s.Terminate(now) }, SubscriberStatusTerminated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSubscriberAt(t, tc.from)
			if err := tc.action(s); err != nil {
				t.Fatalf("action: %v", err)
			}
			if s.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", s.Status, tc.wantStatus)
			}
		})
	}
}

func TestSubscriberSM_InvalidTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		from     SubscriberStatus
		action   func(*Subscriber) error
		wantCode string
	}{
		{"terminated -> suspended", SubscriberStatusTerminated, func(s *Subscriber) error { return s.Suspend("post-term", now) }, "subscriber.cannot_suspend"},
		{"terminated -> active (Reactivate)", SubscriberStatusTerminated, func(s *Subscriber) error { return s.Reactivate(now) }, "subscriber.cannot_reactivate"},
		// Reason validation
		{"suspend without reason", SubscriberStatusActive, func(s *Subscriber) error { return s.Suspend("", now) }, "subscriber.suspend_reason_required"},
		{"suspend with whitespace-only reason", SubscriberStatusActive, func(s *Subscriber) error { return s.Suspend("   ", now) }, "subscriber.suspend_reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSubscriberAt(t, tc.from)
			err := tc.action(s)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", s.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("err type: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}
