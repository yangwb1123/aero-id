package app

import "github.com/aero/aero-id/internal/connector"

func bypassSnaplinkConstruction() {
	_, _ = connector.NewJSONSummaryConnector(connector.SourceSnaplink, connector.HTTPConfig{})
}
