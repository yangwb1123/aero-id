package app

import (
	"strings"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/service"
)

func buildCapabilityService(deps serviceDependencies, accounts service.AccountService) service.CapabilityService {
	cfg := service.CapabilityConfig{LocalRegion: deps.cfg.Account.Region,
		ExportEnabled:    deps.store != nil && deps.cfg.Export.EncryptionKey != "",
		CallbacksEnabled: deps.cfg.Callback.MasterKey != "", Sources: map[string]service.SourceCapabilityConfig{}}
	for _, client := range deps.sourceConnectors {
		if client == nil {
			continue
		}
		cfg.Sources[string(client.Source())] = sourceCapabilityFlags(deps.cfg, client)
	}
	// Report global permissions from the authority, never from the legacy
	// local role/ban checker or its fail-open compatibility fallback.
	return service.NewCapabilityService(accounts, deps.snapshotRepo, deps.ssoClient, cfg)
}

func sourceCapabilityFlags(cfg *config.Config, client connector.Connector) service.SourceCapabilityConfig {
	var value config.SourceEndpointConfig
	summary := true
	switch client.Source() {
	case connector.SourceSnaplink:
		value = cfg.Sources.Snaplink
		summary = value.Capabilities.Summary
	case connector.SourceAeroIM:
		value = cfg.Sources.AeroIM
	case connector.SourceAeroVault:
		value = cfg.Sources.AeroVault
	default:
		return service.SourceCapabilityConfig{}
	}
	_, command := client.(connector.CommandConnector)
	_, status := client.(connector.OperationStatusConnector)
	if client.Source() == connector.SourceSnaplink {
		command = command && value.Capabilities.Erase
		status = status && value.Capabilities.OperationStatus
	}
	return service.SourceCapabilityConfig{SummaryEnabled: summary,
		EraseEnabled:  command && strings.TrimSpace(value.ErasePath) != "",
		StatusEnabled: status && strings.TrimSpace(value.StatusPath) != ""}
}
