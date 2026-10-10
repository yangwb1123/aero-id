package repository

import (
	"context"
	"fmt"

	"github.com/aero/aero-id/internal/domain"
)

// SnapshotMetadataRepository avoids loading projection content on capability,
// freshness and command-planning paths. An explicit registry scope is required.
type SnapshotMetadataRepository interface {
	ListSnapshotMetadata(context.Context, domain.ID, []domain.AccountDataset) ([]*domain.AccountSnapshot, error)
}

func (r *accountSnapshotRepo) ListSnapshotMetadata(ctx context.Context, accountID domain.ID,
	datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	if accountID == (domain.ID{}) || len(datasets) == 0 || len(datasets) > len(domain.AccountDatasetRegistry()) {
		return nil, fmt.Errorf("invalid snapshot metadata scope")
	}
	for _, dataset := range datasets {
		if _, ok := domain.DatasetDescriptorOf(dataset); !ok {
			return nil, fmt.Errorf("invalid snapshot metadata dataset")
		}
	}
	var rows []accountSnapshotRow
	err := r.db.WithContext(ctx).Select("id", "account_id", "source", "source_region", "dataset", "status",
		"version", "generated_at", "last_synced_at", "expires_at").
		Where("account_id = ? AND dataset IN ?", accountID, datasets).
		Limit(len(domain.AccountDatasetRegistry()) + 1).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > len(domain.AccountDatasetRegistry()) {
		return nil, fmt.Errorf("snapshot metadata scope overflow")
	}
	result := make([]*domain.AccountSnapshot, 0, len(rows))
	for i := range rows {
		result = append(result, snapshotRowToDomain(&rows[i]))
	}
	return result, nil
}

var _ SnapshotMetadataRepository = (*accountSnapshotRepo)(nil)
