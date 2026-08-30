package app

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	billingconnector "github.com/aero/aero-id/internal/connector/billing"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

type billingRuntime struct {
	quota service.AccountQuota
	probe func(context.Context) error
}

func buildBillingRuntime(cfg *config.Config, db *gorm.DB, sagaRepo repository.AccountQuotaSagaRepository) (billingRuntime, error) {
	if !cfg.Billing.Enabled {
		return billingRuntime{}, nil
	}
	bindings := make(map[string]billingconnector.BindingConfig, len(cfg.Billing.Bindings))
	for tenantID, value := range cfg.Billing.Bindings {
		secret, err := config.ResolveClientSecretEnv(value.ClientSecretEnv)
		if err != nil {
			return billingRuntime{}, fmt.Errorf("billing credentials are unavailable")
		}
		bindings[tenantID] = billingconnector.BindingConfig{
			TokenURL: value.TokenURL, ClientID: value.ClientID, ClientSecret: secret,
			Scope: value.Scope, Resources: value.Resources,
		}
	}
	client, err := billingconnector.NewClient(billingconnector.Config{
		Endpoint: cfg.Billing.Endpoint, Timeout: cfg.Billing.Timeout, MaxBodyBytes: cfg.Billing.MaxBodyBytes,
		AllowInsecureLoopback: cfg.Billing.AllowInsecureLoopback, Bindings: bindings,
	}, nil)
	if err != nil {
		return billingRuntime{}, fmt.Errorf("billing client: %w", err)
	}
	repo := repository.NewEntitlementRepository(db)
	quota := service.NewBillingServiceWithSaga(repo, client, sagaRepo, service.BillingConfig{
		MaxProjectionStaleness: cfg.Billing.MaxProjectionStaleness,
		AccountFeature:         cfg.Billing.AccountFeature, UserDimension: cfg.Billing.UserDimension,
		ReservationTTL: cfg.Billing.ReservationTTL,
	})
	return billingRuntime{quota: quota, probe: client.HealthCheck}, nil
}
