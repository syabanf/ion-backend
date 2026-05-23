package gateway

import (
	"fmt"
	"os"

	"github.com/ion-core/backend/internal/payment/port"
)

// Registry holds the live gateway clients keyed by code. The webhook
// ingest and refund flows look up the right client per inbound event /
// per refund row's parent intent.
type Registry struct {
	clients map[string]port.GatewayClient
}

// NewStubRegistry wires the default stub clients. Production code
// switches each entry to the real REST adapter when the matching
// env flag is set (XENDIT_ENABLED=true, BCA_H2H_ENABLED=true, …).
//
// The secrets parameter carries per-gateway signing secrets:
//
//	"xendit"   → Xendit webhook secret
//	"midtrans" → Midtrans signature key
//
// A missing entry maps to empty secret (stubs accept all signatures).
func NewStubRegistry(secrets map[string]string) *Registry {
	r := &Registry{clients: map[string]port.GatewayClient{}}

	r.clients["xendit"] = NewXenditStub(secrets["xendit"])
	r.clients["bca_h2h"] = NewBCAH2HStub()
	r.clients["midtrans"] = NewMidtransStub(secrets["midtrans"])
	r.clients["stripe"] = NewStripeStub()

	return r
}

// Resolve returns the client for a gateway code, or an error when the
// code isn't registered.
func (r *Registry) Resolve(code string) (port.GatewayClient, error) {
	c, ok := r.clients[code]
	if !ok {
		return nil, fmt.Errorf("gateway %q is not registered", code)
	}
	return c, nil
}

// Codes returns the list of registered gateway codes — useful when the
// caller wants to broadcast a status check.
func (r *Registry) Codes() []string {
	out := make([]string, 0, len(r.clients))
	for c := range r.clients {
		out = append(out, c)
	}
	return out
}

// SecretsFromEnv collects gateway signing secrets from environment
// variables using the standard `PAYMENT_<GATEWAY>_SECRET` convention.
// Missing vars default to empty (stub-mode accepts any signature).
func SecretsFromEnv() map[string]string {
	return map[string]string{
		"xendit":   os.Getenv("PAYMENT_XENDIT_SECRET"),
		"midtrans": os.Getenv("PAYMENT_MIDTRANS_SECRET"),
	}
}
