// Package http — DTOs for the uploads adapter.
//
// All HTTP-layer request/response shapes for uploads live in this
// file (photo ingest responses). Conversion helpers `toXxxDTO` sit
// next to their target type so a change to the wire shape touches
// one file instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or port code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/uploads/photos
//     return?").
package http

import (
	"github.com/ion-core/backend/internal/uploads/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Photos
// =====================================================================

type uploadResponse struct {
	ID           string   `json:"id"`
	ObjectURL    string   `json:"object_url"`
	ContentType  string   `json:"content_type"`
	Bytes        int64    `json:"bytes"`
	GPSLat       *float64 `json:"gps_lat,omitempty"`
	GPSLng       *float64 `json:"gps_lng,omitempty"`
	GPSAccuracyM *float64 `json:"gps_accuracy_m,omitempty"`
	TakenAt      *string  `json:"taken_at,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	UploadedAt   string   `json:"uploaded_at"`
}

func toUploadResponseDTO(u *port.PhotoUpload) uploadResponse {
	resp := uploadResponse{
		ID:           u.ID.String(),
		ObjectURL:    u.ObjectURL,
		ContentType:  u.ContentType,
		Bytes:        u.Bytes,
		GPSLat:       u.GPSLat,
		GPSLng:       u.GPSLng,
		GPSAccuracyM: u.GPSAccuracyM,
		SHA256:       u.SHA256,
		UploadedAt:   httpserver.FormatRFC3339(u.UploadedAt),
	}
	if u.TakenAt != nil {
		s := httpserver.FormatRFC3339(*u.TakenAt)
		resp.TakenAt = &s
	}
	return resp
}
