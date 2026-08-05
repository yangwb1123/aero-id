package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func snapshotDrift(before, after []*domain.AccountSnapshot) int {
	previous := make(map[string]string, len(before))
	for _, snapshot := range before {
		if snapshot != nil {
			previous[snapshotKey(snapshot)] = snapshotDigest(snapshot)
		}
	}
	drift := 0
	seen := make(map[string]struct{}, len(after))
	for _, snapshot := range after {
		if snapshot == nil {
			continue
		}
		key := snapshotKey(snapshot)
		seen[key] = struct{}{}
		if previous[key] != snapshotDigest(snapshot) {
			drift++
		}
	}
	for key := range previous {
		if _, ok := seen[key]; !ok {
			drift++
		}
	}
	return drift
}

func snapshotKey(snapshot *domain.AccountSnapshot) string {
	return snapshot.Source + "\x00" + string(snapshot.Dataset)
}

func snapshotDigest(snapshot *domain.AccountSnapshot) string {
	value, _ := json.Marshal(struct {
		Status  domain.SnapshotStatus
		Version int64
		Data    map[string]any
	}{snapshot.Status, snapshot.Version, snapshot.Data})
	sum := sha256.Sum256(value)
	return strconv.FormatInt(snapshot.Version, 10) + ":" + string(sum[:])
}

func reconcileMissingSnapshots(ctx context.Context, repo repository.SnapshotRepository, accountID domain.ID, source string,
	requested []domain.AccountDataset, returned []*domain.AccountSnapshot, version int64) (int, error) {
	reconciler, ok := repo.(repository.SnapshotReconciler)
	if !ok || version <= 0 {
		return 0, nil
	}
	present := make(map[domain.AccountDataset]struct{}, len(returned))
	for _, snapshot := range returned {
		if snapshot != nil {
			present[snapshot.Dataset] = struct{}{}
		}
	}
	missing := make([]domain.AccountDataset, 0, len(requested))
	for _, dataset := range requested {
		if _, ok := present[dataset]; !ok {
			missing = append(missing, dataset)
		}
	}
	return reconciler.MarkUnavailable(ctx, accountID, source, missing, version)
}

func markReconciledSnapshots(ctx context.Context, repo repository.SnapshotRepository, accountID domain.ID, source string,
	requested []domain.AccountDataset, returned []*domain.AccountSnapshot, version int64) error {
	_, err := reconcileMissingSnapshots(ctx, repo, accountID, source, requested, returned, version)
	return err
}

func markReconciledReferences(ctx context.Context, repo repository.AccountRepository, accountID domain.ID, source string,
	returnedSources []*domain.AccountSource, returnedMemberships []*domain.AccountMembership, version int64) error {
	reconciler, ok := repo.(repository.AccountReferenceReconciler)
	if !ok || version <= 0 {
		return nil
	}
	return reconciler.MarkMissingReferences(ctx, accountID, source, returnedSources, returnedMemberships, version)
}
