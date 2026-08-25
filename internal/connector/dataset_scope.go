package connector

import "github.com/aero/aero-id/internal/domain"

func validateDatasetScope(source Source, datasets []domain.AccountDataset) error {
	if source != SourceAeroIM {
		return nil
	}
	if len(datasets) == 0 {
		return invalidDatasetScope(source)
	}
	seen := make(map[domain.AccountDataset]struct{}, len(datasets))
	for _, dataset := range datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(source) {
			return invalidDatasetScope(source)
		}
		if _, exists := seen[dataset]; exists {
			return invalidDatasetScope(source)
		}
		seen[dataset] = struct{}{}
	}
	return nil
}

func invalidDatasetScope(source Source) error {
	return &RemoteError{Source: source, Kind: ErrorInvalid, Code: "source.dataset_scope_invalid", Temporary: false}
}
