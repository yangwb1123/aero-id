package service

import (
	"context"
	"errors"
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

func TestOperationDatasetsKeepsNonAeroIMLegacySemantics(t *testing.T) {
	got, err := operationDatasets(map[string]any{"datasets": []any{"aero-vault.tenants", "aero-im.profile", "unknown", 3}}, connector.SourceAeroVault)
	if err != nil {
		t.Fatalf("operationDatasets: %v", err)
	}
	want := []domain.AccountDataset{"aero-vault.tenants", "aero-im.profile"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("datasets = %#v, want %#v", got, want)
	}
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

type operationScopeConnector struct {
	summaries int
	erases    int
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
