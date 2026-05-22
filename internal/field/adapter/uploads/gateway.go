// Package uploads adapts the uploads usecase to the field
// port.UploadsGateway. In-process today; an HTTP adapter can replace
// it when the uploads service moves to its own deployment.
package uploads

import (
	"context"

	"github.com/ion-core/backend/internal/field/port"
	uploadsport "github.com/ion-core/backend/internal/uploads/port"
)

// Service is the subset of uploads.usecase.Service we depend on.
type Service interface {
	FindByObjectURL(ctx context.Context, objectURL string) (*uploadsport.PhotoUpload, error)
}

type Gateway struct {
	svc Service
}

func New(svc Service) *Gateway {
	return &Gateway{svc: svc}
}

var _ port.UploadsGateway = (*Gateway)(nil)

func (g *Gateway) GPSFor(ctx context.Context, objectURL string) (*float64, *float64, error) {
	u, err := g.svc.FindByObjectURL(ctx, objectURL)
	if err != nil {
		return nil, nil, err
	}
	return u.GPSLat, u.GPSLng, nil
}
