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
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

func (c *JSONSummaryConnector) newSummaryRequest(ctx context.Context, req SummaryRequest) (*http.Request, error) {
	req = c.applyTestTargetRequest(req)
	if err := c.validateVaultRequestTenant(req.TenantID); err != nil {
		return nil, err
	}
	var err error
	req.Region, err = c.vaultRequestRegion(req.Region)
	if err != nil {
		return nil, err
	}
	if err := c.validateTargetSummaryRequest(req); err != nil {
		return nil, err
	}
	endpoint, err := c.summaryURL(req)
	if err != nil {
		return nil, err
	}
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
	if err := c.addTargetAssertion(request, req); err != nil {
		return nil, err
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, nil
}

func (c *JSONSummaryConnector) applyTestTargetRequest(req SummaryRequest) SummaryRequest {
	if c.source == SourceAeroIM && req.trustedTarget == nil && c.testTargetRequest != nil {
		return c.testTargetRequest(req)
	}
	return req
}

func (c *JSONSummaryConnector) validateTargetSummaryRequest(req SummaryRequest) error {
	if c.source != SourceAeroIM {
		return nil
	}
	if canonicalTargetDatasetOrder(req.Datasets) {
		return nil
	}
	return &RemoteError{Source: c.source, Kind: ErrorInvalid,
		Code: "source.target_assertion_dataset_order"}
}

func (c *JSONSummaryConnector) addTargetAssertion(request *http.Request, req SummaryRequest) error {
	if c.source != SourceAeroIM {
		return nil
	}
	if c.targetAssertionSigner == nil || req.trustedTarget == nil || req.trustedTarget.source != c.source ||
		!trustedTargetMatchesRequest(req.trustedTarget, req) {
		return &RemoteError{Source: c.source, Kind: ErrorUnavailable,
			Code: "source.target_assertion_not_configured"}
	}
	assertion, err := c.targetAssertionSigner.sign(req.trustedTarget)
	if err != nil {
		return &RemoteError{Source: c.source, Kind: ErrorInvalid,
			Code: "source.target_assertion_failed", Cause: redactedRemoteCause(err)}
	}
	request.Header.Set(accountSummaryTargetAssertionHeader, assertion)
	return nil
}

func (c *JSONSummaryConnector) summaryURL(req SummaryRequest) (*url.URL, error) {
	if c.source == SourceAeroIM && c.targetAssertionSigner != nil && c.cfg.SummaryPath != accountSummaryCanonicalPath {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid,
			Code: "source.target_assertion_path"}
	}
	endpoint, err := c.sourceEndpointURL()
	if err != nil || !appendSourcePath(endpoint, c.cfg.SummaryPath) {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint"}
	}
	query := endpoint.Query()
	query.Set("account_id", req.AccountID.String())
	if req.Region != "" {
		query.Set("region", req.Region)
	}
	// Aero IM and the deployed Vault summary protocol do not publish a
	// reconciliation query flag. Reconcile is an aero-id workflow mode, not a
	// source authorization input; only the legacy Snaplink seam carries it.
	if req.Reconcile && c.source == SourceSnaplink {
		query.Set("reconcile", "true")
	}
	for _, dataset := range req.Datasets {
		query.Add("dataset", string(dataset))
	}
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}

func (c *JSONSummaryConnector) sourceEndpointURL() (*url.URL, error) {
	if c.endpointURL != nil {
		return cloneURL(c.endpointURL), nil
	}
	return sourceauth.ParseEndpoint(c.cfg.Endpoint)
}

func appendSourcePath(endpoint *url.URL, path string) bool {
	if endpoint == nil || !sourceauth.ValidAbsolutePath(path) || path == "" {
		return false
	}
	pathURL, err := url.ParseRequestURI(path)
	if err != nil {
		return false
	}
	baseEscaped := strings.TrimRight(endpoint.EscapedPath(), "/")
	pathEscaped := strings.TrimLeft(pathURL.EscapedPath(), "/")
	joinedEscaped := baseEscaped + "/" + pathEscaped
	decoded, err := url.PathUnescape(joinedEscaped)
	if err != nil || !strings.HasPrefix(decoded, "/") {
		return false
	}
	endpoint.Path = decoded
	endpoint.RawPath = ""
	if (&url.URL{Path: decoded}).EscapedPath() != joinedEscaped {
		endpoint.RawPath = joinedEscaped
	}
	return true
}

func canonicalTargetDatasetOrder(datasets []domain.AccountDataset) bool {
	if len(datasets) == 0 {
		return false
	}
	for index := 1; index < len(datasets); index++ {
		if string(datasets[index-1]) >= string(datasets[index]) {
			return false
		}
	}
	return true
}

func setBearer(request *http.Request, token string) {
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *JSONSummaryConnector) newEraseRequest(ctx context.Context, req EraseRequest) (*http.Request, []byte, error) {
	if err := c.validateVaultRequestTenant(req.TenantID); err != nil {
		return nil, nil, err
	}
	var err error
	req.Region, err = c.vaultRequestRegion(req.Region)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Datasets) == 0 && (c.source == SourceSnaplink || c.source == SourceAeroIM || c.source == SourceAeroVault) {
		return nil, nil, invalidDatasetScope(c.source)
	}
	endpoint, err := c.eraseEndpoint(req.Region)
	if err != nil {
		return nil, nil, err
	}
	body, err := marshalEraseRequest(req)
	if err != nil {
		return nil, nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request",
			Cause: redactedRemoteCause(err)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.request",
			Cause: redactedRemoteCause(err)}
	}
	c.setEraseHeaders(request, req)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	return request, body, nil
}

func (c *JSONSummaryConnector) eraseEndpoint(region string) (*url.URL, error) {
	endpoint, err := c.sourceEndpointURL()
	if err != nil || !appendSourcePath(endpoint, c.cfg.ErasePath) {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.endpoint"}
	}
	if region != "" {
		query := endpoint.Query()
		query.Set("region", region)
		endpoint.RawQuery = query.Encode()
	}
	return endpoint, nil
}

func marshalEraseRequest(req EraseRequest) ([]byte, error) {
	return json.Marshal(map[string]any{"account_id": req.AccountID.String(), "canonical_uid": req.CanonicalUID,
		"datasets": req.Datasets, "idempotency_key": req.IdempotencyKey, "operation_id": req.OperationID})
}

func (c *JSONSummaryConnector) setEraseHeaders(request *http.Request, req EraseRequest) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
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
}

func setTenantContextHeaders(headers http.Header, source Source, tenantID string) {
	headers.Set("X-Aero-Tenant-ID", tenantID)
	// Aero Vault's HTTP middleware resolves the active tenant from this header;
	// X-Aero-Tenant-ID remains the asserted account-summary consistency field.
	if source == SourceAeroVault {
		headers.Set("X-Aero-Tenant", tenantID)
	}
}

func (c *JSONSummaryConnector) decodeEraseResult(ctx context.Context, body io.Reader) (*CommandResult, error) {
	data, err := c.readCommandResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return c.emptyEraseResult()
	}
	payload, err := c.parseErasePayload(data)
	if err != nil {
		return nil, err
	}
	return c.finishErasePayload(payload)
}

func (c *JSONSummaryConnector) emptyEraseResult() (*CommandResult, error) {
	if c.source == SourceAeroVault {
		return nil, unknownCommandOutcome(c.source, "source.response_empty")
	}
	return &CommandResult{Status: "accepted"}, nil
}

func (c *JSONSummaryConnector) parseErasePayload(data []byte) (commandResultPayload, error) {
	var payload commandResultPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		if c.source == SourceAeroVault {
			return commandResultPayload{}, unknownCommandOutcome(c.source, "source.response_invalid")
		}
		return commandResultPayload{}, &RemoteError{Source: c.source, Kind: ErrorInvalid,
			Code: "source.response_invalid", Cause: redactedRemoteCause(err)}
	}
	return payload, nil
}

func (c *JSONSummaryConnector) finishErasePayload(payload commandResultPayload) (*CommandResult, error) {
	if payload.SourceOperationID == "" && c.source != SourceSnaplink {
		payload.SourceOperationID = payload.OperationID
	}
	if payload.Status == "" {
		if c.source == SourceAeroVault {
			return nil, unknownCommandOutcome(c.source, "source.operation_status_missing")
		}
		if c.source == SourceSnaplink {
			return commandResultFromPayload(payload), &RemoteError{Source: c.source, Kind: ErrorInvalid,
				Code: "source.operation_status_missing"}
		}
		payload.Status = "accepted"
	}
	return commandResultFromPayload(payload), nil
}

func commandResultFromPayload(payload commandResultPayload) *CommandResult {
	return &CommandResult{OperationID: payload.OperationID, SourceOperationID: payload.SourceOperationID,
		Status: strings.ToLower(payload.Status), Result: RedactSummaryData(payload.Result)}
}

func (c *JSONSummaryConnector) commandResponseReadError(ctx context.Context, code string, err error) *RemoteError {
	kind := ErrorUnavailable
	if requestTimedOut(ctx, err) {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: code, Temporary: true, Cause: redactedRemoteCause(err)}
}

func unknownCommandOutcome(source Source, code string) *RemoteError {
	return &RemoteError{Source: source, Kind: ErrorUnknown, Code: code, Temporary: true}
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
			return &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.dataset_invalid", Cause: redactedRemoteCause(err)}
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

func requestedSummaryDatasets(source Source, requested []domain.AccountDataset) []domain.AccountDataset {
	if source == SourceAeroVault && len(requested) == 0 {
		return sourceDatasetScope(source)
	}
	return append([]domain.AccountDataset(nil), requested...)
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
