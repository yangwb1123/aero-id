package connector

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (c *JSONSummaryConnector) Health(ctx context.Context) (resultErr error) {
	started := time.Now()
	release, err := c.begin(ctx, workloadHealth)
	if err != nil {
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "health", time.Since(started), err)
		}
		return err
	}
	defer func() {
		release()
		c.finish(workloadHealth, resultErr)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "health", time.Since(started), resultErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	request, err := c.healthRequest(ctx)
	if err != nil {
		return err
	}
	response, err := c.doHTTP(ctx, func() (*http.Request, error) { return request.Clone(ctx), nil })
	if err != nil {
		if remote, ok := IsRemote(err); ok {
			return remote
		}
		kind := ErrorUnavailable
		if requestTimedOut(ctx, err) {
			kind = ErrorTimeout
		}
		return &RemoteError{Source: c.source, Kind: kind, Code: "source.health_failed",
			Temporary: true, Cause: redactedRemoteCause(err)}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return remoteHTTPError(c.source, response.StatusCode)
	}
	return nil
}

func (c *JSONSummaryConnector) healthRequest(ctx context.Context) (*http.Request, error) {
	endpoint, err := c.sourceEndpointURL()
	if err != nil || !appendSourcePath(endpoint, "/healthz") {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request",
			Cause: redactedRemoteCause(err)}
	}
	return request, nil
}

func (c *JSONSummaryConnector) Erase(ctx context.Context, req EraseRequest) (*CommandResult, error) {
	prepared, err := c.prepareEraseRequest(req)
	if err != nil {
		return nil, err
	}
	return c.executeErase(ctx, prepared)
}

func (c *JSONSummaryConnector) prepareEraseRequest(req EraseRequest) (EraseRequest, error) {
	resolved, err := ResolveDatasetScope(c.source, req.Datasets, DatasetScopeDestructive)
	if err != nil {
		return EraseRequest{}, err
	}
	req.Datasets = resolved
	req.Region, err = c.vaultRequestRegion(req.Region)
	if err != nil {
		return EraseRequest{}, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return EraseRequest{}, &RemoteError{Source: c.source, Kind: ErrorInvalid,
			Code: "source.idempotency_key_missing", Temporary: false}
	}
	if strings.TrimSpace(c.cfg.ErasePath) == "" {
		return EraseRequest{}, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.erase_not_configured"}
	}
	return req, nil
}

func (c *JSONSummaryConnector) executeErase(ctx context.Context, req EraseRequest) (result *CommandResult, resultErr error) {
	started := time.Now()
	release, err := c.beginObserved(ctx, workloadCommand, "erase", started)
	if err != nil {
		return nil, err
	}
	defer func() {
		release()
		c.finish(workloadCommand, resultErr)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "erase", time.Since(started), resultErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	httpReq, body, err := c.newEraseRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	response, err := c.doHTTP(ctx, func() (*http.Request, error) {
		return cloneEraseRequest(ctx, httpReq, body)
	})
	if err != nil {
		return nil, c.eraseRequestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteHTTPError(c.source, response.StatusCode)
	}
	return c.decodeEraseResult(ctx, response.Body)
}

func (c *JSONSummaryConnector) eraseRequestError(ctx context.Context, err error) *RemoteError {
	if remote, ok := IsRemote(err); ok {
		return remote
	}
	kind := ErrorUnavailable
	if requestTimedOut(ctx, err) {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: "source.erase_failed", Temporary: true, Cause: redactedRemoteCause(err)}
}

func cloneEraseRequest(ctx context.Context, template *http.Request, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, template.URL.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	for key, values := range template.Header {
		request.Header[key] = append([]string(nil), values...)
	}
	return request, nil
}
