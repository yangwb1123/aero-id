package service

import (
	"reflect"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestDescribeOperationStepScopePreservesOnlyExplicitRegisteredNames(t *testing.T) {
	for _, descriptor := range domain.AccountDatasetRegistry() {
		t.Run(string(descriptor.Name), func(t *testing.T) {
			for _, raw := range []any{[]any{string(descriptor.Name)}, []string{string(descriptor.Name)}, []domain.AccountDataset{descriptor.Name}} {
				step := &domain.OperationStep{Source: descriptor.Source, RequestPayload: map[string]any{"datasets": raw, "token": "not-copied"}}
				got := DescribeOperationStepScope(step)
				if got.Status != "explicit" || !reflect.DeepEqual(got.Datasets, []string{string(descriptor.Name)}) {
					t.Fatalf("scope = %+v", got)
				}
				got.Datasets[0] = "changed-copy"
				if DescribeOperationStepScope(step).Datasets[0] != string(descriptor.Name) || step.RequestPayload["token"] != "not-copied" {
					t.Fatal("metadata must be a defensive read projection, never execution input mutation")
				}
			}
		})
	}
}

func TestDescribeOperationStepScopeRejectsMalformedWithoutExpansion(t *testing.T) {
	for _, raw := range []any{
		nil, []any(nil), []string(nil), []domain.AccountDataset(nil), []any{}, "aero-vault.usage", 1, true,
		[]any{"aero-vault.usage", nil}, []any{"aero-vault.usage", "aero-im.workspaces"},
		[]string{"aero-vault.usage", "aero-vault.usage"}, []string{"aero-vault.private"},
		[]string{" aero-vault.usage"}, make([]string, 12),
	} {
		step := &domain.OperationStep{Source: "aero-vault", Action: "summary", RequestPayload: map[string]any{"datasets": raw}}
		got := DescribeOperationStepScope(step)
		if got.Status != "invalid" || got.Datasets == nil || len(got.Datasets) != 0 {
			t.Fatalf("scope for %T = %+v, must be invalid and contain no unapproved names", raw, got)
		}
	}
	for _, step := range []*domain.OperationStep{nil, {Source: "aero-vault", Action: "summary"}} {
		got := DescribeOperationStepScope(step)
		if got.Status != "missing" || got.Datasets == nil || len(got.Datasets) != 0 {
			t.Fatalf("missing scope = %+v; must not expand Vault/Snaplink compatibility defaults", got)
		}
	}
	for _, source := range []string{"unknown", " aero-vault", "aero-vault "} {
		got := DescribeOperationStepScope(&domain.OperationStep{Source: source, RequestPayload: map[string]any{"datasets": []string{"aero-vault.usage"}}})
		if got.Status != "invalid" || len(got.Datasets) != 0 {
			t.Fatal("do not normalize a stored source to lend it a scope")
		}
	}
}
