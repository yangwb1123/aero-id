package aerovault

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

const DefaultSummaryPath = "/v1/internal/account-summary"

// Capabilities are explicit opt-ins for source command seams. The default
// Vault adapter is intentionally read-only until the corresponding source
// contract is deployed and configured.
type Capabilities struct {
	Erase           bool
	OperationStatus bool
}

// New constructs a read-only Vault adapter. It rejects command paths rather
// than silently shipping command methods merely because the shared HTTP
// implementation has them.
func New(cfg connector.HTTPConfig) (connector.Connector, error) {
	return NewWithCapabilities(cfg, Capabilities{})
}

// NewWithCapabilities constructs a Vault adapter with only the requested
// optional interfaces exposed. The generic connector remains responsible for
// transport, authentication, deadlines, retries, and response handling.
func NewWithCapabilities(cfg connector.HTTPConfig, capabilities Capabilities) (connector.Connector, error) {
	if cfg.SummaryPath == "" {
		cfg.SummaryPath = DefaultSummaryPath
	}
	if err := validateCapabilityPath("erase", capabilities.Erase, cfg.ErasePath); err != nil {
		return nil, err
	}
	if err := validateCapabilityPath("operation status", capabilities.OperationStatus, cfg.OperationStatusPath); err != nil {
		return nil, err
	}
	if err := validateCapabilityAuthentication(cfg, capabilities); err != nil {
		return nil, err
	}
	base, err := connector.NewJSONSummaryConnector(connector.SourceAeroVault, cfg)
	if err != nil {
		return nil, err
	}
	return exposeCapabilities(base, capabilities)
}

func validateCapabilityPath(name string, enabled bool, path string) error {
	configured := strings.TrimSpace(path) != ""
	if enabled && !configured {
		return errors.New("aero-vault " + name + " capability requires its configured path")
	}
	if !enabled && configured {
		return errors.New("aero-vault " + name + " path requires explicit capability enablement")
	}
	return nil
}

func validateCapabilityAuthentication(cfg connector.HTTPConfig, capabilities Capabilities) error {
	if !capabilities.Erase && !capabilities.OperationStatus {
		return nil
	}
	allowedHosts := cfg.OAuthAllowedHosts
	if len(allowedHosts) == 0 {
		allowedHosts = cfg.OAuth.AllowedHosts
	}
	decision, err := sourceauth.Evaluate(sourceauth.Config{
		Source: "aero-vault", Endpoint: cfg.Endpoint, Environment: cfg.Environment,
		AllowInsecureLoopback: cfg.AllowInsecureLoopback, BearerToken: cfg.BearerToken,
		OAuth: sourceauth.OAuthConfig{TokenURL: cfg.OAuth.TokenURL, ClientID: cfg.OAuth.ClientID,
			ClientSecret: cfg.OAuth.ClientSecret, Scope: cfg.OAuth.Scope, Resource: cfg.OAuth.Resource,
			AllowedHosts: allowedHosts}, TLSCertFile: cfg.TLSCertFile, TLSKeyFile: cfg.TLSKeyFile,
	})
	if err != nil {
		return err
	}
	if decision.Mode == sourceauth.ModeLoopback {
		return errors.New("aero-vault command capabilities require authenticated source credentials")
	}
	return nil
}

func exposeCapabilities(base *connector.JSONSummaryConnector, capabilities Capabilities) (connector.Connector, error) {
	command, hasCommand := any(base).(connector.CommandConnector)
	status, hasStatus := any(base).(connector.OperationStatusConnector)
	if (capabilities.Erase && !hasCommand) || (capabilities.OperationStatus && !hasStatus) {
		return nil, errors.New("aero-vault requested capability is unavailable")
	}
	switch {
	case capabilities.Erase && capabilities.OperationStatus:
		return &eraseAndStatusConnector{Connector: base, command: command, status: status}, nil
	case capabilities.Erase:
		return &eraseConnector{Connector: base, command: command}, nil
	case capabilities.OperationStatus:
		return &statusConnector{Connector: base, status: status}, nil
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
