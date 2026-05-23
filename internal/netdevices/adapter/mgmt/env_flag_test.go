// Wave 121E — netdevices device-mgmt env-flag toggle tests.
//
// FINDING: DEVICE_MGMT_ENABLED=true in cmd/netdevices-svc/main.go logs
// a warning and falls back to StubClient. Same shape as the HRIS
// finding — flipping the flag at runtime is currently a no-op.
//
// When the real SNMP / NETCONF client lands, TestRealMode_* becomes
// the landing pad.
package mgmt

import (
	"context"
	"log/slog"
	"testing"
)

// =====================================================================
// 1) StubClient is env-independent.
// =====================================================================

func TestMgmt_StubClient_EnvIndependent(t *testing.T) {
	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	a := NewStubClient(slog.Default())

	t.Setenv("DEVICE_MGMT_ENABLED", "false")
	b := NewStubClient(slog.Default())

	d := fixtureDevice(t)
	ctx := context.Background()

	if err := a.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
		t.Errorf("a: %v", err)
	}
	if err := b.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
		t.Errorf("b: %v", err)
	}
}

// =====================================================================
// 2) Real-mode toggle — currently a NO-OP.
// =====================================================================

func TestMgmt_RealMode_FlagIsCurrentlyNoOp(t *testing.T) {
	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Skip("Wave 121E FINDING: DEVICE_MGMT_ENABLED=true currently falls back to stub with only a Warn log in cmd/netdevices-svc/main.go. Tracked in production wiring readiness doc.")
}
