package service

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"

	"github.com/aero/aero-id/internal/domain"
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
