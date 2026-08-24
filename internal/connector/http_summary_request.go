package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
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
	if req.Region != "" {
		query := endpoint.Query()
		query.Set("region", req.Region)
		endpoint.RawQuery = query.Encode()
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
	if req.Region != "" {
		request.Header.Set("X-Aero-Region", req.Region)
	}
	request.Header.Set("X-Request-ID", req.RequestID)
	request.Header.Set("X-Aero-Operation-ID", req.OperationID)
	request.Header.Set("Idempotency-Key", req.IdempotencyKey)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, body, nil
}

func (c *JSONSummaryConnector) decodeEraseResult(body io.Reader) (*CommandResult, error) {
	data, err := io.ReadAll(io.LimitReader(body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.response_read_failed", Temporary: true, Cause: err}
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large"}
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
	region, err := c.validateSummaryPayload(payload)
	if err != nil {
		return nil, err
	}
	result := newSummaryResult(c.source, region, payload)
	if err := c.appendDatasetSnapshots(result, payload.Datasets, region, payload.GeneratedAt); err != nil {
		return nil, err
	}
	result.Complete = payload.Complete && len(result.SkippedDatasets) == 0
	if err := c.attachSummaryReferences(result, region, payload); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *JSONSummaryConnector) validateSummaryPayload(payload *summaryPayload) (string, error) {
	region := strings.TrimSpace(payload.SourceRegion)
	if region == "" {
		return "", &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.region_missing"}
	}
	if c.cfg.Region != "" && region != c.cfg.Region {
		return "", &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.region_mismatch"}
	}
	if payload.GeneratedAt.IsZero() {
		return "", &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.generated_at_missing"}
	}
	if payload.Version < 1 {
		return "", &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.version_invalid"}
	}
	return region, nil
}

func newSummaryResult(source Source, region string, payload *summaryPayload) *Summary {
	result := &Summary{Source: source, SourceRegion: region, Version: payload.Version, Complete: payload.Complete}
	if payload.Coverage == nil {
		return result
	}
	result.Coverage = domain.SummaryCoverage{Sources: payload.Coverage.Sources,
		Memberships: payload.Coverage.Memberships, Explicit: true}
	for _, name := range payload.Coverage.Datasets {
		result.Coverage.Datasets = append(result.Coverage.Datasets, domain.AccountDataset(strings.TrimSpace(name)))
	}
	return result
}

func (c *JSONSummaryConnector) attachSummaryReferences(result *Summary, region string,
	payload *summaryPayload) error {
	sources, err := sourceReferences(c.source, region, payload)
	if err != nil {
		return err
	}
	memberships, err := membershipReferences(c.source, region, payload)
	if err != nil {
		return err
	}
	result.Sources, result.Memberships = sources, memberships
	return nil
}

func (c *JSONSummaryConnector) appendDatasetSnapshots(result *Summary, datasets map[string]json.RawMessage,
	region string, generatedAt time.Time) error {
	for name, raw := range datasets {
		dataset := domain.AccountDataset(name)
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(c.source) {
			// Record the drop: the raw name is kept (including names not in the
			// registry), so an incomplete source summary can never be marked
			// reconciled. Zero snapshots are projected for skipped datasets.
			result.SkippedDatasets = append(result.SkippedDatasets, name)
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
	sort.Strings(result.SkippedDatasets)
	return nil
}

func normalizeSummaryCoverage(result *Summary, requested []domain.AccountDataset) error {
	if result == nil {
		return &RemoteError{Source: SourceSnaplink, Kind: ErrorInvalid, Code: "source.empty_summary"}
	}
	if len(result.Coverage.Datasets) == 0 && !result.Coverage.Explicit {
		result.Coverage.Datasets = append([]domain.AccountDataset(nil), requested...)
	}
	seen := make(map[domain.AccountDataset]struct{}, len(result.Coverage.Datasets))
	for _, dataset := range result.Coverage.Datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(result.Source) {
			return &RemoteError{Source: result.Source, Kind: ErrorInvalid, Code: "source.coverage_invalid"}
		}
		if _, duplicate := seen[dataset]; duplicate {
			return &RemoteError{Source: result.Source, Kind: ErrorInvalid, Code: "source.coverage_invalid"}
		}
		seen[dataset] = struct{}{}
	}
	if !result.Coverage.Explicit {
		// Legacy summaries did not declare reference coverage. Returned rows are
		// still safe to project, and the application will retain the legacy
		// source-wide revision fence for this response.
		result.Coverage.Sources = len(result.Sources) > 0
		result.Coverage.Memberships = len(result.Memberships) > 0
	}
	return nil
}

func sourceDatasets(source Source) []domain.AccountDataset {
	result := make([]domain.AccountDataset, 0)
	for _, descriptor := range domain.AccountDatasetRegistry() {
		if descriptor.Source == string(source) {
			result = append(result, descriptor.Name)
		}
	}
	return result
}

func markMissingRequestedDatasets(result *Summary, requested []domain.AccountDataset) {
	if result == nil || !result.Complete || len(requested) == 0 {
		return
	}
	returned := make(map[domain.AccountDataset]struct{}, len(result.Snapshots))
	for _, snapshot := range result.Snapshots {
		if snapshot != nil {
			returned[snapshot.Dataset] = struct{}{}
		}
	}
	for _, dataset := range requested {
		if _, ok := returned[dataset]; !ok {
			result.Complete = false
			return
		}
	}
}

func sourceReferences(source Source, region string, payload *summaryPayload) ([]*domain.AccountSource, error) {
	result := make([]*domain.AccountSource, 0, len(payload.Sources))
	for _, value := range payload.Sources {
		if strings.TrimSpace(value.SourceAccountID) == "" ||
			(strings.TrimSpace(value.ScopeType) == "") != (strings.TrimSpace(value.ScopeID) == "") {
			return nil, &RemoteError{Source: source, Kind: ErrorInvalid, Code: "source.reference_invalid"}
		}
		result = append(result, &domain.AccountSource{Source: source.String(), SourceRegion: region,
			SourceAccountID: strings.TrimSpace(value.SourceAccountID), ScopeType: strings.TrimSpace(value.ScopeType),
			ScopeID: strings.TrimSpace(value.ScopeID), Status: strings.TrimSpace(value.Status),
			Version: payload.Version, GeneratedAt: timePtr(payload.GeneratedAt), LastSyncedAt: timePtr(time.Now().UTC()),
			Data: RedactSummaryData(value.Data)})
	}
	return result, nil
}

func membershipReferences(source Source, region string, payload *summaryPayload) ([]*domain.AccountMembership, error) {
	result := make([]*domain.AccountMembership, 0, len(payload.Memberships))
	for _, value := range payload.Memberships {
		if strings.TrimSpace(value.ScopeType) == "" || strings.TrimSpace(value.ScopeID) == "" || strings.TrimSpace(value.SourceMemberID) == "" {
			return nil, &RemoteError{Source: source, Kind: ErrorInvalid, Code: "source.membership_invalid"}
		}
		result = append(result, &domain.AccountMembership{Source: source.String(), SourceRegion: region,
			ScopeType: strings.TrimSpace(value.ScopeType), ScopeID: strings.TrimSpace(value.ScopeID), SourceMemberID: strings.TrimSpace(value.SourceMemberID),
			Role: strings.TrimSpace(value.Role), Status: strings.TrimSpace(value.Status), Version: payload.Version, LastSyncedAt: timePtr(time.Now().UTC()),
			Data: RedactSummaryData(value.Data)})
	}
	return result, nil
}
