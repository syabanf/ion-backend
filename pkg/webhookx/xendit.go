package webhookx

// XenditProvider implements Provider for the Xendit V2 callback
// scheme.
//
// Xendit uses a static "X-Callback-Token" header that's the verbatim
// shared secret — there's no per-payload signature. This is
// historical and weaker than HMAC, but it's what the API ships, so
// we mirror it.
//
// When/if Xendit migrates to HMAC-SHA256 (some newer endpoints
// already do), bump this adapter to compute the real signature
// instead of returning the secret verbatim — the rest of the
// Verifier pipeline stays the same.
type XenditProvider struct{}

func (XenditProvider) Name() string           { return "xendit" }
func (XenditProvider) SignatureHeader() string { return "X-Callback-Token" }
func (XenditProvider) EventIDHeader() string   { return "Webhook-Id" }

// ComputeSignature for Xendit returns the secret itself; the
// Verifier's constant-time compare then matches it against the
// header.
func (XenditProvider) ComputeSignature(_ []byte, secret string) string {
	return secret
}

// XenditHMACProvider is the future-proof variant that does real
// HMAC-SHA256(body, secret) hex-encoded. Swap to this once Xendit
// rolls signed-payload mode for the endpoints we care about.
type XenditHMACProvider struct{}

func (XenditHMACProvider) Name() string           { return "xendit" }
func (XenditHMACProvider) SignatureHeader() string { return "X-Signature" }
func (XenditHMACProvider) EventIDHeader() string   { return "Webhook-Id" }

func (XenditHMACProvider) ComputeSignature(body []byte, secret string) string {
	return HMACHex(body, secret)
}
