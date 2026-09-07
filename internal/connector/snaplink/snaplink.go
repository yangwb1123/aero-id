package snaplink

import (
	"context"
	"fmt"

	"github.com/aero/aero-id/internal/connector"
)

// New constructs the compatibility-safe, summary-only Snaplink adapter. A
// caller that wants command/status methods must opt into both the capability
// configuration and the corresponding verified source contract.
func New(cfg connector.HTTPConfig) (connector.Connector, error) {
	return NewWithCapabilities(cfg, Capabilities{Summary: true})
}

// NewWithCapabilities constructs a Snaplink adapter with only the requested
// optional interfaces exposed. Capability validation happens before the shared
// HTTP connector is constructed, so invalid command configuration cannot load
// source credentials or create a client.
func NewWithCapabilities(cfg connector.HTTPConfig, capabilities Capabilities) (connector.Connector, error) {
	if err := ValidateCapabilities(cfg, capabilities); err != nil {
		return nil, err
	}
	base, err := connector.NewJSONSummaryConnector(connector.SourceSnaplink, cfg)
	if err != nil {
		return nil, err
	}
	return exposeCapabilities(base, capabilities)
}

func exposeCapabilities(base *connector.JSONSummaryConnector, capabilities Capabilities) (connector.Connector, error) {
	command, hasCommand := any(base).(connector.CommandConnector)
	status, hasStatus := any(base).(connector.OperationStatusConnector)
	if (capabilities.Erase && !hasCommand) || (capabilities.OperationStatus && !hasStatus) {
		return nil, fmt.Errorf("snaplink requested capability is unavailable")
	}
	switch {
	case capabilities.Erase && capabilities.OperationStatus:
		return &eraseAndStatusConnector{Connector: base,
			command: &snaplinkCommandConnector{Connector: base, command: command},
			status:  &snaplinkStatusConnector{Connector: base, status: status}}, nil
	case capabilities.Erase:
		return &eraseConnector{Connector: base,
			command: &snaplinkCommandConnector{Connector: base, command: command}}, nil
	case capabilities.OperationStatus:
		return &statusConnector{Connector: base,
			status: &snaplinkStatusConnector{Connector: base, status: status}}, nil
	default:
		return &readOnlyConnector{Connector: base}, nil
	}
}

type readOnlyConnector struct {
	connector.Connector
}

type eraseConnector struct {
	connector.Connector
	command connector.CommandConnector
}

func (c *eraseConnector) Erase(ctx context.Context, request connector.EraseRequest) (*connector.CommandResult, error) {
	return c.command.Erase(ctx, request)
}

type statusConnector struct {
	connector.Connector
	status connector.OperationStatusConnector
}

func (c *statusConnector) OperationStatus(ctx context.Context, request connector.OperationStatusRequest) (*connector.CommandResult, error) {
	return c.status.OperationStatus(ctx, request)
}

type eraseAndStatusConnector struct {
	connector.Connector
	command connector.CommandConnector
	status  connector.OperationStatusConnector
}

func (c *eraseAndStatusConnector) Erase(ctx context.Context, request connector.EraseRequest) (*connector.CommandResult, error) {
	return c.command.Erase(ctx, request)
}

func (c *eraseAndStatusConnector) OperationStatus(ctx context.Context, request connector.OperationStatusRequest) (*connector.CommandResult, error) {
	return c.status.OperationStatus(ctx, request)
}

var _ connector.Connector = (*readOnlyConnector)(nil)
var _ connector.CommandConnector = (*eraseConnector)(nil)
var _ connector.OperationStatusConnector = (*statusConnector)(nil)
var _ connector.CommandConnector = (*eraseAndStatusConnector)(nil)
var _ connector.OperationStatusConnector = (*eraseAndStatusConnector)(nil)
