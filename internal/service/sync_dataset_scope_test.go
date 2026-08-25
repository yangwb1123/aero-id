package service

import (
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func TestParsePersistedDatasetsRejectsMalformedEraseScope(t *testing.T) {
	cases := []map[string]any{
		{}, {"datasets": nil}, {"datasets": []any{}},
		{"datasets": []any{"aero-im.profile", 7}},
		{"datasets": []any{"aero-vault.tenants", "aero-vault.tenants"}},
		{"datasets": []any{"unknown.dataset"}},
	}
	for _, request := range cases {
		if _, err := parsePersistedDatasets(request, true); err == nil {
			t.Fatalf("request %#v was accepted", request)
		} else {
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != "erase.invalid_dataset_scope" {
				t.Fatalf("request %#v error = %v, want erase.invalid_dataset_scope", request, err)
			}
		}
	}
}

func TestParsePersistedDatasetsPreservesExactOrder(t *testing.T) {
	want := []domain.AccountDataset{"aero-vault.usage", "aero-im.activity_summary"}
	got, err := parsePersistedDatasets(map[string]any{"datasets": []any{string(want[0]), string(want[1])}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("datasets = %#v, want %#v", got, want)
	}
}
