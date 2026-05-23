// Package mgmt holds the vendor device-management adapter for the
// netdevices bounded context.
//
// Wave 113 ships only a stub — every call logs (when a logger is wired)
// and returns nil. Real SNMP/NETCONF lands behind DEVICE_MGMT_ENABLED=
// true in a later wave; the port.DeviceMgmtClient interface stays the
// same so the swap is a one-line change in cmd/netdevices-svc.
package mgmt

import (
	"context"
	"log/slog"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
)

// StubClient implements port.DeviceMgmtClient with no side effects.
// Safe to use as the default in cmd/netdevices-svc.
type StubClient struct {
	log *slog.Logger
}

func NewStubClient(log *slog.Logger) *StubClient {
	return &StubClient{log: log}
}

var _ port.DeviceMgmtClient = (*StubClient)(nil)

func (s *StubClient) ScheduleFirmwareUpgrade(ctx context.Context, device *domain.Device, targetVersion string) error {
	if s.log != nil && device != nil {
		s.log.Debug("mgmt.stub schedule firmware upgrade",
			"device_id", device.ID, "serial", device.SerialNo, "target", targetVersion)
	}
	return nil
}

func (s *StubClient) PushStagedImage(ctx context.Context, device *domain.Device, version string) error {
	if s.log != nil && device != nil {
		s.log.Debug("mgmt.stub push staged image",
			"device_id", device.ID, "serial", device.SerialNo, "version", version)
	}
	return nil
}

func (s *StubClient) TriggerUpgrade(ctx context.Context, device *domain.Device) error {
	if s.log != nil && device != nil {
		s.log.Debug("mgmt.stub trigger upgrade",
			"device_id", device.ID, "serial", device.SerialNo)
	}
	return nil
}

func (s *StubClient) RollbackFirmware(ctx context.Context, device *domain.Device, previousVersion string) error {
	if s.log != nil && device != nil {
		s.log.Debug("mgmt.stub rollback firmware",
			"device_id", device.ID, "serial", device.SerialNo, "previous", previousVersion)
	}
	return nil
}
