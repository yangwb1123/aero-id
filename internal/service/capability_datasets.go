package service

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func snapshotMetadata(ctx context.Context, repo repository.SnapshotRepository, accountID domain.ID,
	datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	if metadata, ok := repo.(repository.SnapshotMetadataRepository); ok {
		return metadata.ListSnapshotMetadata(ctx, accountID, datasets)
	}
	// Compatibility fakes/ports remain usable; production has the content-free
	// query above. Callers must never expose Data or dependency error messages.
	return repo.ListSnapshots(ctx, accountID, datasets)
}

func (s *capabilityService) datasetCapabilities(ctx context.Context, account *domain.Account,
	now time.Time) ([]domain.DatasetCapability, bool) {
	registry := domain.AccountDatasetRegistry()
	scope := make([]domain.AccountDataset, 0, len(registry))
	for _, item := range registry {
		scope = append(scope, item.Name)
	}
	var snapshots []*domain.AccountSnapshot
	unavailable := s.snapshots == nil
	if !unavailable {
		var err error
		snapshots, err = snapshotMetadata(ctx, s.snapshots, account.ID, scope)
		unavailable = err != nil || len(snapshots) > len(registry)
	}
	byDataset := scopedSnapshotMetadata(account.ID, snapshots)
	result := make([]domain.DatasetCapability, 0, len(registry))
	for _, descriptor := range registry {
		item := domain.DatasetCapability{Name: descriptor.Name, Source: descriptor.Source, PII: descriptor.PII,
			Registered: true, Enabled: s.config.Sources[descriptor.Source].SummaryEnabled,
			Authorization: domain.AccessAllowed, ResourceAuthorization: domain.AccessUnknown, Status: "unknown"}
		if !unavailable {
			populateDatasetFreshness(&item, byDataset[item.Name], now)
		}
		item.ReasonCode = datasetCapabilityReason(account, item, unavailable)
		result = append(result, item)
	}
	return result, unavailable
}

func scopedSnapshotMetadata(accountID domain.ID, snapshots []*domain.AccountSnapshot) map[domain.AccountDataset]*domain.AccountSnapshot {
	result := map[domain.AccountDataset]*domain.AccountSnapshot{}
	for _, snapshot := range snapshots {
		if snapshot == nil || snapshot.AccountID != accountID {
			continue
		}
		descriptor, ok := domain.DatasetDescriptorOf(snapshot.Dataset)
		if ok && descriptor.Source == snapshot.Source {
			result[snapshot.Dataset] = snapshot
		}
	}
	return result
}

func populateDatasetFreshness(item *domain.DatasetCapability, snapshot *domain.AccountSnapshot, now time.Time) {
	if snapshot == nil {
		return
	}
	item.Status, item.Version = snapshot.Status, snapshot.Version
	item.LastSyncedAt = snapshot.LastSyncedAt
	if !snapshot.GeneratedAt.IsZero() {
		generated := snapshot.GeneratedAt
		item.GeneratedAt = &generated
	}
	item.Fresh = snapshotFreshWithin(snapshot, now, defaultAggregationMaxAge)
	if item.Status == domain.SnapshotFresh && !item.Fresh {
		item.Status = domain.SnapshotStale
	}
}

func datasetCapabilityReason(account *domain.Account, item domain.DatasetCapability, unavailable bool) string {
	if account.Status == domain.AccountErased {
		return "account.erased"
	}
	if unavailable {
		return "capability.projection_unknown"
	}
	if !item.Enabled {
		return "source.not_configured"
	}
	if item.Status == "unknown" {
		return "source.no_projection"
	}
	if !item.Fresh {
		return "projection.not_fresh"
	}
	return ""
}
