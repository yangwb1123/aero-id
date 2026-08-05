package aeroim

import "github.com/aero/aero-id/internal/connector"

func New(cfg connector.HTTPConfig) (connector.Connector, error) {
	return connector.NewJSONSummaryConnector(connector.SourceAeroIM, cfg)
}
