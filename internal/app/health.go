package app

import (
	"context"
	"database/sql"

	"github.com/aero/aero-id/internal/config"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/sso"
)

func buildHealthHandler(cfg *config.Config, db *sql.DB, ssoClient *sso.Client,
	billingProbe func(context.Context) error) *httpapi.HealthHandler {
	checks := []httpapi.Check{
		{Name: "database", Critical: true, Probe: db.PingContext},
		{Name: "sso", Critical: cfg.SSO.ReadinessCritical, Probe: ssoClient.HealthCheck},
	}
	if billingProbe != nil {
		checks = append(checks, httpapi.Check{Name: "billing", Critical: false, Probe: billingProbe})
	}
	return httpapi.NewHealthHandler(checks...)
}
