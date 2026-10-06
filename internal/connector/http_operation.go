package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func (c *JSONSummaryConnector) OperationStatus(ctx context.Context, req OperationStatusRequest) (*CommandResult, error) {
	prepared, err := c.prepareOperationStatusRequest(req)
	if err != nil {
		return nil, err
	}
	return c.executeOperationStatus(ctx, prepared)
}

func (c *JSONSummaryConnector) prepareOperationStatusRequest(req OperationStatusRequest) (OperationStatusRequest, error) {
	if err := c.validateVaultRequestTenant(req.TenantID); err != nil {
		return OperationStatusRequest{}, err
	}
	var err error
	req.Region, err = c.vaultRequestRegion(req.Region)
	if err != nil {
		return OperationStatusRequest{}, err
	}
	if strings.TrimSpace(c.cfg.OperationStatusPath) == "" {
		return OperationStatusRequest{}, &RemoteError{Source: c.source, Kind: ErrorInvalid,
			Code: "source.operation_status_not_configured"}
	}
	return req, nil
}

func (c *JSONSummaryConnector) executeOperationStatus(ctx context.Context,
	req OperationStatusRequest) (result *CommandResult, resultErr error) {
	started := time.Now()
	release, err := c.begin(ctx, workloadCommand)
	if err != nil {
		c.observeCall("operation_status", started, err)
		return nil, err
	}
	defer func() {
		release()
		c.finish(workloadCommand, resultErr)
		c.observeCall("operation_status", started, resultErr)
	}()
	requestCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	endpoint, err := c.operationStatusURL(req)
	if err != nil {
		return nil, err
	}
	request, err := c.operationStatusRequest(requestCtx, endpoint, req)
	if err != nil {
		return nil, err
	}
	response, err := c.doHTTP(requestCtx, func() (*http.Request, error) { return request.Clone(requestCtx), nil })
	if err != nil {
		return nil, c.remoteRequestError(requestCtx, "source.operation_status_failed", err)
	}
	defer response.Body.Close()
	if err := c.validateOperationStatusResponse(response); err != nil {
		return nil, err
	}
	return c.decodeCommandResult(requestCtx, response.Body, req)
}

func (c *JSONSummaryConnector) validateOperationStatusResponse(response *http.Response) error {
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if c.source == SourceAeroVault {
		// A status lookup failure cannot prove that the original command failed.
		return vaultStatusHTTPError(response.StatusCode)
	}
	return remoteHTTPError(c.source, response.StatusCode)
}

func (c *JSONSummaryConnector) operationStatusURL(req OperationStatusRequest) (*url.URL, error) {
	endpoint, err := c.sourceEndpointURL()
	if err != nil || !appendSourcePath(endpoint, c.cfg.OperationStatusPath) {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint"}
	}
	query := endpoint.Query()
	query.Set("account_id", req.AccountID.String())
	query.Set("operation_id", req.OperationID)
	query.Set("source_operation_id", req.SourceOperationID)
	if req.Region != "" {
		query.Set("region", req.Region)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}

func (c *JSONSummaryConnector) operationStatusRequest(ctx context.Context, endpoint *url.URL, req OperationStatusRequest) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request", Cause: redactedRemoteCause(err)}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Aero-Account-ID", req.AccountID.String())
	request.Header.Set("X-Aero-Canonical-UID", req.CanonicalUID)
	request.Header.Set("X-Aero-Actor-UID", req.ActorUID)
	setTenantContextHeaders(request.Header, c.source, req.TenantID)
	if req.Region != "" {
		request.Header.Set("X-Aero-Region", req.Region)
	}
	request.Header.Set("X-Request-ID", req.RequestID)
	request.Header.Set("X-Aero-Operation-ID", req.OperationID)
	request.Header.Set("Idempotency-Key", req.IdempotencyKey)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, nil
}

type commandResultPayload struct {
	OperationID       string         `json:"operation_id"`
	SourceOperationID string         `json:"source_operation_id"`
	Status            string         `json:"status"`
	Result            map[string]any `json:"result"`
}

// operationStatusIDMismatch compares the source-owned source_operation_id. It
// is an opaque string minted by the source, so it stays exact-string compared:
// an empty response means the source omitted it, not that it contradicted the
// request.
func operationStatusIDMismatch(response, requested string) bool {
	return response != "" && response != requested
}

// operationCorrelationIDMismatch applies the same "empty means omitted" rule to
// the aero-id-owned correlation id, but compares by identity: an alternate
// rendering of the requested UUID (upper case, urn:uuid:, 32-hex, braced) is the
// same identity, not a contradiction. See SameCorrelationID.
func operationCorrelationIDMismatch(response, requested string) bool {
	return response != "" && !SameCorrelationID(requested, response)
}

func (c *JSONSummaryConnector) decodeCommandResult(ctx context.Context, body io.Reader, req OperationStatusRequest) (*CommandResult, error) {
	data, err := c.readCommandResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	var payload commandResultPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		if c.source == SourceAeroVault {
			return nil, unknownCommandOutcome(c.source, "source.response_invalid")
		}
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_invalid", Cause: redactedRemoteCause(err)}
	}
	if c.source == SourceAeroVault {
		return c.decodeVaultCommandResult(payload, req)
	}
	return c.decodeStandardCommandResult(payload, req)
}

func (c *JSONSummaryConnector) readCommandResponse(ctx context.Context, body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, c.commandResponseReadError(ctx, "source.response_read_failed", err)
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		if c.source == SourceAeroVault {
			return nil, unknownCommandOutcome(c.source, "source.response_too_large")
		}
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large"}
	}
	return data, nil
}

func (c *JSONSummaryConnector) decodeStandardCommandResult(payload commandResultPayload,
	req OperationStatusRequest) (*CommandResult, error) {
	if c.source == SourceAeroIM && (operationCorrelationIDMismatch(payload.OperationID, req.OperationID) ||
		operationStatusIDMismatch(payload.SourceOperationID, req.SourceOperationID)) {
		return nil, &RemoteError{Source: SourceAeroIM, Kind: ErrorInvalid, Code: "source.operation_id_mismatch"}
	}
	if payload.SourceOperationID == "" && c.source != SourceSnaplink {
		payload.SourceOperationID = payload.OperationID
	}
	if strings.TrimSpace(payload.Status) == "" {
		if c.source == SourceSnaplink {
			return &CommandResult{OperationID: payload.OperationID, SourceOperationID: payload.SourceOperationID,
					Result: RedactSummaryData(payload.Result)}, &RemoteError{Source: c.source, Kind: ErrorInvalid,
					Code: "source.operation_status_missing"}
		}
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.operation_status_missing"}
	}
	return &CommandResult{OperationID: payload.OperationID, SourceOperationID: payload.SourceOperationID,
		Status: strings.ToLower(payload.Status), Result: RedactSummaryData(payload.Result)}, nil
}

func (c *JSONSummaryConnector) remoteRequestError(ctx context.Context, code string, err error) *RemoteError {
	if remote, ok := IsRemote(err); ok {
		return remote
	}
	kind := ErrorUnavailable
	if requestTimedOut(ctx, err) {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: code, Temporary: true, Cause: redactedRemoteCause(err)}
}

func (c *JSONSummaryConnector) observeCall(operation string, started time.Time, err error) {
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.ObserveSourceCall(string(c.source), operation, time.Since(started), err)
	}
}

func (c *JSONSummaryConnector) decodeVaultCommandResult(payload commandResultPayload, req OperationStatusRequest) (*CommandResult, error) {
	if strings.TrimSpace(payload.OperationID) == "" || strings.TrimSpace(req.OperationID) == "" ||
		payload.OperationID != req.OperationID || strings.TrimSpace(payload.SourceOperationID) == "" ||
		strings.TrimSpace(req.SourceOperationID) == "" || payload.SourceOperationID != req.SourceOperationID {
		return nil, unknownCommandOutcome(c.source, "source.operation_outcome_mismatch")
	}
	if strings.TrimSpace(payload.Status) == "" {
		return nil, unknownCommandOutcome(c.source, "source.operation_status_missing")
	}
	return &CommandResult{OperationID: payload.OperationID, SourceOperationID: payload.SourceOperationID,
		Status: strings.ToLower(payload.Status), Result: RedactSummaryData(payload.Result)}, nil
}

func vaultStatusHTTPError(status int) *RemoteError {
	return &RemoteError{Source: SourceAeroVault, Kind: ErrorUnknown,
		Code: "source.operation_status_unavailable", Temporary: true,
		Cause: remoteHTTPError(SourceAeroVault, status)}
}

var _ OperationStatusConnector = (*JSONSummaryConnector)(nil)
