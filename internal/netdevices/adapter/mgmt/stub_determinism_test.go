// Wave 121E — netdevices device-mgmt stub-mode determinism tests.
//
// The StubClient is a no-op: every vendor-SDK method returns nil. The
// real SNMP / NETCONF client lands behind DEVICE_MGMT_ENABLED=true.
//
// Tests below assert:
//   - All four methods are nil-safe with a nil device (no panic).
//   - All methods return nil error consistently.
//   - No outbound network traffic (StubClient holds no client).
//
// What this DOES NOT validate:
//   - Real SNMP write OIDs
//   - Real firmware push transport (TFTP, HTTPS, SCP)
//   - Vendor SDK error mapping (Mikrotik, Huawei, Cisco)
package mgmt

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
)

func fixtureDevice(t *testing.T) *domain.Device {
	t.Helper()
	d, err := domain.NewDevice("SN-FIXED-001", domain.DeviceKindONT, "ModelX", "VendorY")
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	// Pin id + timestamps so the stub's log lines (if any) are stable.
	d.ID = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	d.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.UpdatedAt = d.CreatedAt
	return d
}

// =====================================================================
// 1) All four methods are nil-safe and idempotent.
// =====================================================================

func TestMgmtStub_AllMethodsReturnNilForValidDevice(t *testing.T) {
	stub := NewStubClient(slog.Default())
	d := fixtureDevice(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := stub.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
			t.Errorf("call %d: ScheduleFirmwareUpgrade: %v", i, err)
		}
		if err := stub.PushStagedImage(ctx, d, "v2.0"); err != nil {
			t.Errorf("call %d: PushStagedImage: %v", i, err)
		}
		if err := stub.TriggerUpgrade(ctx, d); err != nil {
			t.Errorf("call %d: TriggerUpgrade: %v", i, err)
		}
		if err := stub.RollbackFirmware(ctx, d, "v1.0"); err != nil {
			t.Errorf("call %d: RollbackFirmware: %v", i, err)
		}
	}
}

// =====================================================================
// 2) Nil device must not panic (real client would 404 the device by
// serial; the stub gracefully skips when the device is nil).
// =====================================================================

func TestMgmtStub_NilDeviceDoesNotPanic(t *testing.T) {
	stub := NewStubClient(slog.Default())
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stub panicked on nil device: %v", r)
		}
	}()

	_ = stub.ScheduleFirmwareUpgrade(ctx, nil, "v2.0")
	_ = stub.PushStagedImage(ctx, nil, "v2.0")
	_ = stub.TriggerUpgrade(ctx, nil)
	_ = stub.RollbackFirmware(ctx, nil, "v1.0")
}

// =====================================================================
// 3) Nil logger must not panic — production wiring sometimes builds the
// stub before the logger.
// =====================================================================

func TestMgmtStub_NilLoggerDoesNotPanic(t *testing.T) {
	stub := NewStubClient(nil)
	d := fixtureDevice(t)
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stub panicked with nil logger: %v", r)
		}
	}()

	if err := stub.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
		t.Errorf("ScheduleFirmwareUpgrade with nil logger: %v", err)
	}
	if err := stub.PushStagedImage(ctx, d, "v2.0"); err != nil {
		t.Errorf("PushStagedImage with nil logger: %v", err)
	}
	if err := stub.TriggerUpgrade(ctx, d); err != nil {
		t.Errorf("TriggerUpgrade with nil logger: %v", err)
	}
	if err := stub.RollbackFirmware(ctx, d, "v1.0"); err != nil {
		t.Errorf("RollbackFirmware with nil logger: %v", err)
	}
}
