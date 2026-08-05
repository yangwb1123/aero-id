package event

import "context"

// EventPublisher is the producer-side port for canonical events (P6-03). The
// replication worker publishes approved projections through it; adapters exist
// for NATS JetStream and signed HTTP ingest so regional cells can exchange
// projections without coupling domain code to a broker.
type EventPublisher interface {
	Publish(ctx context.Context, envelope *Envelope) error
	// Target identifies this publisher in the replication cursor (e.g.
	// "nats:aero.events.canonical" or "http:https://cell-b.internal/v1/events").
	Target() string
}
