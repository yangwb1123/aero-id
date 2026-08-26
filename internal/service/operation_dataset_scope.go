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

func operationDatasetScopeMode(source, action string) (connector.DatasetScopeMode, bool) {
	source = strings.TrimSpace(source)
	action = strings.ToLower(strings.TrimSpace(action))
	if operationActionIsDestructive(action) {
		return connector.DatasetScopeDestructive, true
	}
	if switchOperationAction(action, "sync", "summary", "source.summary") &&
		(source == string(connector.SourceAeroIM) || source == string(connector.SourceAeroVault)) {
		return connector.DatasetScopeSummary, true
	}
	return "", false
}

func operationActionRequiresDatasetScope(source, action string) bool {
	_, applicable := operationDatasetScopeMode(source, action)
	return applicable
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

// operationDatasetValues parses persisted JSON representations without
// allocating for arrays larger than the allow-listed registry.
//
//nolint:cyclop // each accepted persisted representation has a distinct type case.
func operationDatasetValues(raw any) ([]domain.AccountDataset, bool) {
	limit := len(domain.AccountDatasetRegistry())
	switch items := raw.(type) {
	case []any:
		if items == nil || len(items) > limit {
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
		if items == nil || len(items) > limit {
			return nil, false
		}
		values := make([]domain.AccountDataset, 0, len(items))
		for _, item := range items {
			values = append(values, domain.AccountDataset(item))
		}
		return values, true
	case []domain.AccountDataset:
		if items == nil || len(items) > limit {
			return nil, false
		}
		return append([]domain.AccountDataset(nil), items...), true
	default:
		return nil, false
	}
}

// resolveOperationDatasetScope maps connector errors to the stable service
// error and never expands an empty stored scope. Missing datasets are passed
// as a typed empty slice so Vault summary mode can perform its explicit local
// expansion.
func resolveOperationDatasetScope(payload map[string]any, source connector.Source,
	mode connector.DatasetScopeMode) ([]domain.AccountDataset, error) {
	raw, present := payload["datasets"]
	if !present || raw == nil {
		raw = []domain.AccountDataset(nil)
	}
	values, err := connector.ResolveDatasetScope(source, raw, mode)
	if err != nil || len(values) == 0 {
		return nil, invalidOperationDatasetScope()
	}
	return values, nil
}

func parseAeroIMOperationDatasets(payload map[string]any) ([]domain.AccountDataset, error) {
	return resolveOperationDatasetScope(payload, connector.SourceAeroIM, connector.DatasetScopeStored)
}

func parseOperationDatasetsForSource(payload map[string]any, source connector.Source) ([]domain.AccountDataset, error) {
	return resolveOperationDatasetScope(payload, source, connector.DatasetScopeDestructive)
}

func validateOperationStepDatasetScope(step OperationStepInput) error {
	if !operationActionRequiresDatasetScope(step.Source, step.Action) {
		return nil
	}
	mode, applicable := operationDatasetScopeMode(step.Source, step.Action)
	if !applicable {
		return nil
	}
	_, err := resolveOperationDatasetScope(step.RequestPayload,
		connector.Source(strings.TrimSpace(step.Source)), mode)
	return err
}

// normalizeOperationDatasetScopes validates and canonicalizes applicable steps
// before idempotency lookup. Maps are copied before datasets is replaced.
func normalizeOperationDatasetScopes(steps []OperationStepInput) ([]OperationStepInput, error) {
	result := make([]OperationStepInput, len(steps))
	copy(result, steps)
	for i := range result {
		mode, applicable := operationDatasetScopeMode(result[i].Source, result[i].Action)
		if !applicable {
			continue
		}
		values, err := resolveOperationDatasetScope(result[i].RequestPayload,
			connector.Source(strings.TrimSpace(result[i].Source)), mode)
		if err != nil {
			return nil, err
		}
		payload := make(map[string]any, len(result[i].RequestPayload)+1)
		for key, value := range result[i].RequestPayload {
			payload[key] = value
		}
		payload["datasets"] = datasetNames(values)
		result[i].RequestPayload = payload
	}
	return result, nil
}

//nolint:unused // retained for package callers that need validation without normalization.
func validateOperationDatasetScopes(steps []OperationStepInput) error {
	_, err := normalizeOperationDatasetScopes(steps)
	return err
}
