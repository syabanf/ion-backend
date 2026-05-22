// KTP OCR stub handler.
//
// The Sales App (and the web onboarding wizard) call this with the raw
// KTP photo bytes; we return a parsed projection that pre-fills the
// lead-create form. Mode A = auto-fill from the response; Mode B is
// manual entry when this endpoint isn't reachable.
//
// Round-3: parsing is deterministic — we hash the bytes and derive
// plausible fields so the same image always returns the same data.
// That's enough to unblock the mobile + web flow end-to-end and
// regression-test the form wiring.
//
// Round-4: route through Google Vision OCR (or Tesseract if we keep
// it on-prem). The contract this endpoint exposes is stable across
// the swap.
package http

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTO (ktpOCRResponse) lives in dto.go.

// parseKTPImage reads the image body and hands it off to the configured
// KTPProvider (stub by default, tesseract when the binary is built and
// wired for it). The handler enforces the wire-level shape (image
// content type + minimum bytes); the provider does the actual parsing.
func (h *Handler) parseKTPImage(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		httpserver.WriteError(w, errors.Validation("ktp.bad_content_type",
			"Content-Type must be image/*"))
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal,
			"ktp.read", "read body", err))
		return
	}
	if len(body) < 1024 {
		// Anything plausibly small is probably an icon or a click-error.
		httpserver.WriteError(w, errors.Validation("ktp.too_small",
			"image is too small to be a KTP scan"))
		return
	}
	prov := h.ktpProvider
	if prov == nil {
		prov = defaultKTPProvider{}
	}
	resp, err := prov.Parse(body)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal,
			"ktp.parse", "ocr provider failed", err))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

// sha256Hex is exported (lower-case file-scoped) for use by providers
// living in the same package — the stub uses it to derive its
// deterministic projection.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// stubFromHash derives a deterministic projection from the upload's
// sha256. The values look plausible without being PII: NIK is the first
// 16 hex chars normalized to digits-only; the rest follow a stable rota
// of common Indonesian sample names + addresses.
//
// Real OCR replaces this whole function in round-4. The HTTP contract
// is unchanged.
func stubFromHash(hash string) ktpOCRResponse {
	digits := hashToDigits(hash, 16)
	idx := int(hash[0]) % len(stubNames)

	birthDay := 1 + int(hash[8])%28
	birthMonth := 1 + int(hash[9])%12
	birthYear := 1970 + int(hash[10])%30

	return ktpOCRResponse{
		NIK:         digits,
		FullName:    stubNames[idx],
		BirthPlace:  stubBirthPlaces[idx],
		BirthDate:   fmt.Sprintf("%04d-%02d-%02d", birthYear, birthMonth, birthDay),
		Gender:      stubGenders[idx],
		Address:     stubAddresses[idx],
		RTRW:        fmt.Sprintf("%03d/%03d", 1+int(hash[2])%12, 1+int(hash[3])%18),
		Kelurahan:   stubKelurahan[idx],
		Kecamatan:   stubKecamatan[idx],
		Religion:    stubReligion[idx],
		MaritalStat: stubMarital[idx],
		Occupation:  stubOccupation[idx],
		Citizenship: "WNI",
		ValidUntil:  "SEUMUR HIDUP",
		Confidence:  0.92,
		Stub:        true,
	}
}

func hashToDigits(hash string, n int) string {
	// Map the first n hex chars to 0-9 only.
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		c := hash[i]
		if c >= '0' && c <= '9' {
			out[i] = c
		} else if c >= 'a' && c <= 'f' {
			out[i] = byte('0' + (c-'a')%10)
		} else {
			out[i] = '0'
		}
	}
	// First digit can't be zero in a real NIK.
	if out[0] == '0' {
		out[0] = '3'
	}
	return string(out)
}

// Silence unused-import on time when this file is built without the
// real OCR integration.
var _ = time.Now

var stubNames = []string{
	"Budi Santoso", "Siti Aminah", "Rahmat Hidayat", "Dewi Kusuma",
	"Agus Pratama", "Wati Lestari", "Eko Saputra", "Linda Permata",
}
var stubBirthPlaces = []string{
	"Jakarta", "Bandung", "Surabaya", "Yogyakarta",
	"Medan", "Semarang", "Makassar", "Denpasar",
}
var stubGenders = []string{"L", "P", "L", "P", "L", "P", "L", "P"}
var stubAddresses = []string{
	"Jl. Sudirman No. 12", "Jl. Merdeka No. 5", "Jl. Pahlawan No. 88",
	"Jl. Diponegoro No. 23", "Jl. Gatot Subroto No. 9",
	"Jl. Veteran No. 14", "Jl. Ahmad Yani No. 7", "Jl. Imam Bonjol No. 4",
}
var stubKelurahan = []string{
	"Menteng", "Cihampelas", "Genteng", "Gondokusuman",
	"Petisah Tengah", "Sekayu", "Mariso", "Kuta",
}
var stubKecamatan = []string{
	"Menteng", "Coblong", "Genteng", "Gondokusuman",
	"Medan Petisah", "Semarang Tengah", "Mariso", "Kuta",
}
var stubReligion = []string{
	"Islam", "Islam", "Islam", "Kristen",
	"Islam", "Buddha", "Islam", "Hindu",
}
var stubMarital = []string{
	"KAWIN", "BELUM KAWIN", "KAWIN", "BELUM KAWIN",
	"KAWIN", "KAWIN", "BELUM KAWIN", "KAWIN",
}
var stubOccupation = []string{
	"KARYAWAN SWASTA", "WIRASWASTA", "PEGAWAI NEGERI",
	"GURU", "DOSEN", "PEDAGANG", "TEKNISI", "IBU RUMAH TANGGA",
}
