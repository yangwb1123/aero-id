package service

import (
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

const (
	invalidOperationDatasetScopeCode    = "operation.invalid_dataset_scope"
	invalidOperationDatasetScopeMessage = "source operation step requires a non-empty valid dataset scope"
)

func parseAeroIMOperationDatasets(payload map[string]any) ([]domain.AccountDataset, error) {
	raw, present := payload["datasets"]
	if !present || raw == nil {
		return nil, invalidOperationDatasetScope()
	}

	values, ok := operationDatasetValues(raw)
	if !ok {
		return nil, invalidOperationDatasetScope()
	}
	if err := validateAeroIMOperationDatasets(values); err != nil {
		return nil, err
	}
	return values, nil
}

//nolint:cyclop // type-switch parsing rejects every non-canonical representation.
func operationDatasetValues(raw any) ([]domain.AccountDataset, bool) {
	switch items := raw.(type) {
	case []any:
		if items == nil {
			return nil, false
		}
		values := make([]domain.AccountDataset, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, domain.AccountDataset(text))
		}
		return values, true
	case []string:
		if items == nil {
			return nil, false
		}
		values := make([]domain.AccountDataset, 0, len(items))
		for _, item := range items {
			values = append(values, domain.AccountDataset(item))
		}
		return values, true
	case []domain.AccountDataset:
		if items == nil {
			return nil, false
		}
		return append([]domain.AccountDataset(nil), items...), true
	default:
		return nil, false
	}
}

func validateAeroIMOperationDatasets(values []domain.AccountDataset) error {
	if len(values) == 0 {
		return invalidOperationDatasetScope()
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	for _, dataset := range values {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(connector.SourceAeroIM) {
			return invalidOperationDatasetScope()
		}
		if _, exists := seen[dataset]; exists {
			return invalidOperationDatasetScope()
		}
		seen[dataset] = struct{}{}
	}
	return nil
}

func invalidOperationDatasetScope() error {
	return apperr.BadRequest(invalidOperationDatasetScopeCode, invalidOperationDatasetScopeMessage)
}

func datasetNames(values []domain.AccountDataset) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}

func operationActionRequiresDatasetScope(source, action string) bool {
	if operationActionIsDestructive(action) {
		return true
	}
	return strings.TrimSpace(source) == string(connector.SourceAeroIM) &&
		switchOperationAction(action, "sync", "summary", "source.summary")
}

func operationActionIsDestructive(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "erase", "delete", "source.erase":
		return true
	default:
		return false
	}
}

func switchOperationAction(action string, values ...string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	for _, value := range values {
		if action == value {
			return true
		}
	}
	return false
}

func validateOperationStepDatasetScope(step OperationStepInput) error {
	if !operationActionRequiresDatasetScope(step.Source, step.Action) {
		return nil
	}
	source := connector.Source(strings.TrimSpace(step.Source))
	if source == connector.SourceAeroIM {
		_, err := parseAeroIMOperationDatasets(step.RequestPayload)
		return err
	}
	_, err := parseOperationDatasetsForSource(step.RequestPayload, source)
	return err
}

// parseOperationDatasetsForSource strictly validates destructive scopes for
// every source. Empty or cross-source erase payloads must not rely on a
// connector's legacy empty-means-all behavior.
func parseOperationDatasetsForSource(payload map[string]any, source connector.Source) ([]domain.AccountDataset, error) {
	raw, present := payload["datasets"]
	if !present || raw == nil {
		return nil, invalidOperationDatasetScope()
	}
	values, ok := operationDatasetValues(raw)
	if !ok || len(values) == 0 {
		return nil, invalidOperationDatasetScope()
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	for _, dataset := range values {
		descriptor, registered := domain.DatasetDescriptorOf(dataset)
		if !registered || descriptor.Source != string(source) {
			return nil, invalidOperationDatasetScope()
		}
		if _, duplicate := seen[dataset]; duplicate {
			return nil, invalidOperationDatasetScope()
		}
		seen[dataset] = struct{}{}
	}
	return values, nil
}

func validateOperationDatasetScopes(steps []OperationStepInput) error {
	for _, step := range steps {
		if err := validateOperationStepDatasetScope(step); err != nil {
			return err
		}
	}
	return nil
}
