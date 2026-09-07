package event

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSPublisher publishes canonical envelopes to a JetStream stream (P6-03).
// The consumer side is NATSBus; receipts make the pipeline at-least-once with
// exactly-once state transitions.
type NATSPublisher struct {
	conn    *nats.Conn
	subject string
}

// NewNATSPublisher dials NATS and prepares a publisher for subject.
func NewNATSPublisher(url, subject string) (*NATSPublisher, error) {
	if url == "" || subject == "" {
		return nil, fmt.Errorf("nats publisher url and subject are required")
	}
	conn, err := nats.Connect(url, nats.Name("aero-id-replication-publisher"))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &NATSPublisher{conn: conn, subject: subject}, nil
}

func (p *NATSPublisher) Target() string { return "nats:" + p.subject }

func (p *NATSPublisher) Publish(ctx context.Context, envelope *Envelope) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := marshalCanonicalEnvelope(envelope)
	if err != nil {
		return err
	}
	js, err := p.conn.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	if _, err := js.Publish(p.subject, body, nats.Context(ctx)); err != nil {
		return fmt.Errorf("nats publish projection: %w", err)
	}
	flushTimeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		flushTimeout = time.Until(deadline)
		if flushTimeout <= 0 {
			return ctx.Err()
		}
	}
	if err := p.conn.FlushTimeout(flushTimeout); err != nil {
		return fmt.Errorf("nats flush projection: %w", err)
	}
	return nil
}

// Close disconnects from NATS.
func (p *NATSPublisher) Close() error {
	if p.conn == nil {
		return nil
	}
	p.conn.Close()
	return nil
}
