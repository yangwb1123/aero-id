package event

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/aero/aero-id/internal/domain"
)

const deadLetterSchemaVersion = 1

// deadLetterRecord intentionally contains metadata only. The original event
// body can contain source PII, credentials, message content, or object data;
// operators can use the digest to correlate the source record without making
// the broker a second content store.
type deadLetterRecord struct {
	SchemaVersion   int       `json:"schema_version"`
	Reason          string    `json:"reason"`
	OriginalSubject string    `json:"original_subject"`
	EventID         string    `json:"event_id,omitempty"`
	PayloadSHA256   string    `json:"payload_sha256"`
	PayloadBytes    int       `json:"payload_bytes"`
	ReceivedAt      time.Time `json:"received_at"`
}

func (b *NATSBus) terminatePoison(msg *nats.Msg, reason string, envelope *Envelope) {
	if err := b.publishDeadLetter(msg, reason, envelope); err != nil {
		// A failed quarantine is not safe to acknowledge. Nak lets JetStream
		// retry until the configured delivery cap while the DLQ is repaired.
		_ = msg.Nak()
		return
	}
	_ = msg.Term()
}

func (b *NATSBus) publishDeadLetter(msg *nats.Msg, reason string, envelope *Envelope) error {
	if b == nil || b.conn == nil || msg == nil {
		return errors.New("nats dead-letter publisher is unavailable")
	}
	if b.cfg.DeadLetterSubject == "" {
		return errors.New("nats dead-letter subject is required")
	}
	digest := sha256.Sum256(msg.Data)
	record := deadLetterRecord{
		SchemaVersion:   deadLetterSchemaVersion,
		Reason:          reason,
		OriginalSubject: msg.Subject,
		PayloadSHA256:   hex.EncodeToString(digest[:]),
		PayloadBytes:    len(msg.Data),
		ReceivedAt:      time.Now().UTC(),
	}
	if envelope != nil && envelope.EventID != (domain.ID{}) {
		record.EventID = envelope.EventID.String()
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal nats dead-letter record: %w", err)
	}
	js, err := b.conn.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream dead-letter: %w", err)
	}
	ctx, cancel := deadLetterContext(b.ackWait())
	defer cancel()
	_, err = js.Publish(b.cfg.DeadLetterSubject, payload, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("publish nats dead-letter: %w", err)
	}
	return nil
}

func deadLetterContext(ackWait time.Duration) (context.Context, context.CancelFunc) {
	// Publishing a quarantine record is part of message acknowledgement. Keep
	// the wait bounded even when AckWait is configured very generously.
	const maxDeadLetterWait = 5 * time.Second
	if ackWait <= 0 || ackWait > maxDeadLetterWait {
		ackWait = maxDeadLetterWait
	}
	return context.WithTimeout(context.Background(), ackWait)
}
