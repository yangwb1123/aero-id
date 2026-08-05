package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// ReplayProjectionService rebuilds a shadow projection from the durable event
// store and compares it with the live projection (P3-07). It never writes to
// the live projection and produces no side effects.
type ReplayProjectionService interface {
	ReplayProjection(ctx context.Context, input ReplayProjectionInput) (*ProjectionReplayResult, error)
}

type ReplayProjectionInput struct {
	AccountID domain.ID
	TenantID  string
	Source    string
	Limit     int // max events replayed per run; 0 => service default
}

// ProjectionReplayResult reports shadow-vs-live comparison outcomes.
type ProjectionReplayResult struct {
	AccountID      string             `json:"account_id"`
	Source         string             `json:"source"`
	ReplayedEvents int64              `json:"replayed_events"`
	Completed      bool               `json:"completed"` // false => more events remain; run again with the returned cursor
	NextCursor     string             `json:"next_cursor,omitempty"`
	Matched        []ReplayedDataset  `json:"matched,omitempty"`
	Divergent      []ReplayedDataset  `json:"divergent,omitempty"`
	ShadowOnly     []ReplayedDataset  `json:"shadow_only,omitempty"` // in event stream but absent from live projection
	LiveOnly       []ReplayedDataset  `json:"live_only,omitempty"`   // in live projection but never seen in events
}

// ReplayedDataset is one dataset's shadow state after replay.
type ReplayedDataset struct {
	Dataset      domain.AccountDataset `json:"dataset"`
	Status       domain.SnapshotStatus `json:"status"`
	Version      int64                 `json:"version"`
	GeneratedAt  string                `json:"generated_at,omitempty"`
	Digest       string                `json:"digest"`
	LiveDigest   string                `json:"live_digest,omitempty"`
	LiveVersion  int64                 `json:"live_version,omitempty"`
}

// DefaultProjectionReplayBatch caps one replay run; callers loop on NextCursor.
const DefaultProjectionReplayBatch = 1000

type projectionReplayService struct {
	events    repository.EventStoreRepository
	snapshots repository.SnapshotRepository
}

func NewProjectionReplayService(events repository.EventStoreRepository,
	snapshots repository.SnapshotRepository) ReplayProjectionService {
	return &projectionReplayService{events: events, snapshots: snapshots}
}

func (s *projectionReplayService) ReplayProjection(ctx context.Context, input ReplayProjectionInput) (*ProjectionReplayResult, error) {
	if input.AccountID == (domain.ID{}) || strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.TenantID) == "" {
		return nil, apperr.BadRequest("replay.invalid_input", "account_id, tenant and source are required")
	}
	if input.Limit <= 0 {
		input.Limit = DefaultProjectionReplayBatch
	}
	envelopes, next, err := s.events.ListByAccount(ctx, input.TenantID, input.Source, input.AccountID.String(), domain.ID{}, input.Limit)
	if err != nil {
		return nil, apperr.Internal("replay.event_lookup_failed", "failed to read the event store").Wrap(err)
	}
	result := &ProjectionReplayResult{AccountID: input.AccountID.String(), Source: input.Source}
	if len(envelopes) == 0 {
		result.Completed = true
		return result, nil
	}
	shadow, err := rebuildShadowProjection(envelopes)
	if err != nil {
		return nil, apperr.BadRequest("replay.invalid_event", "event stream cannot rebuild a valid projection").Wrap(err)
	}
	live, err := s.snapshots.ListSnapshots(ctx, input.AccountID, nil)
	if err != nil {
		return nil, apperr.Internal("replay.snapshot_lookup_failed", "failed to read live projections").Wrap(err)
	}
	result.ReplayedEvents = int64(len(envelopes))
	result.Completed = next == (domain.ID{})
	if !result.Completed {
		result.NextCursor = next.String()
	}
	compareProjections(result, shadow, live)
	return result, nil
}

// rebuildShadowProjection folds events in received order, keeping the highest
// version per dataset (events are stored in id DESC order, so reverse first).
func rebuildShadowProjection(envelopes []*event.Envelope) (map[domain.AccountDataset]*domain.AccountSnapshot, error) {
	shadow := make(map[domain.AccountDataset]*domain.AccountSnapshot)
	for i := len(envelopes) - 1; i >= 0; i-- {
		envelope := envelopes[i]
		mutation, err := envelope.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("event %s: %w", envelope.EventID, err)
		}
		current, ok := shadow[mutation.Dataset]
		if ok && mutation.Version > 0 && current.Version > mutation.Version {
			continue // out-of-order/older event; keep the newest
		}
		shadow[mutation.Dataset] = &domain.AccountSnapshot{
			AccountID: mutation.AccountID, Source: mutation.Source, SourceRegion: mutation.SourceRegion,
			Dataset: mutation.Dataset, Status: mutation.Status, Version: mutation.Version,
			GeneratedAt: envelope.OccurredAt, Data: mutation.Data,
		}
	}
	return shadow, nil
}

func compareProjections(result *ProjectionReplayResult, shadow map[domain.AccountDataset]*domain.AccountSnapshot,
	live []*domain.AccountSnapshot) {
	liveByDataset := make(map[domain.AccountDataset]*domain.AccountSnapshot, len(live))
	for _, snapshot := range live {
		liveByDataset[snapshot.Dataset] = snapshot
	}
	for dataset, shadowSnapshot := range shadow {
		item := ReplayedDataset{Dataset: dataset, Status: shadowSnapshot.Status, Version: shadowSnapshot.Version,
			Digest: snapshotDigest(shadowSnapshot)}
		if !shadowSnapshot.GeneratedAt.IsZero() {
			item.GeneratedAt = shadowSnapshot.GeneratedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		liveSnapshot, ok := liveByDataset[dataset]
		if !ok {
			result.ShadowOnly = append(result.ShadowOnly, item)
			continue
		}
		item.LiveDigest = snapshotDigest(liveSnapshot)
		item.LiveVersion = liveSnapshot.Version
		if item.Digest == item.LiveDigest {
			result.Matched = append(result.Matched, item)
		} else {
			result.Divergent = append(result.Divergent, item)
		}
	}
	for dataset, liveSnapshot := range liveByDataset {
		if _, ok := shadow[dataset]; !ok {
			result.LiveOnly = append(result.LiveOnly, ReplayedDataset{Dataset: dataset, Status: liveSnapshot.Status,
				Version: liveSnapshot.Version, Digest: snapshotDigest(liveSnapshot)})
		}
	}
}
