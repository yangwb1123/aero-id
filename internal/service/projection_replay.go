package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/connector"
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
	Source    string
	Cursor    domain.ID
	Limit     int // max events replayed per run; 0 => service default
}

// ProjectionReplayResult reports shadow-vs-live comparison outcomes.
type ProjectionReplayResult struct {
	AccountID      string            `json:"account_id"`
	Source         string            `json:"source"`
	ReplayedEvents int64             `json:"replayed_events"`
	Completed      bool              `json:"completed"` // false => more events remain; inspect the next bounded window with NextCursor
	NextCursor     string            `json:"next_cursor,omitempty"`
	Matched        []ReplayedDataset `json:"matched,omitempty"`
	Divergent      []ReplayedDataset `json:"divergent,omitempty"`
	ShadowOnly     []ReplayedDataset `json:"shadow_only,omitempty"` // in event stream but absent from live projection
	LiveOnly       []ReplayedDataset `json:"live_only,omitempty"`   // in live projection but never seen in events
}

// ReplayedDataset is one dataset's shadow state after replay.
type ReplayedDataset struct {
	Dataset     domain.AccountDataset `json:"dataset"`
	Status      domain.SnapshotStatus `json:"status"`
	Version     int64                 `json:"version"`
	GeneratedAt string                `json:"generated_at,omitempty"`
	Digest      string                `json:"digest"`
	LiveDigest  string                `json:"live_digest,omitempty"`
	LiveVersion int64                 `json:"live_version,omitempty"`
}

// DefaultProjectionReplayBatch caps one replay run; callers may inspect the
// next bounded window with NextCursor.
const DefaultProjectionReplayBatch = 1000
const MaxProjectionReplayBatch = 10000

type projectionReplayService struct {
	events     repository.EventStoreRepository
	snapshots  repository.SnapshotRepository
	accounts   repository.AccountRepository
	permission PermissionChecker
	audit      AuditLedgerService // nil => audit inactive
}

func NewProjectionReplayService(events repository.EventStoreRepository,
	snapshots repository.SnapshotRepository, accounts repository.AccountRepository,
	permission PermissionChecker, opts ...func(*projectionReplayService)) ReplayProjectionService {
	s := &projectionReplayService{events: events, snapshots: snapshots, accounts: accounts, permission: permission}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithProjectionReplayAudit wires the ledger service into the replay tool;
// when wired, every run records one projection.replay.run fact (fresh
// per-execution — the tool has no retry-dedup contract).
func WithProjectionReplayAudit(a AuditLedgerService) func(*projectionReplayService) {
	return func(s *projectionReplayService) { s.audit = a }
}

func (s *projectionReplayService) ReplayProjection(ctx context.Context, input ReplayProjectionInput) (*ProjectionReplayResult, error) {
	input, err := normalizeProjectionReplayInput(input)
	if err != nil {
		return nil, err
	}
	tenantID, err := s.authorizeProjectionReplay(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	envelopes, next, err := s.events.ListByAccount(ctx, tenantID, input.Source,
		input.AccountID.String(), input.Cursor, input.Limit)
	if err != nil {
		return nil, apperr.Internal("replay.event_lookup_failed", "failed to read the event store").Wrap(err)
	}
	result := &ProjectionReplayResult{AccountID: input.AccountID.String(), Source: input.Source}
	if len(envelopes) == 0 {
		result.Completed = true
		return s.replayProjectionFinish(ctx, tenantID, input, result)
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
	return s.replayProjectionFinish(ctx, tenantID, input, result)
}

func normalizeProjectionReplayInput(input ReplayProjectionInput) (ReplayProjectionInput, error) {
	input.Source = strings.TrimSpace(input.Source)
	if input.AccountID == (domain.ID{}) || input.Source == "" {
		return ReplayProjectionInput{}, apperr.BadRequest("replay.invalid_input", "account_id and source are required")
	}
	if !allowedProjectionReplaySource(input.Source) {
		return ReplayProjectionInput{}, apperr.BadRequest("replay.source_invalid", "source is not supported for projection replay")
	}
	if input.Limit <= 0 {
		input.Limit = DefaultProjectionReplayBatch
	}
	if input.Limit > MaxProjectionReplayBatch {
		return ReplayProjectionInput{}, apperr.BadRequest("replay.limit_invalid", "limit exceeds the maximum replay batch size")
	}
	return input, nil
}

func allowedProjectionReplaySource(source string) bool {
	switch source {
	case string(connector.SourceSnaplink), string(connector.SourceAeroIM),
		string(connector.SourceAeroVault), "aero-id":
		return true
	default:
		return false
	}
}

func (s *projectionReplayService) authorizeProjectionReplay(ctx context.Context, accountID domain.ID) (string, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return "", err
	}
	if s.permission == nil {
		return "", apperr.Forbidden("replay.permission_unavailable", "replay permission is not configured")
	}
	allowed, err := s.permission.HasPermission(ctx, actor, domain.PermAuditReplay)
	if err != nil {
		return "", apperr.Internal("replay.permission_check_failed", "failed to verify replay permission").Wrap(err)
	}
	if !allowed {
		return "", apperr.Forbidden("replay.forbidden", "replay permission is required")
	}
	if s.accounts == nil {
		return "", apperr.Internal("replay.account_repository_unavailable", "replay account lookup is unavailable")
	}
	account, err := s.accounts.GetByID(ctx, tenantID, accountID)
	if err != nil {
		return "", mapProjectionReplayAccountError(err)
	}
	if account.Status == domain.AccountErased {
		return "", apperr.Forbidden("replay.account_erased", "replay account is erased")
	}
	return tenantID, nil
}

func mapProjectionReplayAccountError(err error) error {
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("replay.account_not_found", "replay account not found").Wrap(err)
	}
	return apperr.Internal("replay.account_lookup_failed", "failed to resolve replay account").Wrap(err)
}

// replayProjectionFinish records the per-run fact (shared by the zero-event
// branch and the normal path) and returns the result, failing closed on an
// audit error.
func (s *projectionReplayService) replayProjectionFinish(ctx context.Context, tenantID string, input ReplayProjectionInput,
	result *ProjectionReplayResult) (*ProjectionReplayResult, error) {
	if err := s.auditProjectionReplay(ctx, tenantID, input, result); err != nil {
		return nil, apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
	}
	return result, nil
}

// auditProjectionReplay records one projection.replay.run fact per run (the
// single shared bare site for both the zero-event branch and the normal path;
// admin-invoked sensitive-read tool). Fresh per-execution (zero EventID): two
// runs produce two rows — no retry-dedup contract. Returns nil when no audit
// service is wired (nil => audit inactive).
//
// audit-site: standalone-fact — projection.replay.run, fresh per-execution.
func (s *projectionReplayService) auditProjectionReplay(ctx context.Context, tenantID string,
	input ReplayProjectionInput, result *ProjectionReplayResult) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Record(ctx, AuditFactInput{Source: "aero-id", SourceRegion: "local",
		AccountID: input.AccountID, TenantID: tenantID, ActorUID: actorFromContext(ctx),
		Action: domain.AuditProjectionReplay, ResourceType: "projection", ResourceID: input.AccountID.String(),
		Payload: map[string]any{"source": input.Source, "limit": input.Limit,
			"cursor": input.Cursor.String(), "replayed_events": result.ReplayedEvents, "completed": result.Completed}})
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
