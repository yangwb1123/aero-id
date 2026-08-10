package service

import "github.com/aero/aero-id/internal/connector"

// Rule (d) negative fixture: service -> connector is legal (upstream):
// connectors are anti-corruption adapters over source APIs; only the reverse
// (connector importing service) is forbidden by rule (d).
func svc(c *connector.Connector) {}
