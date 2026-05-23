// Wave 120 — probe anti-flap edge.
//
// Pins TC-NSM-* / TC-FAM-* "a single critical probe sample must NOT
// open a fault; only the SECOND consecutive critical sample (or
// later) triggers fault creation". The anti-flap policy lives in
// internal/nocmon/cron/cron.go::tickProbes, which calls
// HealthSampleRepository.CountConsecutive(probeID, Critical, 3) and
// only opens a fault when the streak >= 2.
//
// This test exercises the contract by:
//  1. Recording one critical sample → streak=1 → no fault opened.
//  2. Recording a second consecutive critical sample → streak=2 →
//     fault MAY be opened (depends on cron call). We exercise the
//     CountConsecutive contract directly.
//
// The cron itself is hard to unit-test (it owns timers + concurrency);
// this test pins the load-bearing API the cron depends on.

package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
)

func TestProbeService_AntiFlap_SingleCriticalDoesNotOpenFault(t *testing.T) {
	probes := newMemProbeRepo()
	samples := newMemSampleRepo()
	probeSvc := NewProbeService(probes, samples, nil)

	// Probe with warn=50ms critical=100ms (so 150 is critical).
	// RTT semantics: higher = worse. Warn at 50ms, critical at 100ms.
	warn := 50.0
	crit := 100.0
	probe, err := probeSvc.CreateProbe(context.Background(), port.CreateProbeInput{
		CustomerID:        uuid.New(),
		Kind:              domain.ProbeKindRTT,
		Target:            "host.example",
		IntervalSeconds:   60,
		ThresholdWarn:     &warn,
		ThresholdCritical: &crit,
	})
	if err != nil {
		t.Fatalf("CreateProbe: %v", err)
	}

	// Record ONE critical sample.
	now := time.Now().UTC()
	s1, err := probeSvc.RecordSample(context.Background(), probe.ID, 150.0, now)
	if err != nil {
		t.Fatalf("RecordSample 1: %v", err)
	}
	if s1.Status != domain.SampleStatusCritical {
		t.Fatalf("sample 1 status = %s, want critical (value 150 > critical 100)", s1.Status)
	}

	// CountConsecutive must return 1, not 2 — the anti-flap rule (>=2)
	// would block fault opening here.
	cnt, err := samples.CountConsecutive(context.Background(), probe.ID, domain.SampleStatusCritical, 3)
	if err != nil {
		t.Fatalf("CountConsecutive: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected streak = 1 after a single critical, got %d", cnt)
	}
}

func TestProbeService_AntiFlap_TwoConsecutiveCriticalsAllowFault(t *testing.T) {
	probes := newMemProbeRepo()
	samples := newMemSampleRepo()
	probeSvc := NewProbeService(probes, samples, nil)

	// RTT semantics: higher = worse. Warn at 50ms, critical at 100ms.
	warn := 50.0
	crit := 100.0
	probe, err := probeSvc.CreateProbe(context.Background(), port.CreateProbeInput{
		CustomerID:        uuid.New(),
		Kind:              domain.ProbeKindRTT,
		Target:            "host.example",
		IntervalSeconds:   60,
		ThresholdWarn:     &warn,
		ThresholdCritical: &crit,
	})
	if err != nil {
		t.Fatalf("CreateProbe: %v", err)
	}

	base := time.Now().UTC()
	_, err = probeSvc.RecordSample(context.Background(), probe.ID, 150.0, base.Add(-1*time.Minute))
	if err != nil {
		t.Fatalf("RecordSample 1: %v", err)
	}
	_, err = probeSvc.RecordSample(context.Background(), probe.ID, 175.0, base)
	if err != nil {
		t.Fatalf("RecordSample 2: %v", err)
	}

	cnt, _ := samples.CountConsecutive(context.Background(), probe.ID, domain.SampleStatusCritical, 3)
	if cnt < 2 {
		t.Fatalf("expected streak >= 2 (anti-flap threshold), got %d", cnt)
	}
}

func TestProbeService_AntiFlap_RecoverySampleResetsStreak(t *testing.T) {
	probes := newMemProbeRepo()
	samples := newMemSampleRepo()
	probeSvc := NewProbeService(probes, samples, nil)

	// RTT semantics: higher = worse. Warn at 50ms, critical at 100ms.
	warn := 50.0
	crit := 100.0
	probe, err := probeSvc.CreateProbe(context.Background(), port.CreateProbeInput{
		CustomerID:        uuid.New(),
		Kind:              domain.ProbeKindRTT,
		Target:            "host.example",
		IntervalSeconds:   60,
		ThresholdWarn:     &warn,
		ThresholdCritical: &crit,
	})
	if err != nil {
		t.Fatalf("CreateProbe: %v", err)
	}

	base := time.Now().UTC()
	// Critical, critical, then OK — the OK breaks the streak.
	_, _ = probeSvc.RecordSample(context.Background(), probe.ID, 150.0, base.Add(-2*time.Minute))
	_, _ = probeSvc.RecordSample(context.Background(), probe.ID, 175.0, base.Add(-1*time.Minute))
	_, err = probeSvc.RecordSample(context.Background(), probe.ID, 20.0, base) // < warn → ok
	if err != nil {
		t.Fatalf("RecordSample recovery: %v", err)
	}

	cnt, _ := samples.CountConsecutive(context.Background(), probe.ID, domain.SampleStatusCritical, 3)
	if cnt != 0 {
		t.Errorf("streak after OK recovery = %d, want 0 (the latest sample is OK so streak resets)", cnt)
	}
}
