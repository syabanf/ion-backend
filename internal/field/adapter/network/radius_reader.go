// Package network adapts the network usecase to the field
// port.RadiusReader. Round-3 in-process; round-4 swaps to HTTP.
package network

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/port"
	networkdomain "github.com/ion-core/backend/internal/network/domain"
)

// RadiusClient is the subset of network.RadiusClient we need.
type RadiusClient interface {
	Find(ctx context.Context, customerID uuid.UUID) (*networkdomain.RadiusAccount, error)
}

type RadiusReader struct {
	client RadiusClient
}

func NewRadiusReader(c RadiusClient) *RadiusReader {
	return &RadiusReader{client: c}
}

var _ port.RadiusReader = (*RadiusReader)(nil)

// RadiusAccountFor maps the network domain object to the field's
// purposefully narrow projection. The password is deliberately left
// out — round-3 doesn't ship a decrypt path and the on-site flow
// uses the device's pre-configured PPPoE shortcut anyway.
func (r *RadiusReader) RadiusAccountFor(ctx context.Context, customerID uuid.UUID) (*port.RadiusAccountView, error) {
	acct, err := r.client.Find(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		return nil, nil
	}
	return &port.RadiusAccountView{
		Username:           acct.Username,
		BandwidthProfileID: acct.BandwidthProfileID,
		VLANID:             acct.VLANID,
		IPAddress:          acct.IPAddress,
		Status:             string(acct.Status),
	}, nil
}
