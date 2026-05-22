// Package port defines the contract for the upload service. We keep
// this in its own internal package so any context that needs to ingest
// files can call the same usecase; round-3 only wires it under field-svc.
package port

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// PhotoUpload is the metadata row persisted in shared.photo_uploads.
type PhotoUpload struct {
	ID           uuid.UUID
	ObjectURL    string
	ContentType  string
	Bytes        int64
	GPSLat       *float64
	GPSLng       *float64
	GPSAccuracyM *float64
	TakenAt      *time.Time
	SHA256       string
	UploadedBy   *uuid.UUID
	UploadedAt   time.Time
}

type UseCase interface {
	// Ingest streams a request body to local storage, parses EXIF, and
	// writes a row in shared.photo_uploads. Returns the persisted row.
	Ingest(ctx context.Context, in IngestInput) (*PhotoUpload, error)

	// FindByObjectURL looks up metadata so a consumer can verify GPS
	// before accepting a file_url on a gated checklist item.
	FindByObjectURL(ctx context.Context, objectURL string) (*PhotoUpload, error)
}

type IngestInput struct {
	ContentType string
	Body        io.Reader
	UploadedBy  uuid.UUID
	// Optional GPS override from the client when EXIF is missing.
	// We trust client-supplied GPS only when nothing better is available.
	ClientGPSLat       *float64
	ClientGPSLng       *float64
	ClientGPSAccuracyM *float64
}

// Repository persists the metadata row.
type Repository interface {
	Create(ctx context.Context, u *PhotoUpload) error
	FindByObjectURL(ctx context.Context, objectURL string) (*PhotoUpload, error)
}
