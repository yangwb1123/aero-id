package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type AggregationService interface {
	GetMeOverview(context.Context, AggregationInput) (*domain.AggregateOverview, error)
	GetAccountOverview(context.Context, domain.ID, AggregationInput) (*domain.AggregateOverview, error)
}

type AggregationInput struct {
	Consistency domain.ConsistencyMode
	Datasets    []domain.AccountDataset
	MaxAge      time.Duration
}

type aggregationService struct {
	accounts   AccountService
	snapshots  repository.SnapshotRepository
	connectors map[connector.Source]connector.Connector
}

func NewAggregationService(accounts AccountService, snapshots repository.SnapshotRepository, connectors ...connector.Connector) AggregationService {
	bySource := make(map[connector.Source]connector.Connector, len(connectors))
	for _, item := range connectors {
		if item != nil {
			bySource[item.Source()] = item
		}
	}
	return &aggregationService{accounts: accounts, snapshots: snapshots, connectors: bySource}
}

func (s *aggregationService) GetMeOverview(ctx context.Context, input AggregationInput) (*domain.AggregateOverview, error) {
	view, err := s.accounts.GetMe(ctx)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, view.Account, view.Profile, input)
}

func (s *aggregationService) GetAccountOverview(ctx context.Context, id domain.ID, input AggregationInput) (*domain.AggregateOverview, error) {
	view, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, view.Account, view.Profile, input)
}

func (s *aggregationService) load(ctx context.Context, account *domain.Account, profile *domain.AccountProfile,
	input AggregationInput) (*domain.AggregateOverview, error) {
	datasets, err := normalizeDatasets(input.Datasets)
	if err != nil {
		return nil, err
	}
	mode := input.Consistency
	if mode == "" {
		mode = domain.ConsistencyEventual
	}
	if mode != domain.ConsistencyEventual && mode != domain.ConsistencyBounded && mode != domain.ConsistencyStrong {
		return nil, apperr.BadRequest("aggregation.invalid_consistency", "unsupported consistency mode")
	}
	if input.MaxAge <= 0 {
		input.MaxAge = 5 * time.Minute
	}
	snapshots, err := s.snapshots.ListSnapshots(ctx, account.ID, datasets)
	if err != nil {
		return nil, apperr.Internal("aggregation.snapshot_load_failed", "failed to load account projections").Wrap(err)
	}
	result := &domain.AggregateOverview{
		Account: account, Profile: profile, Snapshots: snapshots,
		Consistency: mode, GeneratedAt: time.Now().UTC(), SourceErrors: map[string]string{},
	}
	if mode != domain.ConsistencyEventual {
		s.refresh(ctx, account, datasets, snapshots, input.MaxAge, mode, result)
		fresh, err := s.snapshots.ListSnapshots(ctx, account.ID, datasets)
		if err != nil {
			return nil, apperr.Internal("aggregation.snapshot_reload_failed", "failed to reload account projections").Wrap(err)
		}
		result.Snapshots = fresh
	}
	result.Partial = s.markFreshness(result, datasets)
	return result, nil
}

func (s *aggregationService) refresh(ctx context.Context, account *domain.Account,
	datasets []domain.AccountDataset, snapshots []*domain.AccountSnapshot, maxAge time.Duration,
	mode domain.ConsistencyMode, result *domain.AggregateOverview) {
	bySource := snapshotSources(snapshots)
	var wait sync.WaitGroup
	var mu sync.Mutex
	for source, connectorClient := range s.connectors {
		requested := datasetsForSource(datasets, string(source))
		if len(requested) == 0 || !needsRefresh(bySource[source], requested, maxAge, mode) {
			continue
		}
		wait.Add(1)
		go func(source connector.Source, client connector.Connector, requested []domain.AccountDataset) {
			defer wait.Done()
			s.refreshSource(ctx, account, source, client, requested, result, &mu)
		}(source, connectorClient, requested)
	}
	wait.Wait()
}

func (s *aggregationService) refreshSource(ctx context.Context, account *domain.Account, source connector.Source,
	client connector.Connector, requested []domain.AccountDataset, result *domain.AggregateOverview, mu *sync.Mutex) {
	summary, err := client.Summary(ctx, connector.SummaryRequest{AccountID: account.ID, CanonicalUID: account.CanonicalUID,
		ActorUID: actorFromContext(ctx), TenantID: TenantFrom(ctx), RequestID: RequestIDFrom(ctx), Region: account.HomeRegion, Datasets: requested})
	if err != nil {
		s.setSourceError(result, mu, source, sourceErrorCode(err))
		return
	}
	if summary == nil {
		s.setSourceError(result, mu, source, "source.empty_summary")
		return
	}
	if err := s.persistSummary(ctx, account.ID, summary); err != nil {
		s.setSourceError(result, mu, source, "projection.write_failed")
		return
	}
	if !summary.Complete {
		s.setSourceError(result, mu, source, "source.summary_incomplete")
	}
}

func sourceErrorCode(err error) string {
	if remote, ok := connector.IsRemote(err); ok && remote.Code != "" {
		return remote.Code
	}
	return "source.unavailable"
}

func (s *aggregationService) setSourceError(result *domain.AggregateOverview, mu *sync.Mutex,
	source connector.Source, code string) {
	mu.Lock()
	defer mu.Unlock()
	result.SourceErrors[string(source)] = code
}

func (s *aggregationService) persistSummary(ctx context.Context, accountID domain.ID, summary *connector.Summary) error {
	for _, snapshot := range summary.Snapshots {
		if snapshot == nil {
			continue
		}
		snapshot.AccountID = accountID
		if err := s.snapshots.UpsertSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	for _, sourceRef := range summary.Sources {
		if sourceRef == nil {
			continue
		}
		sourceRef.AccountID = accountID
		if err := s.accounts.UpsertSource(ctx, sourceRef); err != nil {
			return err
		}
	}
	for _, membership := range summary.Memberships {
		if membership == nil {
			continue
		}
		membership.AccountID = accountID
		if err := s.accounts.UpsertMembership(ctx, membership); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDatasets(values []domain.AccountDataset) ([]domain.AccountDataset, error) {
	if len(values) == 0 {
		all := domain.AccountDatasetRegistry()
		result := make([]domain.AccountDataset, len(all))
		for i, descriptor := range all {
			result[i] = descriptor.Name
		}
		return result, nil
	}
	seen := make(map[domain.AccountDataset]struct{}, len(values))
	result := make([]domain.AccountDataset, 0, len(values))
	for _, value := range values {
		value = domain.AccountDataset(strings.TrimSpace(string(value)))
		if _, ok := domain.DatasetDescriptorOf(value); !ok {
			return nil, apperr.BadRequest("aggregation.invalid_dataset", "dataset is not allow-listed")
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func datasetsForSource(datasets []domain.AccountDataset, source string) []domain.AccountDataset {
	result := make([]domain.AccountDataset, 0, len(datasets))
	for _, dataset := range datasets {
		if descriptor, ok := domain.DatasetDescriptorOf(dataset); ok && descriptor.Source == source {
			result = append(result, dataset)
		}
	}
	return result
}

func snapshotSources(snapshots []*domain.AccountSnapshot) map[connector.Source][]*domain.AccountSnapshot {
	result := make(map[connector.Source][]*domain.AccountSnapshot)
	for _, snapshot := range snapshots {
		if snapshot != nil {
			result[connector.Source(snapshot.Source)] = append(result[connector.Source(snapshot.Source)], snapshot)
		}
	}
	return result
}

func needsRefresh(snapshots []*domain.AccountSnapshot, requested []domain.AccountDataset, maxAge time.Duration, mode domain.ConsistencyMode) bool {
	if mode == domain.ConsistencyStrong || len(snapshots) == 0 {
		return true
	}
	byDataset := make(map[domain.AccountDataset]*domain.AccountSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot != nil {
			byDataset[snapshot.Dataset] = snapshot
		}
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	for _, dataset := range requested {
		snapshot := byDataset[dataset]
		if snapshot == nil || snapshot.Status != domain.SnapshotFresh || snapshot.LastSyncedAt == nil || snapshot.LastSyncedAt.Before(cutoff) {
			return true
		}
	}
	return false
}

func (s *aggregationService) markFreshness(result *domain.AggregateOverview, datasets []domain.AccountDataset) bool {
	partial := len(result.SourceErrors) > 0
	now := time.Now().UTC()
	byDataset := snapshotByDataset(result.Snapshots)
	for _, dataset := range datasets {
		snapshot := byDataset[dataset]
		if snapshot == nil {
			partial = s.markMissingDataset(result, dataset) || partial
			continue
		}
		if snapshot.Status == domain.SnapshotFresh && !snapshot.Fresh(now) {
			snapshot.Status = domain.SnapshotStale
		}
		if snapshot.Status != domain.SnapshotFresh {
			result.StaleDatasets = append(result.StaleDatasets, dataset)
			partial = true
		}
	}
	return partial
}

func snapshotByDataset(snapshots []*domain.AccountSnapshot) map[domain.AccountDataset]*domain.AccountSnapshot {
	result := make(map[domain.AccountDataset]*domain.AccountSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot != nil {
			result[snapshot.Dataset] = snapshot
		}
	}
	return result
}

func (s *aggregationService) markMissingDataset(result *domain.AggregateOverview, dataset domain.AccountDataset) bool {
	descriptor, ok := domain.DatasetDescriptorOf(dataset)
	if !ok {
		return true
	}
	source := descriptor.Source
	if _, exists := result.SourceErrors[source]; exists {
		return true
	}
	if _, configured := s.connectors[connector.Source(source)]; configured {
		result.SourceErrors[source] = "source.no_projection"
	} else {
		result.SourceErrors[source] = "source.not_configured"
	}
	return true
}
