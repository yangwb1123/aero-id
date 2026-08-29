package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// NATSConfig configures the JetStream consumer adapter (P3-04).
type NATSConfig struct {
	URL               string        // nats://host:4222
	Stream            string        // existing stream holding canonical events
	Subject           string        // subject filter, e.g. "aero.events.canonical"
	DeadLetterSubject string        // durable subject in the same stream for poison metadata
	Durable           string        // durable consumer name (resume after restart)
	MaxRedeliveries   int           // redelivery cap before a message is dead-lettered by JetStream
	AckWait           time.Duration // per-message ack deadline
}

// NATSBus consumes canonical envelopes from a NATS JetStream stream.
//
// Ack semantics (at-least-once):
//   - handler success      -> Ack
//   - transient failure    -> Nak until the cap; final failure is DLQ'd, then Term
//   - malformed/poison     -> durable redacted DLQ record, then Term
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
	if cfg.Stream == "" || cfg.Subject == "" || cfg.DeadLetterSubject == "" || cfg.Durable == "" {
		return errors.New("nats stream, subject, dead-letter subject and durable are required")
	}
	if cfg.Subject == cfg.DeadLetterSubject {
		return errors.New("nats dead-letter subject must differ from subject")
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
		nats.BindStream(b.cfg.Stream),
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
		b.terminatePoison(msg, "malformed_json", nil)
		return
	}
	// Canonical envelope validation is the adapter's last line of defense;
	// the ingest service re-validates and quarantines with its own codes.
	if err := envelope.Validate(); err != nil {
		b.terminatePoison(msg, "invalid_envelope", &envelope)
		return
	}
	if err := handler(ctx, &envelope); err != nil {
		if transientDeliveryError(err) {
			if b.isFinalDelivery(msg) {
				b.terminatePoison(msg, "retry_exhausted", &envelope)
				return
			}
			_ = msg.Nak()
			return
		}
		// Deterministic rejection (bad projection, tenant mismatch, region
		// policy): quarantine before terminating instead of looping forever.
		b.terminatePoison(msg, "handler_rejected", &envelope)
		return
	}
	_ = msg.Ack()
}

// transientDeliveryError distinguishes retryable failures from deterministic
// ones. The ingest service maps deterministic validation/tenant/region failures
// to 4xx-style errors; transient storage/unavailable failures are 5xx-style.
func transientDeliveryError(err error) bool {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		// Receipt lease contention is reported as a conflict, but the work is
		// still in progress and must be retried. Other conflict codes remain
		// terminal, including event.integrity_conflict.
		if appErr.Code == "event.in_flight" {
			return true
		}
		return appErr.HTTPStatus() >= 500
	}
	type codedError interface {
		HTTPStatus() int
	}
	var coded codedError
	if errors.As(err, &coded) {
		return coded.HTTPStatus() >= 500
	}
	return true // unknown errors are retried (at-least-once)
}

func (b *NATSBus) isFinalDelivery(msg *nats.Msg) bool {
	metadata, err := msg.Metadata()
	if err != nil {
		// A non-JetStream or malformed message must not be terminated merely
		// because its delivery metadata is unavailable. Nak leaves the cap to
		// JetStream's configured MaxDeliver value.
		return false
	}
	// maxRedeliveries returns the default positive value when configured with
	// zero or a negative value, so this conversion cannot be negative.
	return metadata.NumDelivered >= uint64(b.maxRedeliveries()) // #nosec G115 -- validated positive retry count
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
