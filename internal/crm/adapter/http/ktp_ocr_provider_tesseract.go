// Tesseract KTP OCR provider.
//
// Build with: `go build -tags=tesseract ./cmd/crm-svc`
// Then set:    `KTP_OCR_PROVIDER=tesseract` to enable.
//
// Why a build tag + env combo:
//
//   - Most dev machines don't have Tesseract installed; the default
//     binary should work out of the box. Build tag keeps the binary
//     small and slim by not shelling out unless asked.
//   - Even on a Tesseract-enabled build, operators may want to flip
//     between providers per environment without re-building. The env
//     var serves that knob.
//
// This provider shells out to the `tesseract` CLI. It's the lowest-
// dependency path — no cgo bindings, no model download in-process.
// Cost is process startup per request (~50ms); for the onboarding
// workload (a few KTPs per sales rep per day) that overhead is fine.
// If we ever bulk-process more than a few thousand per hour we'd
// switch to a long-running gosseract worker.
//
//go:build tesseract

package http

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type tesseractProvider struct {
	// binary is the path to the tesseract executable. Empty = "tesseract"
	// on $PATH. Useful in tests to point at a fake.
	binary string
	// timeout caps each OCR call. Tesseract should finish in well under
	// 10s on a single KTP image; we ceiling at 15s.
	timeout time.Duration
}

func NewTesseractProvider() KTPProvider {
	return &tesseractProvider{timeout: 15 * time.Second}
}

func (p *tesseractProvider) Name() string { return "tesseract" }

func (p *tesseractProvider) Parse(image []byte) (ktpOCRResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	bin := p.binary
	if bin == "" {
		bin = "tesseract"
	}
	// "-" tells tesseract to read from stdin and write to stdout.
	// "-l ind" selects the Indonesian language model.
	cmd := exec.CommandContext(ctx, bin, "stdin", "stdout", "-l", "ind")
	cmd.Stdin = bytes.NewReader(image)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return ktpOCRResponse{}, fmt.Errorf("tesseract: %w (stderr=%s)", err, errBuf.String())
	}
	return parseKTPText(out.String()), nil
}

// parseKTPText pulls KTP fields from Tesseract output. KTP layout is
// rigid enough that a few regexes get most fields. Anything we can't
// match is left empty; the Mode-B manual entry in the wizard backs
// every field that fails to parse.
func parseKTPText(text string) ktpOCRResponse {
	resp := ktpOCRResponse{Confidence: 0.7, Stub: false}

	// NIK: 16-digit number on its own line.
	if m := regexp.MustCompile(`\b(\d{16})\b`).FindStringSubmatch(text); m != nil {
		resp.NIK = m[1]
	}
	resp.FullName = extractAfter(text, []string{"Nama", "NAMA"})
	resp.Gender = upperFirstField(text, []string{"Jenis Kelamin", "JENIS KELAMIN"})
	resp.Address = extractAfter(text, []string{"Alamat", "ALAMAT"})
	resp.RTRW = extractAfter(text, []string{"RT/RW", "RT / RW"})
	resp.Kelurahan = extractAfter(text, []string{"Kel/Desa", "Kelurahan", "KEL/DESA"})
	resp.Kecamatan = extractAfter(text, []string{"Kecamatan", "KECAMATAN"})
	resp.Religion = extractAfter(text, []string{"Agama", "AGAMA"})
	resp.MaritalStat = extractAfter(text, []string{"Status Perkawinan", "STATUS PERKAWINAN"})
	resp.Occupation = extractAfter(text, []string{"Pekerjaan", "PEKERJAAN"})
	resp.Citizenship = extractAfter(text, []string{"Kewarganegaraan", "KEWARGANEGARAAN"})
	resp.ValidUntil = extractAfter(text, []string{"Berlaku Hingga", "BERLAKU HINGGA"})

	// Birth: "Tempat/Tgl Lahir : JAKARTA, 01-01-1990"
	if m := regexp.MustCompile(`(?i)Tempat[/\s]*Tgl\s*Lahir\s*[:\-]\s*([A-Z ]+),\s*(\d{2})-(\d{2})-(\d{4})`).FindStringSubmatch(text); m != nil {
		resp.BirthPlace = strings.TrimSpace(m[1])
		resp.BirthDate = fmt.Sprintf("%s-%s-%s", m[4], m[3], m[2])
	}
	return resp
}

// extractAfter pulls the value following any of the supplied label
// prefixes. Tesseract output uses colons to separate fields, but the
// layout is fuzzy enough that we accept extra whitespace and any
// trailing tokens up to the next newline.
func extractAfter(text string, labels []string) string {
	for _, l := range labels {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(l) + `\s*[:\-]\s*(.+)`)
		if m := re.FindStringSubmatch(text); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func upperFirstField(text string, labels []string) string {
	v := extractAfter(text, labels)
	if v == "" {
		return ""
	}
	parts := strings.Fields(v)
	if len(parts) == 0 {
		return ""
	}
	return strings.ToUpper(parts[0])
}
