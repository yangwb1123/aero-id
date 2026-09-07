package snaplink

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/aero/aero-id/internal/connector"
)

type snaplinkCommandConnector struct {
	connector.Connector
	command connector.CommandConnector
}

func (c *snaplinkCommandConnector) Erase(ctx context.Context, request connector.EraseRequest) (*connector.CommandResult, error) {
	if !validCorrelationID(request.OperationID) {
		if strings.TrimSpace(request.OperationID) == "" {
			return nil, unknownResultError("source.operation_id_missing")
		}
		return nil, unknownResultError("source.operation_id_mismatch")
	}
	result, err := c.command.Erase(ctx, request)
	if err != nil {
		return result, normalizeEraseError(err)
	}
	if err := validateEraseResult(request, result); err != nil {
		return result, err
	}
	return result, nil
}

type snaplinkStatusConnector struct {
	connector.Connector
	status connector.OperationStatusConnector
}

func (c *snaplinkStatusConnector) OperationStatus(ctx context.Context,
	request connector.OperationStatusRequest) (*connector.CommandResult, error) {
	if !validCorrelationID(request.OperationID) || !validCorrelationID(request.SourceOperationID) {
		if strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.SourceOperationID) == "" {
			return nil, unknownResultError("source.operation_id_missing")
		}
		return nil, unknownResultError("source.operation_id_mismatch")
	}
	result, err := c.status.OperationStatus(ctx, request)
	if err != nil {
		return result, normalizeStatusError(err)
	}
	if err := validateStatusResult(request, result); err != nil {
		return result, err
	}
	return result, nil
}

func validateEraseResult(request connector.EraseRequest, result *connector.CommandResult) error {
	if result == nil || !validCorrelationID(request.OperationID) ||
		!validCorrelationID(result.OperationID) || result.OperationID != request.OperationID {
		return unknownResultError("source.operation_id_mismatch")
	}
	if !validCorrelationID(result.SourceOperationID) {
		return unknownResultError("source.source_operation_id_missing")
	}
	if strings.TrimSpace(result.SourceOperationID) == "" {
		return unknownResultError("source.source_operation_id_missing")
	}
	if strings.TrimSpace(result.Status) == "" {
		return unknownResultError("source.operation_status_missing")
	}
	return nil
}

func validateStatusResult(request connector.OperationStatusRequest, result *connector.CommandResult) error {
	if result == nil || !validCorrelationID(request.OperationID) || !validCorrelationID(request.SourceOperationID) ||
		!validCorrelationID(result.OperationID) || result.OperationID != request.OperationID {
		return unknownResultError("source.operation_id_mismatch")
	}
	if !validCorrelationID(result.SourceOperationID) {
		return unknownResultError("source.source_operation_id_missing")
	}
	if result.SourceOperationID != request.SourceOperationID {
		return unknownResultError("source.source_operation_id_mismatch")
	}
	if strings.TrimSpace(result.Status) == "" {
		return unknownResultError("source.operation_status_missing")
	}
	return nil
}

func validCorrelationID(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func normalizeEraseError(err error) error {
	if remote, ok := connector.IsRemote(err); ok {
		if remote.Kind == connector.ErrorUnauthorized || remote.Kind == connector.ErrorForbidden ||
			isLocalEraseError(remote.Code) || isDefinitiveHTTPClientError(remote) {
			return err
		}
		return unknownRemoteError(remote)
	}
	return unknownError("source.erase_unknown", err)
}

func normalizeStatusError(err error) error {
	if remote, ok := connector.IsRemote(err); ok {
		return unknownRemoteError(remote)
	}
	return unknownError("source.operation_status_unknown", err)
}

func isLocalEraseError(code string) bool {
	switch code {
	case "source.idempotency_key_missing", "source.dataset_scope_invalid", "source.erase_not_configured",
		"source.endpoint", "source.request":
		return true
	default:
		return false
	}
}

func isDefinitiveHTTPClientError(remote *connector.RemoteError) bool {
	if remote == nil || remote.Kind != connector.ErrorInvalid || !strings.HasPrefix(remote.Code, "source.http_4") {
		return false
	}
	return remote.Code != "source.http_408" && remote.Code != "source.http_429"
}

func unknownRemoteError(remote *connector.RemoteError) error {
	code := remote.Code
	if code == "" {
		code = "source.unknown_outcome"
	}
	return &connector.RemoteError{Source: connector.SourceSnaplink, Kind: connector.ErrorUnknown,
		Code: code, Temporary: true}
}

func unknownResultError(code string) error {
	return &connector.RemoteError{Source: connector.SourceSnaplink, Kind: connector.ErrorUnknown,
		Code: code, Temporary: true}
}

func unknownError(code string, cause error) error {
	if cause == nil {
		return unknownResultError(code)
	}
	// Do not retain arbitrary transport/error text: source errors can contain
	// URLs, response fragments, or credentials. The unknown state itself is
	// durable evidence; the connector code is the safe public detail.
	var remote *connector.RemoteError
	if errors.As(cause, &remote) {
		return unknownRemoteError(remote)
	}
	return unknownResultError(code)
}

var _ connector.CommandConnector = (*snaplinkCommandConnector)(nil)
var _ connector.OperationStatusConnector = (*snaplinkStatusConnector)(nil)
