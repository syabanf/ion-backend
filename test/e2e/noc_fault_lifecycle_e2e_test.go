// Wave 121C — NOC monitoring context E2E.
//
// Exercises internal/nocmon end-to-end against live Postgres:
//
//   - Fault state machine: open → ack → investigate → mitigate → resolve
//   - Fault impact linking + count denorm
//   - Manual duplicate flag (cron-driven dedup is covered by unit tests;
//     we exercise the domain-method path the cron drives)
//   - Probe sample insert + anti-flap consecutive-critical count
//   - Fiber attenuation read + status threshold
//   - Topology snapshot create + read-back via TopologyService
//   - Alert WO conversion using the StubWorkOrderCreator
//
//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	nocmonpg "github.com/ion-core/backend/internal/nocmon/adapter/postgres"
	nocmondom "github.com/ion-core/backend/internal/nocmon/domain"
	nocmonport "github.com/ion-core/backend/internal/nocmon/port"
	nocmonuse "github.com/ion-core/backend/internal/nocmon/usecase"
)

type nocHarness struct {
	probes  *nocmonuse.ProbeService
	faults  *nocmonuse.FaultService
	fiber   *nocmonuse.FiberService
	alertWO *nocmonuse.AlertWOService

	probeRepo  *nocmonpg.ServiceProbeRepository
	sampleRepo *nocmonpg.HealthSampleRepository
	faultRepo  *nocmonpg.FaultEventRepository
	impactRepo *nocmonpg.FaultImpactRepository
	fiberRepo  *nocmonpg.FiberLinkRepository
	topoRepo   *nocmonpg.TopologySnapshotRepository
}

func newNocHarness(t *testing.T) *nocHarness {
	t.Helper()
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "nocmon.fault_events")

	probeRepo := nocmonpg.NewServiceProbeRepository(pool)
	sampleRepo := nocmonpg.NewHealthSampleRepository(pool)
	faultRepo := nocmonpg.NewFaultEventRepository(pool)
	impactRepo := nocmonpg.NewFaultImpactRepository(pool)
	fiberRepo := nocmonpg.NewFiberLinkRepository(pool)
	topoRepo := nocmonpg.NewTopologySnapshotRepository(pool)

	return &nocHarness{
		probes:     nocmonuse.NewProbeService(probeRepo, sampleRepo, nil),
		faults:     nocmonuse.NewFaultService(faultRepo, impactRepo, nil),
		fiber:      nocmonuse.NewFiberService(fiberRepo, nil),
		alertWO:    nocmonuse.NewAlertWOService(faultRepo, impactRepo, nocmonuse.StubWorkOrderCreator{}, nil),
		probeRepo:  probeRepo,
		sampleRepo: sampleRepo,
		faultRepo:  faultRepo,
		impactRepo: impactRepo,
		fiberRepo:  fiberRepo,
		topoRepo:   topoRepo,
	}
}

func TestNocFault_LifecycleHappyPath(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	by := uuid.New()
	srcID := uuid.New()
	f, err := h.faults.OpenFault(ctx, nocmonport.OpenFaultInput{
		Kind:       nocmondom.FaultKindManualOutage,
		Severity:   nocmondom.FaultSeverityHigh,
		SourceID:   &srcID,
		SourceKind: "wave121c-test",
	})
	if err != nil {
		t.Fatalf("OpenFault: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_events", "id", f.ID.String()))
	if f.Status != nocmondom.FaultStatusOpen {
		t.Fatalf("initial status: %q, want open", f.Status)
	}

	if _, err := h.faults.AcknowledgeFault(ctx, f.ID, by); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := h.faults.InvestigateFault(ctx, f.ID, by); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if _, err := h.faults.MitigateFault(ctx, f.ID, by, "wave 121c — replaced ODP"); err != nil {
		t.Fatalf("Mitigate: %v", err)
	}
	finally, err := h.faults.ResolveFault(ctx, f.ID, by)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if finally.Status != nocmondom.FaultStatusResolved {
		t.Fatalf("final status: %q, want resolved", finally.Status)
	}
	if finally.ResolvedAt == nil {
		t.Errorf("resolved_at not stamped")
	}
	if finally.RootCause != "wave 121c — replaced ODP" {
		t.Errorf("root_cause: %q, want our marker", finally.RootCause)
	}
}

func TestNocFault_ImpactLinkingAndCount(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	srcID := uuid.New()
	f, err := h.faults.OpenFault(ctx, nocmonport.OpenFaultInput{
		Kind:       nocmondom.FaultKindFiberDegradation,
		Severity:   nocmondom.FaultSeverityMedium,
		SourceID:   &srcID,
		SourceKind: "wave121c-impact",
	})
	if err != nil {
		t.Fatalf("OpenFault: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_events", "id", f.ID.String()))
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_impact_links", "fault_event_id", f.ID.String()))

	// Link 3 customers.
	custIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, cid := range custIDs {
		if _, err := h.faults.LinkImpact(ctx, nocmonport.LinkImpactInput{
			FaultEventID: f.ID,
			CustomerID:   cid,
			ImpactKind:   nocmondom.ImpactKindFullOutage,
			ImpactStart:  time.Now().UTC(),
		}); err != nil {
			t.Fatalf("LinkImpact %s: %v", cid, err)
		}
	}

	impacts, err := h.faults.ListImpact(ctx, f.ID)
	if err != nil {
		t.Fatalf("ListImpact: %v", err)
	}
	if len(impacts) != 3 {
		t.Errorf("impact count: got %d, want 3", len(impacts))
	}

	// Header denorm should match (UpdateStatus is best-effort but
	// shouldn't have failed for valid data).
	got, _ := h.faults.GetFault(ctx, f.ID)
	if got.CustomerImpactCount != 3 {
		t.Errorf("CustomerImpactCount denorm: got %d, want 3", got.CustomerImpactCount)
	}
}

func TestNocFault_MarkDuplicate(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	srcID := uuid.New()
	first, err := h.faults.OpenFault(ctx, nocmonport.OpenFaultInput{
		Kind:       nocmondom.FaultKindProbeCritical,
		Severity:   nocmondom.FaultSeverityHigh,
		SourceID:   &srcID,
		SourceKind: "wave121c-dup",
	})
	if err != nil {
		t.Fatalf("first OpenFault: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_events", "id", first.ID.String()))

	second, err := h.faults.OpenFault(ctx, nocmonport.OpenFaultInput{
		Kind:       nocmondom.FaultKindProbeCritical,
		Severity:   nocmondom.FaultSeverityHigh,
		SourceID:   &srcID, // same source!
		SourceKind: "wave121c-dup",
	})
	if err != nil {
		t.Fatalf("second OpenFault: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_events", "id", second.ID.String()))

	// Drive the duplicate flag via the domain method (this is what the
	// cron-side dedup wraps).
	if err := second.MarkDuplicate(first.ID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkDuplicate: %v", err)
	}
	if err := h.faultRepo.UpdateStatus(ctx, second); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	reloaded, _ := h.faults.GetFault(ctx, second.ID)
	if reloaded.Status != nocmondom.FaultStatusDuplicate {
		t.Errorf("second.Status = %q, want duplicate", reloaded.Status)
	}
	if reloaded.RootCause == "" {
		t.Errorf("MarkDuplicate should stamp root_cause with back-ref")
	}
}

func TestNocProbe_RecordSampleAndAntiFlap(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	custID := uuid.New()
	warn := 100.0
	crit := 200.0
	p, err := h.probes.CreateProbe(ctx, nocmonport.CreateProbeInput{
		CustomerID:        custID,
		Kind:              nocmondom.ProbeKindRTT,
		Target:            "8.8.8.8",
		IntervalSeconds:   60,
		ThresholdWarn:     &warn,
		ThresholdCritical: &crit,
	})
	if err != nil {
		t.Fatalf("CreateProbe: %v", err)
	}
	t.Cleanup(func() {
		// Samples cascade with the probe via ON DELETE CASCADE in
		// migration 0075; if not, this best-effort delete still fires.
		_, _ = pool.Exec(ctx, `DELETE FROM nocmon.service_health_samples WHERE probe_id = $1`, p.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM nocmon.service_probes WHERE id = $1`, p.ID)
	})

	// Insert one critical sample → streak = 1 (anti-flap should NOT
	// fire yet).
	now := time.Now().UTC()
	if _, err := h.probes.RecordSample(ctx, p.ID, 250.0, now); err != nil {
		t.Fatalf("RecordSample #1: %v", err)
	}
	got, err := h.sampleRepo.CountConsecutive(ctx, p.ID, nocmondom.SampleStatusCritical, 3)
	if err != nil {
		t.Fatalf("CountConsecutive: %v", err)
	}
	if got != 1 {
		t.Errorf("streak after 1: got %d, want 1", got)
	}

	// Insert second critical 30 seconds later → streak = 2 (would
	// trigger auto-fault in cron).
	if _, err := h.probes.RecordSample(ctx, p.ID, 260.0, now.Add(30*time.Second)); err != nil {
		t.Fatalf("RecordSample #2: %v", err)
	}
	got, _ = h.sampleRepo.CountConsecutive(ctx, p.ID, nocmondom.SampleStatusCritical, 3)
	if got < 2 {
		t.Errorf("streak after 2: got %d, want >=2", got)
	}

	// Verify the last_status denorm got bumped to critical.
	reloaded, _ := h.probes.GetProbe(ctx, p.ID)
	if reloaded.LastStatus != nocmondom.SampleStatusCritical {
		t.Errorf("last_status: got %q, want critical", reloaded.LastStatus)
	}
}

func TestNocFiber_AttenuationUpdatesStatus(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	// Seed a fiber link directly — fiber_links has no FK to network so
	// a raw INSERT is safe. Warn at 25dB, critical at 28dB (defaults).
	linkID := uuid.New()
	cust := uuid.New()
	w121cExec(t, pool,
		`INSERT INTO nocmon.fiber_links (id, customer_id, status, warn_threshold_db, critical_threshold_db)
		 VALUES ($1, $2, 'unknown', 25, 28)`, linkID, cust)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM nocmon.fiber_attenuation_history WHERE fiber_link_id=$1`, linkID)
		_, _ = pool.Exec(ctx, `DELETE FROM nocmon.fiber_links WHERE id = $1`, linkID)
	})

	updated, err := h.fiber.RecordAttenuation(ctx, linkID, 26.5, time.Now().UTC(), "wave121c")
	if err != nil {
		t.Fatalf("RecordAttenuation: %v", err)
	}
	// 26.5 > 25 (warn) but < 28 (crit) → warn.
	if updated.Status != nocmondom.FiberStatusWarn {
		t.Errorf("status after 26.5dB: %q, want warn", updated.Status)
	}
	if updated.LastMeasuredDB == nil || *updated.LastMeasuredDB != 26.5 {
		t.Errorf("LastMeasuredDB: %v, want 26.5", updated.LastMeasuredDB)
	}

	// Push past critical.
	updated, err = h.fiber.RecordAttenuation(ctx, linkID, 30.0, time.Now().UTC(), "wave121c")
	if err != nil {
		t.Fatalf("RecordAttenuation #2: %v", err)
	}
	if updated.Status != nocmondom.FiberStatusCritical {
		t.Errorf("status after 30dB: %q, want critical", updated.Status)
	}
}

func TestNocTopology_SnapshotPersist(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	// Snapshot insert directly — we don't have a TopologyBuilder
	// wired (the builder is the cross-context bridge into the network
	// service which is too heavy to wire in this test). The repo +
	// domain are the load-bearing path.
	scopeID := uuid.New()
	payload, _ := json.Marshal(map[string]any{
		"nodes": []map[string]any{{"id": "n1", "type": "olt"}},
		"edges": []map[string]any{},
	})
	snap, err := nocmondom.NewTopologySnapshot(nocmondom.TopologyScopeSubArea, &scopeID, payload, 1, 0)
	if err != nil {
		t.Fatalf("NewTopologySnapshot: %v", err)
	}
	if err := h.topoRepo.Create(ctx, snap); err != nil {
		t.Fatalf("topo Create: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.topology_snapshots", "id", snap.ID.String()))

	latest, err := h.topoRepo.FindLatest(ctx, nocmondom.TopologyScopeSubArea, &scopeID)
	if err != nil {
		t.Fatalf("FindLatest: %v", err)
	}
	if latest.ID != snap.ID {
		t.Errorf("FindLatest returned wrong row")
	}
	if latest.NodeCount != 1 {
		t.Errorf("NodeCount: %d, want 1", latest.NodeCount)
	}
}

func TestNocAlertWO_ConvertFault(t *testing.T) {
	h := newNocHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	srcID := uuid.New()
	f, err := h.faults.OpenFault(ctx, nocmonport.OpenFaultInput{
		Kind:       nocmondom.FaultKindManualOutage,
		Severity:   nocmondom.FaultSeverityCritical,
		SourceID:   &srcID,
		SourceKind: "wave121c-alertwo",
	})
	if err != nil {
		t.Fatalf("OpenFault: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_events", "id", f.ID.String()))
	t.Cleanup(w121cCleanup(pool, "nocmon.fault_impact_links", "fault_event_id", f.ID.String()))

	// Link one impact so the WO creator has a target.
	_, err = h.faults.LinkImpact(ctx, nocmonport.LinkImpactInput{
		FaultEventID: f.ID,
		CustomerID:   uuid.New(),
		ImpactKind:   nocmondom.ImpactKindFullOutage,
		ImpactStart:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("LinkImpact: %v", err)
	}

	by := uuid.New()
	updated, err := h.alertWO.ConvertFaultToWO(ctx, f.ID, by)
	if err != nil {
		t.Fatalf("ConvertFaultToWO: %v", err)
	}
	if updated.TicketWOID == nil {
		t.Errorf("ticket_wo_id not stamped")
	}
	if updated.Status != nocmondom.FaultStatusInvestigating {
		t.Errorf("status after convert: %q, want investigating", updated.Status)
	}
}
