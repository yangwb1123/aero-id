package repository

import (
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

func validateSummaryApplicationRequest(request *domain.SummaryApplicationRequest) error {
	if request.AccountID == (domain.ID{}) {
		return summaryValidationError("summary.account_invalid", "account id is required")
	}
	request.Source = strings.TrimSpace(request.Source)
	request.SourceRegion = strings.TrimSpace(request.SourceRegion)
	if request.Source == "" {
		return summaryValidationError("summary.source_invalid", "source is required")
	}
	if request.SourceRegion == "" {
		return summaryValidationError("summary.source_region_invalid", "source region is required")
	}
	if request.Version <= 0 {
		return summaryValidationError("summary.version_invalid", "summary version must be positive")
	}
	if err := validateRequestedDatasets(request); err != nil {
		return err
	}
	if err := normalizeSummaryCoverage(request); err != nil {
		return err
	}
	if err := validateSummarySnapshots(request); err != nil {
		return err
	}
	if err := validateSummarySources(request); err != nil {
		return err
	}
	return validateSummaryMemberships(request)
}

func validateRequestedDatasets(request *domain.SummaryApplicationRequest) error {
	seen := make(map[domain.AccountDataset]struct{}, len(request.RequestedDatasets))
	for i, value := range request.RequestedDatasets {
		dataset := domain.AccountDataset(strings.TrimSpace(string(value)))
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != request.Source {
			return summaryValidationError("summary.dataset_invalid", "requested dataset is not registered for the source")
		}
		if _, exists := seen[dataset]; exists {
			return summaryValidationError("summary.duplicate_key", "requested dataset is duplicated")
		}
		seen[dataset] = struct{}{}
		request.RequestedDatasets[i] = dataset
	}
	return nil
}

func validateSummarySnapshots(request *domain.SummaryApplicationRequest) error {
	seen := make(map[domain.AccountDataset]struct{}, len(request.Snapshots))
	for _, snapshot := range request.Snapshots {
		if snapshot == nil {
			return summaryValidationError("summary.dataset_invalid", "snapshot is nil")
		}
		if err := validateSummaryMetadata(request, &snapshot.AccountID, &snapshot.Source,
			&snapshot.SourceRegion, &snapshot.Version); err != nil {
			return err
		}
		snapshot.Dataset = domain.AccountDataset(strings.TrimSpace(string(snapshot.Dataset)))
		descriptor, ok := domain.DatasetDescriptorOf(snapshot.Dataset)
		if !ok || descriptor.Source != request.Source {
			return summaryValidationError("summary.dataset_invalid", "snapshot dataset is not registered for the source")
		}
		if !containsDataset(request.Coverage.Datasets, snapshot.Dataset) {
			return summaryValidationError("summary.dataset_out_of_scope", "snapshot dataset is outside declared coverage")
		}
		if _, exists := seen[snapshot.Dataset]; exists {
			return summaryValidationError("summary.duplicate_key", "snapshot dataset is duplicated")
		}
		seen[snapshot.Dataset] = struct{}{}
		if snapshot.Data == nil {
			snapshot.Data = map[string]any{}
		}
	}
	return nil
}

func validateSummarySources(request *domain.SummaryApplicationRequest) error {
	if err := validateSourceCoverage(request); err != nil {
		return err
	}
	type sourceKey struct{ account, scopeType, scopeID string }
	seen := make(map[sourceKey]struct{}, len(request.Sources))
	for _, source := range request.Sources {
		if source == nil {
			return summaryValidationError("summary.reference_key_invalid", "source reference is nil")
		}
		if err := validateSummaryMetadata(request, &source.AccountID, &source.Source,
			&source.SourceRegion, &source.Version); err != nil {
			return err
		}
		source.SourceAccountID = strings.TrimSpace(source.SourceAccountID)
		source.ScopeType = strings.TrimSpace(source.ScopeType)
		source.ScopeID = strings.TrimSpace(source.ScopeID)
		if source.SourceAccountID == "" || (source.ScopeType == "") != (source.ScopeID == "") {
			return summaryValidationError("summary.reference_key_invalid", "source reference natural key is invalid")
		}
		key := sourceKey{source.SourceAccountID, source.ScopeType, source.ScopeID}
		if _, exists := seen[key]; exists {
			return summaryValidationError("summary.duplicate_key", "source reference natural key is duplicated")
		}
		seen[key] = struct{}{}
		if source.Data == nil {
			source.Data = map[string]any{}
		}
	}
	return nil
}

func validateSummaryMemberships(request *domain.SummaryApplicationRequest) error {
	if err := validateMembershipCoverage(request); err != nil {
		return err
	}
	type membershipKey struct{ scopeType, scopeID, memberID string }
	seen := make(map[membershipKey]struct{}, len(request.Memberships))
	for _, membership := range request.Memberships {
		if membership == nil {
			return summaryValidationError("summary.membership_key_invalid", "membership is nil")
		}
		if err := validateSummaryMetadata(request, &membership.AccountID, &membership.Source,
			&membership.SourceRegion, &membership.Version); err != nil {
			return err
		}
		membership.ScopeType = strings.TrimSpace(membership.ScopeType)
		membership.ScopeID = strings.TrimSpace(membership.ScopeID)
		membership.SourceMemberID = strings.TrimSpace(membership.SourceMemberID)
		if membership.ScopeType == "" || membership.ScopeID == "" || membership.SourceMemberID == "" {
			return summaryValidationError("summary.membership_key_invalid", "membership natural key is invalid")
		}
		key := membershipKey{membership.ScopeType, membership.ScopeID, membership.SourceMemberID}
		if _, exists := seen[key]; exists {
			return summaryValidationError("summary.duplicate_key", "membership natural key is duplicated")
		}
		seen[key] = struct{}{}
		if membership.Data == nil {
			membership.Data = map[string]any{}
		}
	}
	return nil
}

func validateSourceCoverage(request *domain.SummaryApplicationRequest) error {
	if request.Coverage.Explicit && len(request.Sources) > 0 && !request.Coverage.Sources {
		return summaryValidationError("summary.reference_out_of_scope", "source references are outside declared coverage")
	}
	return nil
}

func validateMembershipCoverage(request *domain.SummaryApplicationRequest) error {
	if request.Coverage.Explicit && len(request.Memberships) > 0 && !request.Coverage.Memberships {
		return summaryValidationError("summary.membership_out_of_scope", "memberships are outside declared coverage")
	}
	return nil
}

func normalizeSummaryCoverage(request *domain.SummaryApplicationRequest) error {
	coverage := &request.Coverage
	if len(coverage.Datasets) == 0 && !coverage.Explicit {
		coverage.Datasets = append([]domain.AccountDataset(nil), request.RequestedDatasets...)
	}
	requested := make(map[domain.AccountDataset]struct{}, len(request.RequestedDatasets))
	for _, dataset := range request.RequestedDatasets {
		requested[dataset] = struct{}{}
	}
	seen := make(map[domain.AccountDataset]struct{}, len(coverage.Datasets))
	for i, dataset := range coverage.Datasets {
		dataset = domain.AccountDataset(strings.TrimSpace(string(dataset)))
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != request.Source {
			return summaryValidationError("summary.dataset_invalid", "covered dataset is not registered for the source")
		}
		if len(requested) > 0 {
			if _, ok := requested[dataset]; !ok {
				return summaryValidationError("summary.coverage_invalid", "covered dataset was not requested")
			}
		}
		if _, duplicate := seen[dataset]; duplicate {
			return summaryValidationError("summary.duplicate_key", "covered dataset is duplicated")
		}
		seen[dataset] = struct{}{}
		coverage.Datasets[i] = dataset
	}
	return nil
}

func containsDataset(values []domain.AccountDataset, wanted domain.AccountDataset) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateSummaryMetadata(request *domain.SummaryApplicationRequest, accountID *domain.ID,
	source, sourceRegion *string, version *int64) error {
	if *accountID == (domain.ID{}) {
		*accountID = request.AccountID
	} else if *accountID != request.AccountID {
		return summaryValidationError("summary.account_mismatch", "projection account does not match summary account")
	}
	*source = strings.TrimSpace(*source)
	if *source == "" {
		*source = request.Source
	} else if *source != request.Source {
		return summaryValidationError("summary.source_mismatch", "projection source does not match summary source")
	}
	*sourceRegion = strings.TrimSpace(*sourceRegion)
	if *sourceRegion == "" {
		*sourceRegion = request.SourceRegion
	} else if *sourceRegion != request.SourceRegion {
		return summaryValidationError("summary.source_region_mismatch", "projection region does not match summary region")
	}
	if *version < 0 {
		return summaryValidationError("summary.version_invalid", "projection version must not be negative")
	}
	if *version == 0 {
		*version = request.Version
	} else if *version != request.Version {
		return summaryValidationError("summary.version_mismatch", "projection version does not match summary version")
	}
	return nil
}
