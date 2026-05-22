// KTP OCR provider abstraction.
//
// The HTTP handler calls a KTPProvider; what that provider actually
// does is pluggable. Round-3 ships two providers:
//
//   - stubProvider   — deterministic fake driven by sha256(image bytes).
//                      Default; lets the wizard exercise the Mode A
//                      auto-fill path without any external dependency.
//                      Wired in every binary out of the box.
//
//   - tesseractProvider — shells out to the `tesseract` CLI when it's
//                         on PATH. Selected via env var
//                         `KTP_OCR_PROVIDER=tesseract`. Lives in
//                         ktp_ocr_provider_tesseract.go behind the
//                         `tesseract` build tag so binaries built
//                         without it stay slim.
//
// Round-4 will add a Google Vision provider; the contract here is the
// same as the stub — bytes in, parsed fields out.
package http

// KTPProvider parses an Indonesian KTP image into a structured
// projection. Returns the response struct directly so the HTTP handler
// can serialise it without an intermediate mapping.
type KTPProvider interface {
	Name() string
	Parse(imageBytes []byte) (ktpOCRResponse, error)
}

// defaultKTPProvider is the deterministic stub used when no real
// provider is wired. Always available.
type defaultKTPProvider struct{}

func (defaultKTPProvider) Name() string { return "stub" }

func (defaultKTPProvider) Parse(b []byte) (ktpOCRResponse, error) {
	hash := sha256Hex(b)
	return stubFromHash(hash), nil
}
