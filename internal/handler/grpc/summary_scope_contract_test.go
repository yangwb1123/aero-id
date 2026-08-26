package grpc

import (
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestOperationToProtoSupportsNormalizedDatasetArray(t *testing.T) {
	operation := &domain.Operation{
		RequestPayload: map[string]any{"datasets": []string{"aero-vault.tenants", "aero-vault.usage"}},
		Steps: []*domain.OperationStep{{
			RequestPayload: map[string]any{"datasets": []string{"aero-vault.tenants", "aero-vault.usage"}},
			Result:         map[string]any{},
		}},
		Result: map[string]any{"datasets": []string{"aero-vault.tenants"}},
	}
	converted, err := operationToProto(operation)
	if err != nil {
		t.Fatalf("operationToProto() error = %v", err)
	}
	assertStringList(t, converted.Steps[0].RequestPayload.AsMap()["datasets"],
		[]string{"aero-vault.tenants", "aero-vault.usage"})
	assertStringList(t, converted.RequestPayload.AsMap()["datasets"],
		[]string{"aero-vault.tenants", "aero-vault.usage"})
}

func TestSyncJobToProtoSupportsExpandedDatasetArray(t *testing.T) {
	job := &domain.SyncJob{
		Request: map[string]any{"datasets": []domain.AccountDataset{
			"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage",
		}},
		Result: map[string]any{"datasets": []string{"aero-vault.tenants"}},
	}
	converted, err := syncJobToProto(job)
	if err != nil {
		t.Fatalf("syncJobToProto() error = %v", err)
	}
	assertStringList(t, converted.Request.AsMap()["datasets"],
		[]string{"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage"})
}

func TestStructFromMapPreservesDatasetOrderAcrossWireShapes(t *testing.T) {
	values := map[string]any{
		"domain":  []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"},
		"strings": []string{"aero-vault.buckets", "aero-vault.tenants"},
	}
	converted, err := structFromMap(values)
	if err != nil {
		t.Fatalf("structFromMap() error = %v", err)
	}
	got := converted.AsMap()
	assertStringList(t, got["domain"], []string{"aero-vault.usage", "aero-vault.tenants"})
	assertStringList(t, got["strings"], []string{"aero-vault.buckets", "aero-vault.tenants"})
}

func assertStringList(t *testing.T, value any, want []string) {
	t.Helper()
	got, ok := value.([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("array = %#v, want %v", value, want)
	}
	for i, item := range got {
		if item != want[i] {
			t.Fatalf("array = %#v, want %v", value, want)
		}
	}
}
