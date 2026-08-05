package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// buildProjectionReplication wires the optional cross-region projection
// replication worker (P6-03). Returns a nil stop func when disabled.
func buildProjectionReplication(cfg *config.Config, gdb *gorm.DB, log *zap.Logger) (stop func(), err error) {
	if !cfg.Replication.Enabled {
		return nil, nil
	}
	var publisher event.EventPublisher
	switch cfg.Replication.Transport {
	case "nats":
		natsPublisher, err := event.NewNATSPublisher(cfg.EventBus.NATS.URL, cfg.Replication.NATSSubject)
		if err != nil {
			return nil, fmt.Errorf("replication nats publisher: %w", err)
		}
		publisher = natsPublisher
	case "http":
		httpPublisher, err := event.NewHTTPPublisher(cfg.Replication.HTTPEndpoint, cfg.Replication.HTTPKey,
			&http.Client{Timeout: 10 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("replication http publisher: %w", err)
		}
		publisher = httpPublisher
	default:
		return nil, fmt.Errorf("replication transport %q is not supported", cfg.Replication.Transport)
	}
	batch := cfg.Replication.BatchSize
	if batch <= 0 {
		batch = service.DefaultReplicationBatch
	}
	worker := service.NewProjectionReplicationService(cfg.Account.Region,
		repository.NewAccountSnapshotRepository(gdb),
		repository.NewReplicationCursorRepository(gdb),
		publisher)
	interval := cfg.Replication.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	stop = worker.Start(context.Background(), interval)
	log.Info("projection replication enabled",
		zap.String("region", cfg.Account.Region),
		zap.String("transport", cfg.Replication.Transport),
		zap.Duration("interval", interval))
	return stop, nil
}
