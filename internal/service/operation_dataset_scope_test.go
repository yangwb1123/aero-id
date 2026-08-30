package service

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func TestParseAeroIMOperationDatasetsIsStrictAndOrdered(t *testing.T) {
	valid := []domain.AccountDataset{"aero-im.notifications", "aero-im.profile"}
	got, err := parseAeroIMOperationDatasets(map[string]any{"datasets": []any{string(valid[0]), string(valid[1])}})
	if err != nil {
		t.Fatalf("parse valid scope: %v", err)
	}
	if len(got) != len(valid) || got[0] != valid[0] || got[1] != valid[1] {
		t.Fatalf("datasets = %#v, want %#v", got, valid)
	}

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{name: "missing", payload: map[string]any{}},
		{name: "nil", payload: map[string]any{"datasets": nil}},
		{name: "empty", payload: map[string]any{"datasets": []any{}}},
		{name: "non string", payload: map[string]any{"datasets": []any{"aero-im.profile", 1}}},
		{name: "unknown", payload: map[string]any{"datasets": []any{"aero-im.unknown"}}},
		{name: "wrong source", payload: map[string]any{"datasets": []any{"aero-vault.tenants"}}},
		{name: "duplicate", payload: map[string]any{"datasets": []any{"aero-im.profile", "aero-im.profile"}}},
		{name: "not exact", payload: map[string]any{
			"datasets": []any{" aero-im.profile"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAeroIMOperationDatasets(tc.payload)
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
				t.Fatalf("error = %v, want %s", err, invalidOperationDatasetScopeCode)
			}
		})
	}
}

func TestOperationDatasetsKeepsUntouchedLegacySemantics(t *testing.T) {
	got, err := operationDatasets(map[string]any{"datasets": []any{"aero-vault.tenants", "aero-im.profile", "unknown", 3}}, connector.SourceSnaplink)
	if err != nil {
		t.Fatalf("operationDatasets: %v", err)
	}
	want := []domain.AccountDataset{"aero-vault.tenants", "aero-im.profile"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("datasets = %#v, want %#v", got, want)
	}
}

func TestLegacyOperationRunnerExpandsEmptySnaplinkSummaryScope(t *testing.T) {
	want := []domain.AccountDataset{"snaplink.identity", "snaplink.tenants", "snaplink.permissions", "snaplink.security"}
	for _, action := range []string{"sync", "summary", "source.summary"} {
		for _, tc := range []struct {
			name    string
			payload map[string]any
		}{
			{name: "missing payload"},
			{name: "missing datasets", payload: map[string]any{}},
			{name: "nil datasets", payload: map[string]any{"datasets": nil}},
			{name: "empty json array", payload: map[string]any{"datasets": []any{}}},
			{name: "empty string array", payload: map[string]any{"datasets": []string{}}},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				client := &legacySnaplinkSummaryConnector{}
				application := &operationRunnerSummaryApplication{}
				runner := NewConnectorOperationRunner(&operationScopeAccountRepo{}, application, client)
				operation := &domain.Operation{ID: domain.ID{1}, AccountID: domain.ID{2}, TenantID: "tenant-a", ActorUID: "actor-1"}
				step := &domain.OperationStep{ID: domain.ID{3}, Source: string(connector.SourceSnaplink), Action: action, RequestPayload: tc.payload}
				if _, _, err := runner.Run(t.Context(), operation, step); err != nil {
					t.Fatalf("runner.Run: %v", err)
				}
				if client.calls != 1 {
					t.Fatalf("summary calls = %d, want 1", client.calls)
				}
				if !reflect.DeepEqual(client.request.Datasets, want) {
					t.Fatalf("connector datasets = %v, want %v", client.request.Datasets, want)
				}
				if !reflect.DeepEqual(application.request.RequestedDatasets, want) {
					t.Fatalf("application datasets = %v, want %v", application.request.RequestedDatasets, want)
				}
			})
		}
	}
}

type legacySnaplinkSummaryConnector struct {
	request connector.SummaryRequest
	calls   int
}

func (c *legacySnaplinkSummaryConnector) Source() connector.Source   { return connector.SourceSnaplink }
func (*legacySnaplinkSummaryConnector) Health(context.Context) error { return nil }
func (c *legacySnaplinkSummaryConnector) Summary(_ context.Context, request connector.SummaryRequest) (*connector.Summary, error) {
	c.calls++
	c.request = request
	return &connector.Summary{Source: connector.SourceSnaplink, SourceRegion: "local", Version: 1, Complete: true}, nil
}

type operationRunnerSummaryApplication struct {
	request domain.SummaryApplicationRequest
}

func (a *operationRunnerSummaryApplication) ApplySummary(_ context.Context, request domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	a.request = request
	return domain.SummaryApplicationResult{Outcome: domain.SummaryApplied}, nil
}

func TestDestructiveOperationScopeIsSourceBound(t *testing.T) {
	valid, err := parseOperationDatasetsForSource(
		map[string]any{"datasets": []any{"aero-vault.tenants", "aero-vault.buckets"}}, connector.SourceAeroVault)
	if err != nil || len(valid) != 2 {
		t.Fatalf("valid vault scope = %#v, err=%v", valid, err)
	}
	for _, payload := range []map[string]any{
		{}, {"datasets": []any{}}, {"datasets": []any{"aero-im.profile"}},
		{"datasets": []any{"aero-vault.tenants", "aero-vault.tenants"}},
	} {
		if _, err := parseOperationDatasetsForSource(payload, connector.SourceAeroVault); err == nil {
			t.Fatalf("payload %#v was accepted", payload)
		}
	}
}

func TestOperationRunStatusMapsInvalidDatasetScopeToFailed(t *testing.T) {
	status, code, message := operationRunStatus(invalidOperationDatasetScope())
	if status != domain.StepFailed || code != invalidOperationDatasetScopeCode || message == "" {
		t.Fatalf("status/code/message = %s/%s/%q, want failed/%s/non-empty", status, code, message, invalidOperationDatasetScopeCode)
	}
}

func TestOperationCreateValidatesAeroIMScopeBeforeIdempotencyLookup(t *testing.T) {
	repo := &operationAuthorizationRepo{}
	svc := NewOperationService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
	_, err := svc.Create(ctx, OperationInput{AccountID: domain.ID{1}, OperationType: "sync", IdempotencyKey: "op-1", Steps: []OperationStepInput{{
		StepKey: "step-1", Source: string(connector.SourceAeroIM), Action: "sync", RequestPayload: map[string]any{},
	}}})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
		t.Fatalf("error = %v, want %s", err, invalidOperationDatasetScopeCode)
	}
	if repo.findCalls != 0 {
		t.Fatalf("idempotency lookups = %d, want 0", repo.findCalls)
	}
}

type operationScopeCreateRepo struct {
	operationAuthorizationRepo
	created *domain.Operation
}

func (r *operationScopeCreateRepo) Create(_ context.Context, operation *domain.Operation) error {
	r.created = operation
	return nil
}

func TestVaultSummaryOperationNormalizesWithoutMutatingCaller(t *testing.T) {
	payload := map[string]any{"datasets": []any{"aero-vault.usage", "aero-vault.tenants"}, "keep": "value"}
	before := map[string]any{"datasets": []any{"aero-vault.usage", "aero-vault.tenants"}, "keep": "value"}
	repo := &operationScopeCreateRepo{}
	svc := NewOperationService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
	operation, err := svc.Create(ctx, OperationInput{AccountID: domain.ID{1}, OperationType: "sync", IdempotencyKey: "vault-summary-1",
		Steps: []OperationStepInput{{StepKey: "step-1", Source: string(connector.SourceAeroVault), Action: "summary", RequestPayload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, before) {
		t.Fatalf("caller payload changed: %#v", payload)
	}
	if repo.created != operation || len(operation.Steps) != 1 {
		t.Fatalf("created operation = %#v", operation)
	}
	got, ok := operation.Steps[0].RequestPayload["datasets"].([]string)
	want := []string{"aero-vault.usage", "aero-vault.tenants"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted datasets = %#v, want ordered strings %v", operation.Steps[0].RequestPayload["datasets"], want)
	}
}

func TestVaultSummaryOperationEmptyScopeExpandsBeforePersistence(t *testing.T) {
	for i, payload := range []map[string]any{
		nil,
		{"datasets": nil},
		{"datasets": []string{}},
	} {
		repo := &operationScopeCreateRepo{}
		svc := NewOperationService(repo)
		ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
		operation, err := svc.Create(ctx, OperationInput{AccountID: domain.ID{1}, OperationType: "sync", IdempotencyKey: "vault-summary-empty-" + strconv.Itoa(i),
			Steps: []OperationStepInput{{StepKey: "step-1", Source: string(connector.SourceAeroVault), Action: "summary", RequestPayload: payload}}})
		if err != nil {
			t.Fatalf("payload %#v: %v", payload, err)
		}
		got, ok := operation.Steps[0].RequestPayload["datasets"].([]string)
		want := []string{"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage"}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("payload %#v expanded datasets = %#v, want %v", payload, operation.Steps[0].RequestPayload["datasets"], want)
		}
	}
}

func TestInvalidVaultOperationScopeHasNoRepositorySideEffects(t *testing.T) {
	for _, payload := range []map[string]any{
		{"datasets": []any{"aero-vault.tenants", "aero-vault.tenants"}},
		{"datasets": []any{"aero-vault.tenants", "aero-im.profile"}},
		{"datasets": []any{"aero-vault.unknown"}},
	} {
		repo := &operationScopeCreateRepo{}
		svc := NewOperationService(repo)
		ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
		_, err := svc.Create(ctx, OperationInput{AccountID: domain.ID{1}, OperationType: "sync", IdempotencyKey: "invalid-vault",
			Steps: []OperationStepInput{{StepKey: "step-1", Source: string(connector.SourceAeroVault), Action: "summary", RequestPayload: payload}}})
		var appErr *apperr.AppError
		if !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
			t.Fatalf("payload %#v error = %v, want %s", payload, err, invalidOperationDatasetScopeCode)
		}
		if repo.findCalls != 0 || repo.created != nil {
			t.Fatalf("repository side effects for payload %#v: find=%d created=%v", payload, repo.findCalls, repo.created != nil)
		}
	}
}

type operationScopeConnector struct {
	summaries int
	erases    int
	statuses  int
}

func (c *operationScopeConnector) Source() connector.Source   { return connector.SourceAeroIM }
func (*operationScopeConnector) Health(context.Context) error { return nil }
func (c *operationScopeConnector) Summary(context.Context, connector.SummaryRequest) (*connector.Summary, error) {
	c.summaries++
	return &connector.Summary{Source: connector.SourceAeroIM}, nil
}
func (c *operationScopeConnector) Erase(context.Context, connector.EraseRequest) (*connector.CommandResult, error) {
	c.erases++
	return &connector.CommandResult{Status: "completed"}, nil
}

type operationVaultScopeConnector struct{ operationScopeConnector }

func (*operationVaultScopeConnector) Source() connector.Source { return connector.SourceAeroVault }
func (c *operationVaultScopeConnector) OperationStatus(context.Context, connector.OperationStatusRequest) (*connector.CommandResult, error) {
	c.statuses++
	return &connector.CommandResult{Status: "completed"}, nil
}

func TestOperationRunnerRejectsMalformedStoredAeroVaultScopesBeforeConnectorCalls(t *testing.T) {
	for _, action := range []string{"sync", "erase"} {
		t.Run(action, func(t *testing.T) {
			client := &operationVaultScopeConnector{}
			runner := NewConnectorOperationRunner(&operationScopeAccountRepo{}, nil, client)
			step := &domain.OperationStep{ID: domain.ID{3}, Source: string(connector.SourceAeroVault), Action: action,
				RequestPayload: map[string]any{"datasets": []any{}}}
			_, _, err := runner.Run(t.Context(), &domain.Operation{ID: domain.ID{1}, AccountID: domain.ID{2}}, step)
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
				t.Fatalf("error = %v, want %s", err, invalidOperationDatasetScopeCode)
			}
			if client.summaries != 0 || client.erases != 0 {
				t.Fatalf("connector calls = summary:%d erase:%d, want 0/0", client.summaries, client.erases)
			}
		})
	}
}

func TestOperationRunnerReconcileRejectsMalformedStoredAeroVaultScopeBeforeStatus(t *testing.T) {
	client := &operationVaultScopeConnector{}
	runner := NewConnectorOperationRunner(&operationScopeAccountRepo{}, nil, client)
	reconciler := runner.(OperationOutcomeReconciler)
	status, _, _, err := reconciler.Reconcile(t.Context(), &domain.Operation{ID: domain.ID{1}, AccountID: domain.ID{2}},
		&domain.OperationStep{ID: domain.ID{3}, Source: string(connector.SourceAeroVault), Action: "erase",
			RequestPayload: map[string]any{"datasets": []any{"aero-vault.tenants", "aero-vault.tenants"}}})
	var appErr *apperr.AppError
	if status != domain.StepFailed || !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
		t.Fatalf("reconcile = %s/%v, want failed/%s", status, err, invalidOperationDatasetScopeCode)
	}
	if client.statuses != 0 {
		t.Fatalf("operation status calls = %d, want 0", client.statuses)
	}
}

func TestOperationRunnerRejectsMalformedStoredAeroIMScopesBeforeConnectorCalls(t *testing.T) {
	for _, action := range []string{"sync", "erase"} {
		t.Run(action, func(t *testing.T) {
			client := &operationScopeConnector{}
			runner := NewConnectorOperationRunner(&operationScopeAccountRepo{}, nil, client)
			_, _, err := runner.Run(t.Context(), &domain.Operation{ID: domain.ID{1}, AccountID: domain.ID{2}}, &domain.OperationStep{
				ID: domain.ID{3}, Source: string(connector.SourceAeroIM), Action: action, RequestPayload: map[string]any{},
			})
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != invalidOperationDatasetScopeCode {
				t.Fatalf("error = %v, want %s", err, invalidOperationDatasetScopeCode)
			}
			if client.summaries != 0 || client.erases != 0 {
				t.Fatalf("connector calls = summary:%d erase:%d, want 0/0", client.summaries, client.erases)
			}
		})
	}
}

type operationScopeAccountRepo struct{ repository.AccountRepository }

func (*operationScopeAccountRepo) GetByID(_ context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	return &domain.Account{ID: id, TenantID: tenantID, CanonicalUID: "canonical-1", HomeRegion: "local", Status: domain.AccountActive}, nil
}
