package app

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
)

type capabilityTestConnector struct {
	connector.Connector
	source connector.Source
}

func (f capabilityTestConnector) Source() connector.Source { return f.source }

type capabilityCommandTestConnector struct{ capabilityTestConnector }

func (f capabilityCommandTestConnector) Erase(context.Context, connector.EraseRequest) (*connector.CommandResult, error) {
	return nil, nil
}

func TestCapabilityFlagsRequireConfiguredSourceAndCommandInterface(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sources.Snaplink.ErasePath = "/erase"
	read := capabilityTestConnector{source: connector.SourceSnaplink}
	if value := sourceCapabilityFlags(cfg, read); value.SummaryEnabled || value.EraseEnabled || value.StatusEnabled {
		t.Fatalf("defaults = %+v", value)
	}
	cfg.Sources.Snaplink.Capabilities.Summary = true
	if value := sourceCapabilityFlags(cfg, read); !value.SummaryEnabled || value.EraseEnabled {
		t.Fatalf("read-only = %+v", value)
	}
	command := capabilityCommandTestConnector{read}
	if value := sourceCapabilityFlags(cfg, command); value.EraseEnabled {
		t.Fatalf("path without explicit capability = %+v", value)
	}
	cfg.Sources.Snaplink.Capabilities.Erase = true
	if value := sourceCapabilityFlags(cfg, command); !value.EraseEnabled {
		t.Fatalf("command = %+v", value)
	}
	cfg.Sources.Snaplink.ErasePath = ""
	if value := sourceCapabilityFlags(cfg, command); value.EraseEnabled {
		t.Fatalf("missing erase contract = %+v", value)
	}
}
