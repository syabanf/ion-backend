package domain

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// KMLPlacemark is one Placemark extracted from a KML / KMZ file.
//
// We deliberately don't support the full KML schema — we want exactly what
// matters for ION coverage:
//
//   - Name (renders as ODP candidate name during import preview)
//   - Description (optional notes)
//   - A single Polygon → GeoJSONPolygon
//
// MultiGeometry, Points, LineStrings, and styles are ignored on this pass.
// If a placemark has no polygon we skip it.
type KMLPlacemark struct {
	Name        string
	Description string
	Polygon     GeoJSONPolygon
}

// ParseKMZ accepts a KMZ (zipped KML) byte buffer and returns all
// placemarks that have a usable polygon. KMZ is just a ZIP whose entry
// `doc.kml` is the KML payload (sometimes the entry is named differently;
// we pick the first .kml found).
func ParseKMZ(body []byte) ([]KMLPlacemark, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("invalid kmz (not a zip): %w", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".kml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		raw, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		return ParseKML(raw)
	}
	return nil, fmt.Errorf("no .kml entry found inside kmz")
}

// ParseKML accepts raw KML XML and returns all polygon-bearing placemarks.
//
// We use encoding/xml with a permissive shape — KML uses a default
// namespace (xmlns="http://www.opengis.net/kml/2.2"), but local-name
// matching in encoding/xml ignores it as long as the struct tags use the
// local element name.
func ParseKML(body []byte) ([]KMLPlacemark, error) {
	var doc kmlDocument
	dec := xml.NewDecoder(bytes.NewReader(body))
	// KML files sometimes ship with stray BOM / mixed encodings; be lenient.
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid kml: %w", err)
	}

	out := []KMLPlacemark{}
	for _, pm := range doc.placemarks() {
		poly, ok := pm.firstPolygon()
		if !ok {
			continue // skip points / linestrings / placemarks with no polygon
		}
		out = append(out, KMLPlacemark{
			Name:        strings.TrimSpace(pm.Name),
			Description: strings.TrimSpace(pm.Description),
			Polygon:     poly,
		})
	}
	return out, nil
}

// --- internal XML shape ---

type kmlDocument struct {
	XMLName    xml.Name      `xml:"kml"`
	Document   *kmlContainer `xml:"Document"`
	Folder     *kmlContainer `xml:"Folder"`
	Placemarks []kmlPlacemark `xml:"Placemark"` // some KML files put placemarks at root
}

type kmlContainer struct {
	Placemarks []kmlPlacemark `xml:"Placemark"`
	Folders    []kmlContainer `xml:"Folder"`
	Documents  []kmlContainer `xml:"Document"`
}

type kmlPlacemark struct {
	Name          string         `xml:"name"`
	Description   string         `xml:"description"`
	Polygon       *kmlPolygon    `xml:"Polygon"`
	MultiGeometry *kmlMulti      `xml:"MultiGeometry"`
}

type kmlMulti struct {
	Polygons []kmlPolygon `xml:"Polygon"`
}

type kmlPolygon struct {
	Outer kmlBoundary   `xml:"outerBoundaryIs"`
	Inner []kmlBoundary `xml:"innerBoundaryIs"`
}

type kmlBoundary struct {
	LinearRing kmlLinearRing `xml:"LinearRing"`
}

type kmlLinearRing struct {
	Coordinates string `xml:"coordinates"`
}

func (d *kmlDocument) placemarks() []kmlPlacemark {
	out := append([]kmlPlacemark{}, d.Placemarks...)
	collect := func(c *kmlContainer) {
		if c == nil {
			return
		}
		out = append(out, c.Placemarks...)
		var walk func(k []kmlContainer)
		walk = func(k []kmlContainer) {
			for _, x := range k {
				out = append(out, x.Placemarks...)
				walk(x.Folders)
				walk(x.Documents)
			}
		}
		walk(c.Folders)
		walk(c.Documents)
	}
	collect(d.Document)
	collect(d.Folder)
	return out
}

func (p kmlPlacemark) firstPolygon() (GeoJSONPolygon, bool) {
	if p.Polygon != nil {
		return p.Polygon.toGeoJSON()
	}
	if p.MultiGeometry != nil && len(p.MultiGeometry.Polygons) > 0 {
		return p.MultiGeometry.Polygons[0].toGeoJSON()
	}
	return GeoJSONPolygon{}, false
}

// toGeoJSON converts a KML Polygon to RFC 7946 GeoJSON.
//
// KML `<coordinates>` format is whitespace-separated coordinate tuples,
// each tuple being `lng,lat[,alt]`. GeoJSON wants `[lng, lat]`. Altitude
// is dropped — coverage polygons are 2D.
func (p kmlPolygon) toGeoJSON() (GeoJSONPolygon, bool) {
	outer := parseKMLCoords(p.Outer.LinearRing.Coordinates)
	if len(outer) < 4 {
		return GeoJSONPolygon{}, false
	}
	rings := [][][]float64{outer}
	for _, in := range p.Inner {
		hole := parseKMLCoords(in.LinearRing.Coordinates)
		if len(hole) >= 4 {
			rings = append(rings, hole)
		}
	}
	out := GeoJSONPolygon{Type: "Polygon", Coordinates: rings}
	if !out.IsValid() {
		return GeoJSONPolygon{}, false
	}
	return out, true
}

func parseKMLCoords(s string) [][]float64 {
	out := [][]float64{}
	for _, tuple := range strings.Fields(s) {
		parts := strings.Split(strings.TrimSpace(tuple), ",")
		if len(parts) < 2 {
			continue
		}
		lng, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lat, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, []float64{lng, lat})
	}
	// Auto-close the ring if KML omitted the trailing repeat (some exporters do).
	if len(out) >= 3 {
		first, last := out[0], out[len(out)-1]
		if first[0] != last[0] || first[1] != last[1] {
			out = append(out, []float64{first[0], first[1]})
		}
	}
	return out
}
