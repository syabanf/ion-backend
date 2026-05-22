// Package eventbus is an in-process pub/sub used by bounded contexts to
// communicate without direct imports of each other.
//
// Today it's a synchronous, in-memory dispatcher. When ION Core splits to
// microservices, swap the Publisher implementation for a NATS/Kafka client —
// subscribers will be replaced by message-broker consumers in each service.
//
// Events are typed by a stable string Name (e.g. "billing.invoice.paid").
// Names follow the convention "<context>.<aggregate>.<verb_past_tense>".
package eventbus

import (
	"context"
	"log/slog"
	"sync"
)

// Event is the envelope all subscribers receive.
type Event struct {
	Name    string         // e.g. "billing.invoice.paid"
	Payload map[string]any // structured event data
}

// Handler processes an event. Returning an error currently only logs;
// retry/DLQ semantics will be added when we move to a broker.
type Handler func(ctx context.Context, e Event) error

type Bus struct {
	mu   sync.RWMutex
	subs map[string][]Handler
	log  *slog.Logger
}

func New(log *slog.Logger) *Bus {
	return &Bus{
		subs: make(map[string][]Handler),
		log:  log,
	}
}

// Subscribe registers a handler for a specific event name. Subscriptions
// happen at startup (wiring time) — there is no Unsubscribe.
func (b *Bus) Subscribe(name string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[name] = append(b.subs[name], h)
}

// Publish dispatches an event to all subscribers synchronously.
// Subscriber errors are logged; they do not propagate to the publisher.
// This mirrors the "fire and forget" semantics of a real broker.
func (b *Bus) Publish(ctx context.Context, e Event) {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.subs[e.Name]...)
	b.mu.RUnlock()

	for _, h := range handlers {
		if err := h(ctx, e); err != nil {
			b.log.Error("eventbus handler failed", "event", e.Name, "err", err)
		}
	}
}
