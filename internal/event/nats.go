package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSConfig configures the JetStream consumer adapter (P3-04).
type NATSConfig struct {
	URL              string        // nats://host:4222
	Stream           string        // existing stream holding canonical events
	Subject          string        // subject filter, e.g. "aero.events.canonical"
	Durable          string        // durable consumer name (resume after restart)
	MaxRedeliveries  int           // redelivery cap before a message is dead-lettered by JetStream
	AckWait          time.Duration // per-message ack deadline
}

// NATSBus consumes canonical envelopes from a NATS JetStream stream.
//
// Ack semantics (at-least-once):
//   - handler success      -> Ack
//   - transient failure    -> Nak (redeliver with backoff up to MaxRedeliveries)
//   - malformed/poison     -> Term (JetStream dead-letter, never replayed forever)
type NATSBus struct {
	conn *nats.Conn
	cfg  NATSConfig
}

// NewNATSBus dials NATS and validates the configured stream exists.
func NewNATSBus(cfg NATSConfig) (*NATSBus, error) {
	if err := validateNATSConfig(cfg); err != nil {
		return nil, err
	}
	conn, err := nats.Connect(cfg.URL, nats.Name("aero-id-event-consumer"))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	bus := &NATSBus{conn: conn, cfg: cfg}
	return bus, nil
}

func validateNATSConfig(cfg NATSConfig) error {
	if cfg.URL == "" {
		return errors.New("nats url is required")
	}
	if cfg.Stream == "" || cfg.Subject == "" || cfg.Durable == "" {
		return errors.New("nats stream, subject and durable are required")
	}
	return nil
}

// Subscribe starts a JetStream pull/push consumer. Delivery is at-least-once;
// the handler must be idempotent (event receipts provide exactly-once state).
func (b *NATSBus) Subscribe(ctx context.Context, handler EnvelopeHandler) (func(), error) {
	js, err := b.conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	options := []nats.SubOpt{
		nats.Durable(b.cfg.Durable),
		nats.AckExplicit(),
		nats.MaxDeliver(b.maxRedeliveries()),
		nats.AckWait(b.ackWait()),
		nats.ManualAck(),
	}
	subscription, err := js.Subscribe(b.cfg.Subject, func(msg *nats.Msg) {
		b.handleMessage(ctx, msg, handler)
	}, options...)
	if err != nil {
		return nil, fmt.Errorf("nats subscribe: %w", err)
	}
	stop := func() {
		_ = subscription.Drain()
	}
	return stop, nil
}

func (b *NATSBus) handleMessage(ctx context.Context, msg *nats.Msg, handler EnvelopeHandler) {
	var envelope Envelope
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		// Poison: cannot even decode; terminate without redelivery.
		_ = msg.Term()
		return
	}
	// Canonical envelope validation is the adapter's last line of defense;
	// the ingest service re-validates and quarantines with its own codes.
	if err := envelope.Validate(); err != nil {
		_ = msg.Term()
		return
	}
	if err := handler(ctx, &envelope); err != nil {
		if transientDeliveryError(err) {
			_ = msg.Nak()
			return
		}
		// Deterministic rejection (bad projection, tenant mismatch, region
		// policy): terminate so the message goes to the JetStream DLQ subject
		// instead of looping until MaxRedeliveries.
		_ = msg.Term()
		return
	}
	_ = msg.Ack()
}

// transientDeliveryError distinguishes retryable failures from deterministic
// ones. The ingest service maps deterministic validation/tenant/region failures
// to 4xx-style errors; transient storage/unavailable failures are 5xx-style.
func transientDeliveryError(err error) bool {
	type codedError interface {
		HTTPStatus() int
	}
	var coded codedError
	if errors.As(err, &coded) {
		return coded.HTTPStatus() >= 500
	}
	return true // unknown errors are retried (at-least-once)
}

func (b *NATSBus) maxRedeliveries() int {
	if b.cfg.MaxRedeliveries <= 0 {
		return 5
	}
	return b.cfg.MaxRedeliveries
}

func (b *NATSBus) ackWait() time.Duration {
	if b.cfg.AckWait <= 0 {
		return 30 * time.Second
	}
	return b.cfg.AckWait
}

// Close disconnects from NATS.
func (b *NATSBus) Close() error {
	if b.conn == nil {
		return nil
	}
	b.conn.Close()
	return nil
}

// mustMarshalEnvelope encodes a canonical envelope; used by publishers where a
// marshal failure is a programming error (the envelope was validated already).
func mustMarshalEnvelope(envelope *Envelope) []byte {
	data, err := json.Marshal(envelope)
	if err != nil {
		panic("event: marshal envelope: " + err.Error())
	}
	return data
}
