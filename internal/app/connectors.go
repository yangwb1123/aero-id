package app

import (
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/connector/aeroim"
	"github.com/aero/aero-id/internal/connector/aerovault"
	"github.com/aero/aero-id/internal/connector/snaplink"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

func buildSourceConnectors(cfg *config.Config, metricsReg *metrics.Registry) ([]connector.Connector, error) {
	result := make([]connector.Connector, 0, 3)
	if strings.TrimSpace(cfg.Sources.Snaplink.Endpoint) != "" {
		item, err := snaplink.New(sourceHTTPConfig(cfg.Sources.Snaplink, cfg.App.Env, metricsReg))
		if err != nil {
			return nil, fmt.Errorf("snaplink connector: %w", err)
		}
		result = append(result, item)
	}
	if strings.TrimSpace(cfg.Sources.AeroIM.Endpoint) != "" {
		item, err := aeroim.New(sourceHTTPConfig(cfg.Sources.AeroIM, cfg.App.Env, metricsReg))
		if err != nil {
			return nil, fmt.Errorf("aero-im connector: %w", err)
		}
		result = append(result, item)
	}
	if strings.TrimSpace(cfg.Sources.AeroVault.Endpoint) != "" {
		item, err := aerovault.New(sourceHTTPConfig(cfg.Sources.AeroVault, cfg.App.Env, metricsReg))
		if err != nil {
			return nil, fmt.Errorf("aero-vault connector: %w", err)
		}
		result = append(result, item)
	}
	return result, nil
}

func sourceHTTPConfig(value config.SourceEndpointConfig, environment string, metricsReg *metrics.Registry) connector.HTTPConfig {
	return connector.HTTPConfig{
		Endpoint: value.Endpoint, AllowInsecureLoopback: value.AllowInsecureLoopback,
		Environment: environment, SummaryPath: value.SummaryPath, ErasePath: value.ErasePath, Region: value.Region,
		OperationStatusPath: value.StatusPath,
		BearerToken:         value.BearerToken,
		OAuth: connector.OAuthClientCredentialsConfig{TokenURL: value.OAuthTokenURL, ClientID: value.OAuthClientID,
			ClientSecret: value.OAuthSecret, Scope: value.OAuthScope, Resource: value.OAuthResource},
		Timeout: value.Timeout, MaxBodyBytes: value.MaxBodyBytes, MaxConcurrency: value.MaxConcurrency,
		MaxAttempts: value.MaxAttempts, RetryBackoff: value.RetryBackoff, TLSCAFile: value.TLSCAFile, TLSCertFile: value.TLSCertFile,
		TLSKeyFile: value.TLSKeyFile, TLSServerName: value.TLSServerName, Metrics: metricsReg}
}
