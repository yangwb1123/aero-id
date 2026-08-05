package app

import (
	"time"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// outboxDependencies carries the pieces the relay composition needs; extracted
// from new.go so the composition root stays under the file-size gate (P1-08).
type outboxDependencies struct {
	cfg        *config.Config
	log        *zap.Logger
	outboxRepo *outbox.GormRepository
	notifRepo  repository.NotificationRepository
	blockRepo  repository.BlockRepository
	prefRepo   repository.PreferenceRepository
	sseHub     *hub.Hub
	metricsReg *metrics.Registry
}

// buildOutboxRelay drains domain events written transactionally with their rows
// and fans them out to per-recipient notification rows. Block-aware and
// preference-aware: a recipient who blocked the actor or disabled the
// notification type gets no row. SSE push after create; optionally chained to
// an HMAC-signed webhook endpoint.
func buildOutboxRelay(deps outboxDependencies) (stop func()) {
	prefChecker := service.NewPreferenceChecker(deps.prefRepo)
	dispatcher := service.NewNotificationDispatcher(deps.notifRepo,
		service.WithDispatcherBlockChecker(deps.blockRepo),
		service.WithDispatcherPreferenceChecker(prefChecker),
		service.WithDispatcherRateLimiter(10),         // max 10 notifications per type per minute per recipient
		service.WithDispatcherCollapse(5*time.Minute), // merge duplicates within 5 minutes
		service.WithDispatcherPusher(deps.sseHub),
	)
	var relayHandler outbox.Handler = dispatcher
	if deps.cfg.Outbox.Webhook.URL != "" {
		timeout := 10 * time.Second
		if deps.cfg.Outbox.Webhook.Timeout != "" {
			if d, err := time.ParseDuration(deps.cfg.Outbox.Webhook.Timeout); err == nil && d > 0 {
				timeout = d
			}
		}
		whCfg := outbox.WebhookConfig{
			URL:        deps.cfg.Outbox.Webhook.URL,
			SigningKey: deps.cfg.Outbox.Webhook.SigningKey,
			Timeout:    timeout,
			EventTypes: deps.cfg.Outbox.Webhook.EventTypes,
			Headers:    deps.cfg.Outbox.Webhook.Headers,
		}
		relayHandler = outbox.NewChain(dispatcher, outbox.NewWebhookHandler(whCfg, deps.log))
		deps.log.Info("outbox webhook enabled",
			zap.String("url", deps.cfg.Outbox.Webhook.URL),
			zap.Strings("event_types", deps.cfg.Outbox.Webhook.EventTypes))
	}
	relay := outbox.NewRelay(deps.outboxRepo, relayHandler, deps.cfg.Outbox.MaxRetries, deps.log, deps.metricsReg)
	pollInterval := 5 * time.Second
	if deps.cfg.Outbox.PollInterval > 0 {
		pollInterval = deps.cfg.Outbox.PollInterval
	}
	return relay.Start(pollInterval)
}

