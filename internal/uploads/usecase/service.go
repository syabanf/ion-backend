// Package usecase implements the upload pipeline: stream bytes to
// storage, hash them on the way, parse EXIF, persist metadata.
package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/uploads/port"
	"github.com/ion-core/backend/pkg/uploads"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Service struct {
	store uploads.Storer
	repo  port.Repository
}

func NewService(store uploads.Storer, repo port.Repository) *Service {
	return &Service{store: store, repo: repo}
}

var _ port.UseCase = (*Service)(nil)

// Ingest reads the entire body into memory (round-3 only supports
// small photos), tees through sha256, writes to storage, then parses
// EXIF from the buffered bytes. We buffer because EXIF parsing needs
// random access to a seekable source and the storage write needs the
// raw stream; cleaner to read once into RAM. ≤ 10 MB ceiling is enough
// for installation photos.
func (s *Service) Ingest(ctx context.Context, in port.IngestInput) (*port.PhotoUpload, error) {
	const maxBytes = 10 * 1024 * 1024 // 10 MB
	limited := io.LimitReader(in.Body, maxBytes+1)

	var buf bytes.Buffer
	hasher := sha256.New()
	tee := io.TeeReader(limited, hasher)
	n, err := io.Copy(&buf, tee)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "upload.read", "read upload body", err)
	}
	if n > maxBytes {
		return nil, derrors.Validation("upload.too_large",
			"upload exceeds the 10 MB round-3 limit")
	}
	if n == 0 {
		return nil, derrors.Validation("upload.empty", "upload body is empty")
	}

	key := uploads.NewKey(extensionFor(in.ContentType))
	objURL, written, err := s.store.Put(ctx, key, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "upload.put", "write upload to store", err)
	}

	meta, _ := uploads.ParseEXIF(bytes.NewReader(buf.Bytes()))
	// Client GPS only wins when EXIF didn't supply one.
	if meta.GPSLat == nil && in.ClientGPSLat != nil {
		meta.GPSLat = in.ClientGPSLat
	}
	if meta.GPSLng == nil && in.ClientGPSLng != nil {
		meta.GPSLng = in.ClientGPSLng
	}

	uploadedBy := in.UploadedBy
	rec := &port.PhotoUpload{
		ID:           uuid.New(),
		ObjectURL:    objURL,
		ContentType:  in.ContentType,
		Bytes:        written,
		GPSLat:       meta.GPSLat,
		GPSLng:       meta.GPSLng,
		GPSAccuracyM: in.ClientGPSAccuracyM,
		TakenAt:      meta.TakenAt,
		SHA256:       hex.EncodeToString(hasher.Sum(nil)),
		UploadedBy:   &uploadedBy,
		UploadedAt:   time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) FindByObjectURL(ctx context.Context, objectURL string) (*port.PhotoUpload, error) {
	return s.repo.FindByObjectURL(ctx, objectURL)
}

// extensionFor maps a MIME type to a sensible file extension. Falls
// back to "" so the storage key is still unique.
func extensionFor(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	switch ct {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/heic":
		return ".heic"
	case "image/webp":
		return ".webp"
	}
	// Strip leading "image/" for an ad-hoc extension.
	if rest := strings.TrimPrefix(ct, "image/"); rest != ct && rest != "" {
		return "." + filepath.Base(rest)
	}
	return ""
}
