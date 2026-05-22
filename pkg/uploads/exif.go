package uploads

import (
	"errors"
	"io"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// ExifMeta is the subset of EXIF we care about for checklist gating.
type ExifMeta struct {
	GPSLat  *float64
	GPSLng  *float64
	TakenAt *time.Time
}

// ParseEXIF extracts GPS + taken_at from a JPEG. Returns zero-valued
// fields when the file has no EXIF (e.g. PNGs, web-stripped photos).
// Errors are non-fatal in the caller: a checklist response without GPS
// is rejected by the service later, not here.
func ParseEXIF(r io.Reader) (ExifMeta, error) {
	x, err := exif.Decode(r)
	if err != nil {
		if errors.Is(err, io.EOF) || exif.IsCriticalError(err) {
			return ExifMeta{}, nil // treat as "no EXIF" — let the gate decide
		}
		return ExifMeta{}, err
	}
	var meta ExifMeta
	if lat, lng, e := x.LatLong(); e == nil {
		meta.GPSLat = &lat
		meta.GPSLng = &lng
	}
	if t, e := x.DateTime(); e == nil {
		meta.TakenAt = &t
	}
	return meta, nil
}
