package connector

import "github.com/aero/aero-id/internal/domain"

// DatasetScopeMode describes the lifecycle boundary at which a dataset scope
// is being interpreted. Stored scopes are never expanded: an empty persisted
// scope is malformed rather than an implicit request for every dataset.
type DatasetScopeMode string

const (
	DatasetScopeSummary     DatasetScopeMode = "summary"
	DatasetScopeDestructive DatasetScopeMode = "destructive"
	DatasetScopeStored      DatasetScopeMode = "stored"
)

// ResolveDatasetScope parses and validates a connector dataset scope. It
// accepts only the representations used by transport and persisted JSON, and
// always returns a defensive copy.
func ResolveDatasetScope(source Source, raw any, mode DatasetScopeMode) ([]domain.AccountDataset, error) {
	if mode != DatasetScopeSummary && mode != DatasetScopeDestructive && mode != DatasetScopeStored {
		return nil, invalidDatasetScope(source)
	}
	values, ok := datasetScopeValues(raw)
	if !ok {
		return nil, invalidDatasetScope(source)
	}
	if len(values) == 0 {
		return resolveEmptyDatasetScope(source, mode)
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	result := make([]domain.AccountDataset, 0, len(values))
	for _, dataset := range values {
		descriptor, registered := domain.DatasetDescriptorOf(dataset)
		if !registered || descriptor.Source != string(source) {
			return nil, invalidDatasetScope(source)
		}
		if _, duplicate := seen[dataset]; duplicate {
			return nil, invalidDatasetScope(source)
		}
		seen[dataset] = struct{}{}
		result = append(result, dataset)
	}
	return result, nil
}

// datasetScopeValues deliberately does not coerce scalar values or mixed
// element types. JSON decoding uses []any, while Go callers commonly use one
// of the typed string slices.
//
//nolint:cyclop // each accepted wire representation has a distinct type case.
func datasetScopeValues(raw any) ([]domain.AccountDataset, bool) {
	limit := len(domain.AccountDatasetRegistry())
	switch values := raw.(type) {
	case []domain.AccountDataset:
		if values == nil {
			return []domain.AccountDataset{}, true
		}
		if len(values) > limit {
			return nil, false
		}
		return append([]domain.AccountDataset(nil), values...), true
	case []string:
		if values == nil {
			return []domain.AccountDataset{}, true
		}
		if len(values) > limit {
			return nil, false
		}
		result := make([]domain.AccountDataset, len(values))
		for i, value := range values {
			result[i] = domain.AccountDataset(value)
		}
		return result, true
	case []any:
		if values == nil {
			return []domain.AccountDataset{}, true
		}
		if len(values) > limit {
			return nil, false
		}
		result := make([]domain.AccountDataset, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			result[i] = domain.AccountDataset(text)
		}
		return result, true
	default:
		return nil, false
	}
}

func resolveEmptyDatasetScope(source Source, mode DatasetScopeMode) ([]domain.AccountDataset, error) {
	if source == SourceAeroVault && mode == DatasetScopeSummary {
		values := sourceDatasetScope(source)
		if len(values) > 0 {
			return values, nil
		}
		return nil, invalidDatasetScope(source)
	}
	if source == SourceSnaplink || source == SourceAeroIM || (source == SourceAeroVault && mode != DatasetScopeSummary) {
		return nil, invalidDatasetScope(source)
	}
	// Preserve the legacy empty behavior for sources that have not adopted a
	// strict dataset contract. Operation creation applies its stricter
	// destructive-mode rule before persistence.
	return []domain.AccountDataset{}, nil
}

func sourceDatasetScope(source Source) []domain.AccountDataset {
	registry := domain.AccountDatasetRegistry()
	result := make([]domain.AccountDataset, 0, len(registry))
	for _, descriptor := range registry {
		if descriptor.Source == string(source) {
			result = append(result, descriptor.Name)
		}
	}
	return result
}

func invalidDatasetScope(source Source) error {
	return &RemoteError{Source: source, Kind: ErrorInvalid, Code: "source.dataset_scope_invalid", Temporary: false}
}
