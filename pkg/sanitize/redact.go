// Package sanitize is the standard place for PII / secret redaction
// helpers. Anything that goes out in an API response and shouldn't be
// the cleartext value lives here so the redaction rule is reviewed in
// one file rather than scattered across handlers.
package sanitize

// NIK masks an Indonesian KTP number (16 digits) to first-6 + ****** + last-4.
// Examples:
//
//	"3174012345678901" → "317401******8901"
//	"31740123"         → "31740123" (too short to mask sensibly)
//	""                 → "" (nothing to redact)
//
// We mask in-place, not at-rest — at-rest encryption (pgcrypto) is a
// round-2 item. The redaction here is the boundary control: anyone
// reading an API response sees only the head/tail digits.
func NIK(nik string) string {
	const minMaskable = 12
	if len(nik) < minMaskable {
		return nik
	}
	const head = 6
	const tail = 4
	if len(nik) <= head+tail {
		return nik
	}
	mid := len(nik) - head - tail
	masked := make([]byte, 0, len(nik))
	masked = append(masked, nik[:head]...)
	for i := 0; i < mid; i++ {
		masked = append(masked, '*')
	}
	masked = append(masked, nik[len(nik)-tail:]...)
	return string(masked)
}
