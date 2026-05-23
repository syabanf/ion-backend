package domain

import (
	"testing"
	"time"
)

func TestNormalizeSeverity_LegacyAndPRD(t *testing.T) {
	cases := []struct {
		in   string
		want AnnouncementSeverity
	}{
		{"info", AnnouncementInfo},
		{"warning", AnnouncementImportant},
		{"important", AnnouncementImportant},
		{"critical", AnnouncementUrgent},
		{"urgent", AnnouncementUrgent},
		{"", AnnouncementInfo},
		{"random", AnnouncementInfo},
		{"  IMPORTANT  ", AnnouncementImportant},
	}
	for _, tc := range cases {
		if got := NormalizeSeverity(tc.in); got != tc.want {
			t.Errorf("NormalizeSeverity(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeAudience(t *testing.T) {
	if got := NormalizeAudience("technicians"); got != AudienceTechnicians {
		t.Errorf("want technicians, got %s", got)
	}
	if got := NormalizeAudience("anything"); got != AudienceAll {
		t.Errorf("want all (fallback), got %s", got)
	}
}

func TestAnnouncement_StateMachine_AllDelivered(t *testing.T) {
	a := &Announcement{DispatchStatus: DispatchPending}
	a.MarkDispatching(time.Now())
	if a.DispatchStatus != DispatchDispatching {
		t.Fatalf("expected dispatching, got %s", a.DispatchStatus)
	}
	now := time.Now()
	a.MarkAllRecipientsDelivered(now, 10)
	if a.DispatchStatus != DispatchDispatched {
		t.Fatalf("expected dispatched, got %s", a.DispatchStatus)
	}
	if a.SentCount != 10 {
		t.Fatalf("expected SentCount 10, got %d", a.SentCount)
	}
	if a.DispatchedAt == nil {
		t.Fatalf("expected DispatchedAt set")
	}
	if a.SentAt == nil {
		t.Fatalf("expected SentAt set")
	}
}

func TestAnnouncement_StateMachine_Partial(t *testing.T) {
	a := &Announcement{DispatchStatus: DispatchDispatching}
	now := time.Now()
	a.MarkSomeFailed(now, 7, 10)
	if a.DispatchStatus != DispatchPartial {
		t.Fatalf("expected partial, got %s", a.DispatchStatus)
	}
	if a.SentCount != 7 {
		t.Errorf("expected 7, got %d", a.SentCount)
	}
}

func TestAnnouncement_StateMachine_AllFailed(t *testing.T) {
	a := &Announcement{DispatchStatus: DispatchDispatching}
	a.MarkSomeFailed(time.Now(), 0, 5)
	if a.DispatchStatus != DispatchFailed {
		t.Fatalf("expected failed, got %s", a.DispatchStatus)
	}
}

func TestAnnouncement_MarkDispatching_Idempotent(t *testing.T) {
	a := &Announcement{DispatchStatus: DispatchDispatched}
	a.MarkDispatching(time.Now())
	if a.DispatchStatus != DispatchDispatched {
		t.Fatalf("dispatched -> dispatching must not regress; got %s", a.DispatchStatus)
	}
}

func TestRecipient_MarkRead_Idempotent(t *testing.T) {
	now := time.Now()
	r := &AnnouncementRecipient{}
	r.MarkRead(now)
	if r.ReadAt == nil {
		t.Fatalf("expected ReadAt set")
	}
	first := *r.ReadAt
	r.MarkRead(now.Add(time.Hour))
	if !r.ReadAt.Equal(first) {
		t.Fatalf("expected ReadAt unchanged on second call")
	}
}
