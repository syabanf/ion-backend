// Package http exposes the upload service. Two endpoints in round-3:
//
//	POST /uploads/photos        — accepts raw bytes, returns metadata + object_url
//	GET  /uploads/{key}         — serves the bytes back (auth required)
//
// The handler is mounted by whichever service binary wants to surface
// uploads. Round-3 wires it under field-svc since checklist photos
// are the only consumer; future contexts (warehouse intake photos)
// can mount the same handler against the same shared.photo_uploads
// table.
package http

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ion-core/backend/internal/uploads/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/uploads"
)

type Handler struct {
	uc       port.UseCase
	store    uploads.Storer
	verifier *auth.Verifier
}

func NewHandler(uc port.UseCase, store uploads.Storer, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, store: store, verifier: verifier}
}

// Mount registers the uploads routes on the host's router. Paths here
// land under the host service's chi.Router; the api-gateway maps the
// public `/api/uploads/*` prefix to this same upstream and strips
// `/api/uploads` so the routes match what we register.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.With(httpserver.RequirePermission("shared.upload.write")).
			Post("/photos", h.uploadPhoto)
		// Serve is authed but ungated — any logged-in user can read an
		// upload if they know the key. Round-4 will tighten with signed URLs.
		r.Get("/photos/*", h.serve)
	})
}

// DTOs (uploadResponse) live in dto.go.

func (h *Handler) uploadPhoto(w http.ResponseWriter, r *http.Request) {
	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		httpserver.WriteError(w, errors.Validation("upload.bad_content_type",
			"Content-Type must be an image/* MIME type"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	defer r.Body.Close()

	in := port.IngestInput{
		ContentType: contentType,
		Body:        r.Body,
		UploadedBy:  c.UserID,
	}
	// Optional client-supplied GPS via headers (avoids multipart parsing in round-3).
	if v := r.Header.Get("X-Upload-GPS-Lat"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			in.ClientGPSLat = &f
		}
	}
	if v := r.Header.Get("X-Upload-GPS-Lng"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			in.ClientGPSLng = &f
		}
	}
	if v := r.Header.Get("X-Upload-GPS-Accuracy"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			in.ClientGPSAccuracyM = &f
		}
	}

	u, err := h.uc.Ingest(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toUploadResponseDTO(u))
}

// serve streams the file bytes back. The chi splat captures everything
// after /uploads/photos/ as the storage key.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "*")
	if key == "" {
		httpserver.WriteError(w, errors.Validation("upload.key_required", "missing storage key"))
		return
	}
	// Defensive: refuse traversal attempts. The store also joins under
	// its root so .. would never escape, but we reject loudly here.
	if strings.Contains(key, "..") {
		httpserver.WriteError(w, errors.Validation("upload.key_invalid", "key contains '..'"))
		return
	}
	rc, err := h.store.ReadCloser(r.Context(), "photos/"+key)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	defer rc.Close()
	// We don't know the content type from the key — round-4 can store
	// the MIME alongside. For now we let the browser sniff.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}
