package app

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/service"
)

// buildEventBusConsumer wires the optional canonical-event consumer transport
// (P3-04). When enabled, the NATS JetStream adapter feeds the same ingest
// service as the HTTP endpoint; durable receipts keep both transports
// exactly-once and idempotent. Returns nil when disabled.
func buildEventBusConsumer(cfg *config.Config, log *zap.Logger,
	ingest service.EventIngestService) (stop func(), err error) {
	if !cfg.EventBus.NATS.Enabled {
		return nil, nil
	}
	bus, err := event.NewNATSBus(event.NATSConfig{
		URL: cfg.EventBus.NATS.URL, Stream: cfg.EventBus.NATS.Stream,
		Subject: cfg.EventBus.NATS.Subject, Durable: cfg.EventBus.NATS.Durable,
		MaxRedeliveries: cfg.EventBus.NATS.MaxRedeliveries, AckWait: cfg.EventBus.NATS.AckWait,
	})
	if err != nil {
		return nil, fmt.Errorf("event bus: %w", err)
	}
	stop, err = bus.Subscribe(context.Background(), func(ctx context.Context, envelope *event.Envelope) error {
		_, err := ingest.Ingest(ctx, envelope)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("event bus subscribe: %w", err)
	}
	log.Info("event bus consumer enabled", zap.String("stream", cfg.EventBus.NATS.Stream),
		zap.String("subject", cfg.EventBus.NATS.Subject), zap.String("durable", cfg.EventBus.NATS.Durable))
	return stop, nil
}
