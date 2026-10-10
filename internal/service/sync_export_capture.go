package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func invalidExportScope() error {
	return apperr.Internal("export.scope_mismatch", "export data does not match the requested scope")
}

func (s *syncJobService) buildAccountExportManifest(ctx context.Context, job *domain.SyncJob) (map[string]any, error) {
	selection, err := parsePersistedSyncDatasets(job.Request, true)
	if err != nil {
		return nil, apperr.BadRequest("export.invalid_dataset_scope", "export requires an explicit persisted dataset scope")
	}
	ctx, cancel := syncRemoteContext(ctx)
	defer cancel()
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return nil, err
	}
	manifest, err := s.exportCoreManifest(ctx, job, selection)
	if err != nil {
		return nil, err
	}
	budget := &exportBudget{limit: s.maxExport}
	if err := budget.add(manifest); err != nil {
		return nil, err
	}
	sources, err := readExportPages(budget, func(page domain.Page) (exportReferencePage, error) {
		return s.exportSourcePage(ctx, job, selection, page)
	})
	if err != nil {
		return nil, err
	}
	memberships, err := readExportPages(budget, func(page domain.Page) (exportReferencePage, error) {
		return s.exportMembershipPage(ctx, job, selection, page)
	})
	if err != nil {
		return nil, err
	}
	manifest["sources"], manifest["memberships"] = sources, memberships
	manifest["source_count"], manifest["membership_count"] = len(sources), len(memberships)
	manifest["traversal_complete"] = true
	// Completion is pagination/scope coverage, not a cross-source point-in-time
	// snapshot. Individual snapshots still expose their status and version.
	manifest["manifest_digest"] = accountExportDigest(manifest)
	if manifest["manifest_digest"] == "" {
		return nil, apperr.Internal("export.serialize_failed", "failed to serialize export manifest")
	}
	return manifest, nil
}

func (s *syncJobService) exportCoreManifest(ctx context.Context, job *domain.SyncJob,
	selection []domain.AccountDataset) (map[string]any, error) {
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		return nil, apperr.Internal("export.account_load_failed", "failed to load account").Wrap(err)
	}
	if account == nil || account.Status == domain.AccountErased {
		return nil, apperr.Forbidden("export.account_erased", "export is unavailable for an erased account")
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return nil, err
	}
	profile, err := s.accountRepo.GetProfile(ctx, job.AccountID)
	if err != nil {
		return nil, apperr.Internal("export.profile_load_failed", "failed to load export profile").Wrap(err)
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return nil, err
	}
	snapshots, err := s.snapshots.ListSnapshots(ctx, job.AccountID, selection)
	if err != nil {
		return nil, apperr.Internal("export.snapshot_load_failed", "failed to load export data").Wrap(err)
	}
	values, missing, err := selectedExportSnapshots(job.AccountID, selection, snapshots)
	if err != nil {
		return nil, err
	}
	return map[string]any{"schema_version": 1, "type": "export", "job_id": job.ID.String(), "tenant_id": job.TenantID,
		"account_id": job.AccountID.String(), "account": exportAccount(account), "profile": exportProfile(profile),
		"datasets": values, "dataset_count": len(values), "selected_datasets": datasetNames(selection),
		"missing_datasets": datasetNames(missing), "coverage_complete": len(missing) == 0,
		"complete":      len(missing) == 0 && exportSnapshotsAvailable(values),
		"point_in_time": false, "generated_at": time.Now().UTC()}, nil
}

func exportSnapshotsAvailable(values []map[string]any) bool {
	for _, value := range values {
		status := value["status"]
		if status != domain.SnapshotFresh && status != domain.SnapshotStale {
			return false
		}
	}
	return true
}

func selectedExportSnapshots(accountID domain.ID, selection []domain.AccountDataset,
	snapshots []*domain.AccountSnapshot) ([]map[string]any, []domain.AccountDataset, error) {
	wanted := make(map[domain.AccountDataset]bool, len(selection))
	for _, dataset := range selection {
		wanted[dataset] = true
	}
	seen := make(map[domain.AccountDataset]bool)
	values := make([]map[string]any, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		descriptor, _ := domain.DatasetDescriptorOf(snapshot.Dataset)
		if snapshot.AccountID != accountID || !wanted[snapshot.Dataset] ||
			(snapshot.Source != descriptor.Source && snapshot.Source != "aero-id") {
			return nil, nil, invalidExportScope()
		}
		seen[snapshot.Dataset] = true
		values = append(values, exportSnapshot(snapshot))
	}
	missing := make([]domain.AccountDataset, 0)
	for _, dataset := range selection {
		if !seen[dataset] {
			missing = append(missing, dataset)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return values, missing, nil
}

func (s *syncJobService) exportDataFailure(ctx context.Context, job *domain.SyncJob, err error) (*domain.SyncJob, error) {
	if isSyncExecutionBlocked(err) {
		return s.pauseSyncExecution(ctx, job, err, false, nil)
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		appErr = apperr.Internal("export.read_failed", "failed to read export data").Wrap(err)
	}
	if appErr.Code == "export.too_large" {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobPartiallyComplete, appErr.Code, appErr.Message, nil)
	}
	return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, appErr.Code, appErr.Message, nil, err)
}
