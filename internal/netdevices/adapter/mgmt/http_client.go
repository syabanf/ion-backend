// Wave 128A — Device-mgmt real HTTP adapter (closes Wave 121E §6.2).
//
// HTTPClient is the production-shaped DeviceMgmtClient: POSTs vendor
// commands to a remote device-management gateway over HTTPS using a
// bearer token. Same hexagonal port as StubClient, so the swap in
// cmd/netdevices-svc/main.go is a single conditional.
//
// Swap matrix:
//
//	DEVICE_MGMT_ENABLED unset / empty / "false" → StubClient
//	DEVICE_MGMT_ENABLED=true + DEVICE_MGMT_BASE_URL + DEVICE_MGMT_API_KEY → HTTPClient
//	DEVICE_MGMT_ENABLED=true + any required var missing → *errors.Error (boot fail)
//
// The real client doesn't have to fully work end-to-end against a real
// vendor SDK today — it has to make actual HTTP calls and surface
// transport / non-2xx / parse errors as typed *errors.Error so the
// firmware service + ops can react. The vendor-specific NETCONF /
// SNMP shim lives behind this HTTP gateway in production.
package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// HTTPConfig drives the HTTPClient.
type HTTPConfig struct {
	// BaseURL is the device-mgmt gateway root, no trailing slash.
	// Required when DEVICE_MGMT_ENABLED=true.
	BaseURL string

	// APIKey is the bearer token. Required. Never logged.
	APIKey string

	// Timeout caps each HTTP call. Defaults to 30s if zero.
	Timeout time.Duration

	// Logger is optional.
	Logger *slog.Logger
}

// EnvFlagSet reports whether DEVICE_MGMT_ENABLED is set to "true".
func EnvFlagSet() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DEVICE_MGMT_ENABLED")), "true")
}

// HTTPConfigFromEnv reads HTTPConfig from process env.
func HTTPConfigFromEnv() HTTPConfig {
	return HTTPConfig{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("DEVICE_MGMT_BASE_URL")), "/"),
		APIKey:  os.Getenv("DEVICE_MGMT_API_KEY"),
		Timeout: 30 * time.Second,
	}
}

// NewHTTPClient constructs the real HTTP adapter. Returns a typed
// *errors.Error (KindValidation, "netdevices.mgmt.misconfigured") when
// any required env var is missing.
func NewHTTPClient(cfg HTTPConfig) (*HTTPClient, error) {
	var missing []string
	if cfg.BaseURL == "" {
		missing = append(missing, "DEVICE_MGMT_BASE_URL")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		missing = append(missing, "DEVICE_MGMT_API_KEY")
	}
	if len(missing) > 0 {
		return nil, derrors.New(
			derrors.KindValidation,
			"netdevices.mgmt.misconfigured",
			fmt.Sprintf("DEVICE_MGMT_ENABLED=true but required env vars are missing: %s",
				strings.Join(missing, ", ")),
		)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "netdevices.mgmt.http")
	logger.Info("device-mgmt client mode=real", "base_url", cfg.BaseURL)
	return &HTTPClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}, nil
}

// HTTPClient is the HTTP-backed DeviceMgmtClient.
type HTTPClient struct {
	cfg    HTTPConfig
	http   *http.Client
	logger *slog.Logger
}

var _ port.DeviceMgmtClient = (*HTTPClient)(nil)

// upgradeRequest is the JSON body posted for schedule / push / trigger /
// rollback operations. All four ops share the shape so the upstream
// gateway can route by `action` without ambiguity.
type upgradeRequest struct {
	Action          string `json:"action"`
	DeviceID        string `json:"device_id"`
	SerialNo        string `json:"serial_no"`
	Kind            string `json:"kind,omitempty"`
	Model           string `json:"model,omitempty"`
	Manufacturer    string `json:"manufacturer,omitempty"`
	TargetVersion   string `json:"target_version,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
}

func (c *HTTPClient) ScheduleFirmwareUpgrade(ctx context.Context, device *domain.Device, targetVersion string) error {
	return c.post(ctx, "schedule_upgrade", "/firmware/schedule", deviceBody(device, upgradeRequest{
		Action:        "schedule_upgrade",
		TargetVersion: targetVersion,
	}))
}

func (c *HTTPClient) PushStagedImage(ctx context.Context, device *domain.Device, version string) error {
	return c.post(ctx, "push_image", "/firmware/push", deviceBody(device, upgradeRequest{
		Action:        "push_image",
		TargetVersion: version,
	}))
}

func (c *HTTPClient) TriggerUpgrade(ctx context.Context, device *domain.Device) error {
	return c.post(ctx, "trigger_upgrade", "/firmware/trigger", deviceBody(device, upgradeRequest{
		Action: "trigger_upgrade",
	}))
}

func (c *HTTPClient) RollbackFirmware(ctx context.Context, device *domain.Device, previousVersion string) error {
	return c.post(ctx, "rollback", "/firmware/rollback", deviceBody(device, upgradeRequest{
		Action:          "rollback",
		PreviousVersion: previousVersion,
	}))
}

// deviceBody fills the device-identity portion of an upgradeRequest.
// Returns a request with action+target fields preserved.
func deviceBody(device *domain.Device, req upgradeRequest) upgradeRequest {
	if device != nil {
		req.DeviceID = device.ID.String()
		req.SerialNo = device.SerialNo
		req.Kind = string(device.Kind)
		req.Model = device.Model
		req.Manufacturer = device.Manufacturer
	}
	return req
}

// post is the shared HTTP helper. opCode prefixes the typed-error code
// so callers can distinguish (e.g.) "schedule_upgrade.transport" from
// "rollback.transport".
func (c *HTTPClient) post(ctx context.Context, opCode, path string, body upgradeRequest) error {
	if body.DeviceID == "" {
		// Mirrors the stub's nil-device tolerance: we surface a typed
		// validation error rather than a blank POST.
		return derrors.New(derrors.KindValidation,
			"netdevices.mgmt."+opCode+".missing_device",
			"device is required for "+opCode)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal,
			"netdevices.mgmt."+opCode+".marshal",
			"failed to marshal "+opCode+" request", err)
	}
	url := c.cfg.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return derrors.Wrap(derrors.KindInternal,
			"netdevices.mgmt."+opCode+".build_request",
			"failed to build "+opCode+" request", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ion-core/netdevices-svc")

	c.logger.Info("netdev.mgmt request",
		"action", body.Action, "device_id", body.DeviceID, "url", url)
	resp, err := c.http.Do(req)
	if err != nil {
		return derrors.Wrap(derrors.KindUnavailable,
			"netdevices.mgmt."+opCode+".transport",
			"device-mgmt transport error", err)
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Warn("netdev.mgmt non-2xx",
			"action", body.Action, "status", resp.StatusCode, "device_id", body.DeviceID)
		return derrors.New(derrors.KindUnavailable,
			"netdevices.mgmt."+opCode+".non_2xx",
			fmt.Sprintf("device-mgmt returned HTTP %d", resp.StatusCode))
	}
	return nil
}
