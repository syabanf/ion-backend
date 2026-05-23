package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 113 — RMARecord state-machine tests.
//
// Lifecycle:
//
//	open → shipped → received → replaced → closed
//	                         ↘ rejected → closed
//	any-non-closed → expired (after 90d untouched; cron-driven)
// =====================================================================

func newRMAAt(t *testing.T, status RMAStatus) *RMARecord {
	t.Helper()
	r, err := NewRMARecord(uuid.New(), "TestVendor", "dead-on-arrival", nil)
	if err != nil {
		t.Fatalf("NewRMARecord: %v", err)
	}
	at := time.Now().UTC()
	switch status {
	case RMAStatusOpen:
	case RMAStatusShipped:
		if err := r.MarkShipped("VRMA-1", at); err != nil {
			t.Fatalf("MarkShipped: %v", err)
		}
	case RMAStatusReceived:
		if err := r.MarkShipped("VRMA-1", at); err != nil {
			t.Fatalf("MarkShipped: %v", err)
		}
		if err := r.MarkReceived("REP-1", at); err != nil {
			t.Fatalf("MarkReceived: %v", err)
		}
	case RMAStatusReplaced:
		if err := r.MarkShipped("VRMA-1", at); err != nil {
			t.Fatalf("MarkShipped: %v", err)
		}
		if err := r.MarkReceived("REP-1", at); err != nil {
			t.Fatalf("MarkReceived: %v", err)
		}
		if err := r.MarkReplaced(); err != nil {
			t.Fatalf("MarkReplaced: %v", err)
		}
	case RMAStatusRejected:
		if err := r.MarkShipped("VRMA-1", at); err != nil {
			t.Fatalf("MarkShipped: %v", err)
		}
		if err := r.MarkReceived("REP-1", at); err != nil {
			t.Fatalf("MarkReceived: %v", err)
		}
		if err := r.MarkRejected("damage out of warranty"); err != nil {
			t.Fatalf("MarkRejected: %v", err)
		}
	case RMAStatusClosed:
		if err := r.MarkShipped("VRMA-1", at); err != nil {
			t.Fatalf("MarkShipped: %v", err)
		}
		if err := r.MarkReceived("REP-1", at); err != nil {
			t.Fatalf("MarkReceived: %v", err)
		}
		if err := r.MarkReplaced(); err != nil {
			t.Fatalf("MarkReplaced: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	return r
}

func TestRMASM_ValidTransitions(t *testing.T) {
	at := time.Now().UTC()
	cases := []struct {
		name       string
		from       RMAStatus
		action     func(*RMARecord) error
		wantStatus RMAStatus
	}{
		{"open → shipped", RMAStatusOpen,
			func(r *RMARecord) error { return r.MarkShipped("V1", at) }, RMAStatusShipped},
		{"shipped → received", RMAStatusShipped,
			func(r *RMARecord) error { return r.MarkReceived("R1", at) }, RMAStatusReceived},
		{"received → replaced", RMAStatusReceived,
			func(r *RMARecord) error { return r.MarkReplaced() }, RMAStatusReplaced},
		{"received → rejected", RMAStatusReceived,
			func(r *RMARecord) error { return r.MarkRejected("no warranty") }, RMAStatusRejected},
		{"shipped → rejected (early)", RMAStatusShipped,
			func(r *RMARecord) error { return r.MarkRejected("declined") }, RMAStatusRejected},
		{"replaced → closed", RMAStatusReplaced,
			func(r *RMARecord) error { return r.Close() }, RMAStatusClosed},
		{"rejected → closed", RMAStatusRejected,
			func(r *RMARecord) error { return r.Close() }, RMAStatusClosed},
		// Idempotence
		{"open → open (idempotent ship pre-call: no-op via re-shipped check)",
			RMAStatusShipped,
			func(r *RMARecord) error { return r.MarkShipped("V1", at) }, RMAStatusShipped},
		{"closed → closed", RMAStatusClosed,
			func(r *RMARecord) error { return r.Close() }, RMAStatusClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRMAAt(t, tc.from)
			if err := tc.action(r); err != nil {
				t.Fatalf("action: %v", err)
			}
			if r.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", r.Status, tc.wantStatus)
			}
		})
	}
}

func TestRMASM_InvalidTransitions(t *testing.T) {
	at := time.Now().UTC()
	cases := []struct {
		name     string
		from     RMAStatus
		action   func(*RMARecord) error
		wantCode string
	}{
		{"open → received (skip ship)", RMAStatusOpen,
			func(r *RMARecord) error { return r.MarkReceived("R1", at) },
			"rma.invalid_state_transition"},
		{"open → replaced (skip ship+receive)", RMAStatusOpen,
			func(r *RMARecord) error { return r.MarkReplaced() },
			"rma.invalid_state_transition"},
		{"closed → reopen via ship", RMAStatusClosed,
			func(r *RMARecord) error { return r.MarkShipped("V2", at) },
			"rma.invalid_state_transition"},
		{"replaced → reject", RMAStatusReplaced,
			func(r *RMARecord) error { return r.MarkRejected("no") },
			"rma.invalid_state_transition"},
		{"rejected → replaced", RMAStatusRejected,
			func(r *RMARecord) error { return r.MarkReplaced() },
			"rma.invalid_state_transition"},
		{"open → close (skip workflow)", RMAStatusOpen,
			func(r *RMARecord) error { return r.Close() },
			"rma.invalid_state_transition"},
		// Validation
		{"rejected without reason", RMAStatusReceived,
			func(r *RMARecord) error { return r.MarkRejected("  ") },
			"rma.reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRMAAt(t, tc.from)
			err := tc.action(r)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", r.Status)
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

func TestRMAExpire(t *testing.T) {
	r := newRMAAt(t, RMAStatusOpen)
	// Force the updated_at back 100 days.
	r.UpdatedAt = time.Now().UTC().Add(-100 * 24 * time.Hour)
	changed, err := r.Expire(time.Now().UTC())
	if err != nil || !changed {
		t.Fatalf("Expire should flip the record: changed=%v err=%v", changed, err)
	}
	if r.Status != RMAStatusExpired {
		t.Errorf("status = %s, want expired", r.Status)
	}
	// Fresh record stays put.
	r2 := newRMAAt(t, RMAStatusOpen)
	changed, _ = r2.Expire(time.Now().UTC())
	if changed {
		t.Fatalf("fresh record should not expire")
	}
	if r2.Status != RMAStatusOpen {
		t.Errorf("fresh status = %s, want open", r2.Status)
	}
	// Closed records don't expire.
	r3 := newRMAAt(t, RMAStatusClosed)
	r3.UpdatedAt = time.Now().UTC().Add(-200 * 24 * time.Hour)
	changed, _ = r3.Expire(time.Now().UTC())
	if changed {
		t.Fatalf("closed RMA should not auto-expire")
	}
}

func TestEvaluateDevice(t *testing.T) {
	d := Device{FirmwareVersion: "v1"}
	rec := &FirmwareVersion{Version: "v1"}
	if v := EvaluateDevice(d, rec); v != ComplianceCompliant {
		t.Errorf("matching version should be compliant, got %s", v)
	}
	rec.Version = "v2"
	if v := EvaluateDevice(d, rec); v != ComplianceNonCompliant {
		t.Errorf("mismatch non-critical should be non_compliant, got %s", v)
	}
	rec.IsCritical = true
	if v := EvaluateDevice(d, rec); v != ComplianceCriticalPending {
		t.Errorf("mismatch critical should be critical_pending, got %s", v)
	}
	if v := EvaluateDevice(d, nil); v != ComplianceUnknown {
		t.Errorf("nil recommended should be unknown, got %s", v)
	}
}

func TestComputeHealthScore(t *testing.T) {
	good := HealthSnapshot{}
	if got := ComputeHealthScore(good); got != 100 {
		t.Errorf("empty snapshot should score 100, got %d", got)
	}
	signal := -32.0
	loss := 6.0
	cpu := 96.0
	bad := HealthSnapshot{
		SignalDBM:     &signal,
		PacketLossPct: &loss,
		CPUPct:        &cpu,
	}
	got := ComputeHealthScore(bad)
	if got >= 60 {
		t.Errorf("bad snapshot should score < 60, got %d", got)
	}
	// Score floor.
	if got < 0 {
		t.Errorf("score should be clipped to 0, got %d", got)
	}
}
