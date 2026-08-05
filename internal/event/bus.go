// Package event defines the transport-neutral canonical event contract and the
// event-bus port. Transport is an implementation detail behind the EventBus
// interface: HTTP CloudEvents ingest, NATS JetStream and Kafka-compatible
// streams may coexist through adapters; domain/service code never couples to a
// specific broker (ADR-009).
package event

import "context"

// EnvelopeHandler processes one canonical envelope. It must be idempotent:
// delivery is at-least-once and the consumer relies on durable receipts for
// exactly-once state transitions.
type EnvelopeHandler func(ctx context.Context, envelope *Envelope) error

// EventBus is the consumer-side port for canonical events. Subscribe starts a
// background consumer that decodes canonical envelopes and dispatches them to
// handler. The returned stop function drains and closes the subscription.
type EventBus interface {
	Subscribe(ctx context.Context, handler EnvelopeHandler) (stop func(), err error)
	Close() error
}
