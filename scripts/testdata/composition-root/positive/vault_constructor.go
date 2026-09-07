package app

import "github.com/aero/aero-id/internal/connector"

func bypassVaultConstruction() {
	_, _ = connector.NewJSONSummaryConnector(connector.SourceAeroVault, connector.HTTPConfig{})
}
