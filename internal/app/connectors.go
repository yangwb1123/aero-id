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
		value := cfg.Sources.Snaplink
		capabilities := snaplink.Capabilities{
			Summary: value.Capabilities.Summary, Erase: value.Capabilities.Erase,
			OperationStatus: value.Capabilities.OperationStatus,
			EraseContractVersion: value.Capabilities.EraseContractVersion,
		}
		item, err := snaplink.NewWithCapabilities(sourceHTTPConfig(value, cfg.App.Env, metricsReg), capabilities)
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
		value := cfg.Sources.AeroVault
		httpCfg := sourceHTTPConfig(value, cfg.App.Env, metricsReg)
		capabilities := aerovault.Capabilities{Erase: value.Capabilities.Erase, OperationStatus: value.Capabilities.OperationStatus}
		var item connector.Connector
		var err error
		if capabilities.Erase || capabilities.OperationStatus {
			item, err = aerovault.NewWithCapabilities(httpCfg, capabilities)
		} else {
			item, err = aerovault.New(httpCfg)
		}
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
			ClientSecret: value.OAuthSecret, Scope: value.OAuthScope, Resource: value.OAuthResource,
			AllowedHosts: value.OAuthAllowedHosts},
		OAuthAllowedHosts: value.OAuthAllowedHosts,
		OAuthTLSCAFile:    value.OAuthTLSCAFile, OAuthTLSCertFile: value.OAuthTLSCertFile,
		OAuthTLSKeyFile: value.OAuthTLSKeyFile, OAuthTLSServerName: value.OAuthTLSServerName,
		OAuthTenantID: value.OAuthTenantID,
		Timeout:       value.Timeout, MaxBodyBytes: value.MaxBodyBytes, MaxConcurrency: value.MaxConcurrency,
		QueryMaxConcurrency: value.QueryMaxConcurrency, CommandMaxConcurrency: value.CommandMaxConcurrency,
		HealthMaxConcurrency: value.HealthMaxConcurrency, MaxAttempts: value.MaxAttempts, RetryBackoff: value.RetryBackoff,
		TLSCAFile: value.TLSCAFile, TLSCertFile: value.TLSCertFile,
		TLSKeyFile: value.TLSKeyFile, TLSServerName: value.TLSServerName,
		TargetAssertionKeyFile: value.TargetAssertionKeyFile, TargetAssertionKeyEnv: value.TargetAssertionKeyEnv,
		TargetAssertionJWKSURL: value.TargetAssertionJWKSURL, TargetAssertionTrustDomain: value.TargetAssertionTrustDomain,
		TargetAssertionKeyID: value.TargetAssertionKeyID, TargetAssertionIssuer: value.TargetAssertionIssuer,
		TargetAssertionAudience: value.TargetAssertionAudience, TargetAssertionSubject: value.TargetAssertionSubject,
		TargetAssertionClientID: value.TargetAssertionClientID, TargetAssertionLifetime: value.TargetAssertionLifetime,
		Metrics: metricsReg}
}
