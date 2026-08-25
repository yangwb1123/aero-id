package service

import (
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func datasetsFromRequest(request map[string]any) []domain.AccountDataset {
	raw := request["datasets"]
	result := make([]domain.AccountDataset, 0)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				result = append(result, domain.AccountDataset(name))
			}
		}
	case []string:
		for _, name := range values {
			result = append(result, domain.AccountDataset(name))
		}
	case []domain.AccountDataset:
		result = append(result, values...)
	}
	return result
}

func parsePersistedDatasets(request map[string]any, requireNonEmpty bool) ([]domain.AccountDataset, error) {
	raw, present := request["datasets"]
	if !present || raw == nil {
		if requireNonEmpty {
			return nil, invalidSyncDatasetScope()
		}
		return nil, nil
	}
	values, ok := operationDatasetValues(raw)
	if !ok {
		return nil, invalidSyncDatasetScope()
	}
	if requireNonEmpty && len(values) == 0 {
		return nil, invalidSyncDatasetScope()
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	for _, dataset := range values {
		if _, valid := domain.DatasetDescriptorOf(dataset); !valid {
			return nil, invalidSyncDatasetScope()
		}
		if _, duplicate := seen[dataset]; duplicate {
			return nil, invalidSyncDatasetScope()
		}
		seen[dataset] = struct{}{}
	}
	return values, nil
}

func validateDestructiveDatasetInput(values []domain.AccountDataset) error {
	if len(values) == 0 {
		return invalidSyncDatasetScope()
	}
	_, err := parsePersistedDatasets(map[string]any{"datasets": values}, true)
	return err
}

func invalidSyncDatasetScope() error {
	return apperr.BadRequest("erase.invalid_dataset_scope", "erase job requires a non-empty valid dataset scope")
}

func datasetsBySource(datasets []domain.AccountDataset) map[connector.Source][]domain.AccountDataset {
	result := make(map[connector.Source][]domain.AccountDataset)
	for _, dataset := range datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok {
			continue
		}
		source := connector.Source(descriptor.Source)
		result[source] = append(result[source], dataset)
	}
	return result
}

func workerOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "inline"
	}
	return value
}
