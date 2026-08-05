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

	"github.com/aero/aero-id/internal/domain"
)

func (c *JSONSummaryConnector) newSummaryRequest(ctx context.Context, req SummaryRequest) (*http.Request, error) {
	endpoint, err := c.summaryURL(req)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request", Cause: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Aero-Account-ID", req.AccountID.String())
	request.Header.Set("X-Aero-Canonical-UID", req.CanonicalUID)
	request.Header.Set("X-Aero-Actor-UID", req.ActorUID)
	request.Header.Set("X-Aero-Tenant-ID", req.TenantID)
	if req.Region != "" {
		request.Header.Set("X-Aero-Region", req.Region)
	}
	request.Header.Set("X-Request-ID", req.RequestID)
	request.Header.Set("X-Aero-Operation-ID", req.OperationID)
	setBearer(request, c.cfg.BearerToken)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, nil
}

func (c *JSONSummaryConnector) summaryURL(req SummaryRequest) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.cfg.Endpoint, "/") + c.cfg.SummaryPath)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint", Cause: err}
	}
	query := endpoint.Query()
	query.Set("account_id", req.AccountID.String())
	if req.Region != "" {
		query.Set("region", req.Region)
	}
	if req.Reconcile {
		query.Set("reconcile", "true")
	}
	for _, dataset := range req.Datasets {
		query.Add("dataset", string(dataset))
	}
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}

func setBearer(request *http.Request, token string) {
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *JSONSummaryConnector) newEraseRequest(ctx context.Context, req EraseRequest) (*http.Request, []byte, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.cfg.Endpoint, "/") + c.cfg.ErasePath)
	if err != nil {
		return nil, nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint", Cause: err}
	}
	body, err := json.Marshal(map[string]any{"account_id": req.AccountID.String(), "canonical_uid": req.CanonicalUID,
		"datasets": req.Datasets, "idempotency_key": req.IdempotencyKey, "operation_id": req.OperationID})
	if err != nil {
		return nil, nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request", Cause: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aero-Account-ID", req.AccountID.String())
	request.Header.Set("X-Aero-Canonical-UID", req.CanonicalUID)
	request.Header.Set("X-Aero-Actor-UID", req.ActorUID)
	request.Header.Set("X-Aero-Tenant-ID", req.TenantID)
	request.Header.Set("X-Request-ID", req.RequestID)
	request.Header.Set("X-Aero-Operation-ID", req.OperationID)
	request.Header.Set("Idempotency-Key", req.IdempotencyKey)
	setBearer(request, c.cfg.BearerToken)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, body, nil
}

func (c *JSONSummaryConnector) decodeEraseResult(body io.Reader) (*CommandResult, error) {
	data, err := io.ReadAll(io.LimitReader(body, c.cfg.MaxBodyBytes+1))
	if err != nil || int64(len(data)) > c.cfg.MaxBodyBytes {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large", Cause: err}
	}
	if len(data) == 0 {
		return &CommandResult{Status: "accepted"}, nil
	}
	var payload struct {
		OperationID       string         `json:"operation_id"`
		SourceOperationID string         `json:"source_operation_id"`
		Status            string         `json:"status"`
		Result            map[string]any `json:"result"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_invalid", Cause: err}
	}
	if payload.SourceOperationID == "" {
		payload.SourceOperationID = payload.OperationID
	}
	if payload.Status == "" {
		payload.Status = "accepted"
	}
	return &CommandResult{SourceOperationID: payload.SourceOperationID, Status: payload.Status, Result: payload.Result}, nil
}

func (c *JSONSummaryConnector) summaryFromPayload(payload *summaryPayload) (*Summary, error) {
	region := payload.SourceRegion
	if region == "" {
		region = c.cfg.Region
	}
	if c.cfg.Region != "" && region != c.cfg.Region {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.region_mismatch"}
	}
	if payload.GeneratedAt.IsZero() {
		payload.GeneratedAt = time.Now().UTC()
	}
	if payload.Version < 1 {
		payload.Version = 1
	}
	result := &Summary{Source: c.source, SourceRegion: region, Version: payload.Version, Complete: payload.Complete}
	if err := c.appendDatasetSnapshots(result, payload.Datasets, region, payload.GeneratedAt); err != nil {
		return nil, err
	}
	result.Sources = sourceReferences(c.source, region, payload)
	result.Memberships = membershipReferences(c.source, region, payload)
	return result, nil
}

func (c *JSONSummaryConnector) appendDatasetSnapshots(result *Summary, datasets map[string]json.RawMessage,
	region string, generatedAt time.Time) error {
	for name, raw := range datasets {
		dataset := domain.AccountDataset(name)
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(c.source) {
			continue
		}
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			return &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.dataset_invalid", Cause: err}
		}
		result.Snapshots = append(result.Snapshots, &domain.AccountSnapshot{Source: c.source.String(), SourceRegion: region,
			Dataset: dataset, Status: domain.SnapshotFresh, Version: result.Version, GeneratedAt: generatedAt,
			LastSyncedAt: timePtr(time.Now().UTC()), Data: RedactSummaryData(values)})
	}
	return nil
}

func sourceReferences(source Source, region string, payload *summaryPayload) []*domain.AccountSource {
	result := make([]*domain.AccountSource, 0, len(payload.Sources))
	for _, value := range payload.Sources {
		if value.SourceAccountID == "" {
			continue
		}
		result = append(result, &domain.AccountSource{Source: source.String(), SourceRegion: region,
			SourceAccountID: value.SourceAccountID, ScopeType: value.ScopeType, ScopeID: value.ScopeID, Status: value.Status,
			Version: payload.Version, GeneratedAt: timePtr(payload.GeneratedAt), LastSyncedAt: timePtr(time.Now().UTC()),
			Data: RedactSummaryData(value.Data)})
	}
	return result
}

func membershipReferences(source Source, region string, payload *summaryPayload) []*domain.AccountMembership {
	result := make([]*domain.AccountMembership, 0, len(payload.Memberships))
	for _, value := range payload.Memberships {
		if value.ScopeType == "" || value.ScopeID == "" || value.SourceMemberID == "" {
			continue
		}
		result = append(result, &domain.AccountMembership{Source: source.String(), SourceRegion: region,
			ScopeType: value.ScopeType, ScopeID: value.ScopeID, SourceMemberID: value.SourceMemberID,
			Role: value.Role, Status: value.Status, Version: payload.Version, LastSyncedAt: timePtr(time.Now().UTC()),
			Data: RedactSummaryData(value.Data)})
	}
	return result
}
