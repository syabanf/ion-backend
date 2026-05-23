package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewOpnameTabletSession_Validates(t *testing.T) {
	if _, err := NewOpnameTabletSession(uuid.Nil, uuid.New(), "tab-1"); err == nil {
		t.Fatal("expected error on nil opname_session_id")
	}
	if _, err := NewOpnameTabletSession(uuid.New(), uuid.Nil, "tab-1"); err == nil {
		t.Fatal("expected error on nil tech")
	}
	if _, err := NewOpnameTabletSession(uuid.New(), uuid.New(), ""); err == nil {
		t.Fatal("expected error on empty device_id")
	}
	s, err := NewOpnameTabletSession(uuid.New(), uuid.New(), "tab-1")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if s.SyncStatus != OpnameTabletSyncInProgress {
		t.Fatalf("expected in_progress, got %s", s.SyncStatus)
	}
}

func TestOpnameTabletSession_MarkSynced(t *testing.T) {
	s, _ := NewOpnameTabletSession(uuid.New(), uuid.New(), "tab-1")
	if err := s.MarkSynced("hash-abc", 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.SyncStatus != OpnameTabletSyncSynced {
		t.Fatalf("expected synced, got %s", s.SyncStatus)
	}
	if s.OfflinePayloadHash != "hash-abc" {
		t.Fatalf("expected hash recorded")
	}
	if s.TotalScans != 42 {
		t.Fatalf("expected total_scans=42")
	}
	if s.LastSyncedAt == nil {
		t.Fatal("expected last_synced_at set")
	}
}

func TestOpnameTabletSession_MarkReconciled(t *testing.T) {
	s, _ := NewOpnameTabletSession(uuid.New(), uuid.New(), "tab-1")
	// Reconcile before sync — should fail.
	if err := s.MarkReconciled(); err == nil {
		t.Fatal("expected refusal to reconcile in_progress session")
	}
	_ = s.MarkSynced("h", 1)
	if err := s.MarkReconciled(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.SyncStatus != OpnameTabletSyncReconciled {
		t.Fatalf("expected reconciled, got %s", s.SyncStatus)
	}
	// Re-sync after reconcile — should fail.
	if err := s.MarkSynced("h2", 2); err == nil {
		t.Fatal("expected refusal to re-sync after reconcile")
	}
}

func TestOpnameTabletSession_MarkFailedAllowsRetry(t *testing.T) {
	s, _ := NewOpnameTabletSession(uuid.New(), uuid.New(), "tab-1")
	s.MarkFailed("network timeout")
	if s.SyncStatus != OpnameTabletSyncFailed {
		t.Fatalf("expected failed, got %s", s.SyncStatus)
	}
	// Retry allowed after failure.
	if err := s.MarkSynced("h", 5); err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
}
