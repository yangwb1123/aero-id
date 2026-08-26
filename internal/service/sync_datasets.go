package service

import (
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func datasetsFromRequest(request map[string]any) []domain.AccountDataset {
	values, err := parsePersistedDatasets(request, false)
	if err != nil {
		return nil
	}
	return values
}

func parsePersistedDatasets(request map[string]any, requireNonEmpty bool) ([]domain.AccountDataset, error) {
	return parsePersistedDatasetsWithError(request, requireNonEmpty, invalidSyncDatasetScope)
}

func parsePersistedDatasetsWithError(request map[string]any, requireNonEmpty bool,
	invalid func() error) ([]domain.AccountDataset, error) {
	raw, present := request["datasets"]
	if !present || raw == nil {
		if requireNonEmpty {
			return nil, invalid()
		}
		return nil, nil
	}
	values, ok := operationDatasetValues(raw)
	if !ok {
		return nil, invalid()
	}
	if requireNonEmpty && len(values) == 0 {
		return nil, invalid()
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	for _, dataset := range values {
		if _, valid := domain.DatasetDescriptorOf(dataset); !valid {
			return nil, invalid()
		}
		if _, duplicate := seen[dataset]; duplicate {
			return nil, invalid()
		}
		seen[dataset] = struct{}{}
	}
	return values, nil
}

func parsePersistedSyncDatasets(request map[string]any, requireNonEmpty bool) ([]domain.AccountDataset, error) {
	return parsePersistedDatasetsWithError(request, requireNonEmpty, invalidSyncJobDatasetScope)
}

func validateDestructiveDatasetInput(values []domain.AccountDataset) error {
	if len(values) == 0 {
		return invalidSyncDatasetScope()
	}
	_, err := parsePersistedDatasets(map[string]any{"datasets": values}, true)
	return err
}

func normalizeSyncJobDatasets(jobType domain.SyncJobType, values []domain.AccountDataset) ([]domain.AccountDataset, error) {
	if len(values) == 0 {
		return allDatasets(), nil
	}
	if jobType == domain.SyncJobErase {
		if err := validateDestructiveDatasetInput(values); err != nil {
			return nil, err
		}
		return append([]domain.AccountDataset(nil), values...), nil
	}
	return parsePersistedSyncDatasets(map[string]any{"datasets": values}, true)
}

func invalidSyncDatasetScope() error {
	return apperr.BadRequest("erase.invalid_dataset_scope", "erase job requires a non-empty valid dataset scope")
}

func invalidSyncJobDatasetScope() error {
	return apperr.BadRequest("sync.invalid_dataset_scope", "sync job requires a non-empty valid dataset scope")
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

func validatedDatasetsBySource(datasets []domain.AccountDataset) (map[connector.Source][]domain.AccountDataset, error) {
	result := datasetsBySource(datasets)
	count := 0
	for _, requested := range result {
		count += len(requested)
	}
	if count != len(datasets) {
		return nil, invalidSyncDatasetScope()
	}
	for source, requested := range result {
		resolved, err := connector.ResolveDatasetScope(source, requested, connector.DatasetScopeStored)
		if err != nil {
			return nil, invalidSyncDatasetScope()
		}
		result[source] = resolved
	}
	return result, nil
}

// validatePersistedSyncJobScope is used before service-level account loading
// when a manually retried job is still pending/unknown. A malformed historical
// row must be made durably failed, not blocked by an unrelated account lookup.
func validatePersistedSyncJobScope(job *domain.SyncJob) error {
	if job == nil {
		return invalidSyncDatasetScope()
	}
	switch job.JobType {
	case domain.SyncJobSync, domain.SyncJobReconcile:
		datasets, err := parsePersistedSyncDatasets(job.Request, true)
		if err != nil {
			return err
		}
		_, err = validatedDatasetsBySource(datasets)
		return err
	case domain.SyncJobErase:
		datasets, err := parsePersistedDatasets(job.Request, true)
		if err != nil {
			return err
		}
		_, err = validatedDatasetsBySource(datasets)
		return err
	default:
		return nil
	}
}

func syncJobScopeFailure(jobType domain.SyncJobType) (string, string) {
	if jobType == domain.SyncJobErase {
		return "erase.invalid_dataset_scope", "erase job requires a non-empty valid dataset scope"
	}
	return "sync.invalid_dataset_scope", "sync job requires a non-empty valid dataset scope"
}

func workerOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "inline"
	}
	return value
}
