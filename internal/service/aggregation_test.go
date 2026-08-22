package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type aggregationConnectorFixture struct {
	summary *connector.Summary
}

func (f aggregationConnectorFixture) Source() connector.Source { return f.summary.Source }
func (f aggregationConnectorFixture) Summary(context.Context, connector.SummaryRequest) (*connector.Summary, error) {
	return f.summary, nil
}
func (aggregationConnectorFixture) Health(context.Context) error { return nil }

type aggregationSnapshotFixture struct {
	repository.SnapshotRepository
	upserted []*domain.AccountSnapshot
}

func (f *aggregationSnapshotFixture) UpsertSnapshot(_ context.Context, snapshot *domain.AccountSnapshot) error {
	f.upserted = append(f.upserted, snapshot)
	return nil
}

type aggregationAccountFixture struct{ AccountService }

func (aggregationAccountFixture) UpsertSource(context.Context, *domain.AccountSource) error {
	return nil
}
func (aggregationAccountFixture) UpsertMembership(context.Context, *domain.AccountMembership) error {
	return nil
}

type activityAccountFixture struct {
	AccountService
	accountID domain.ID
}

func (f activityAccountFixture) GetMe(context.Context) (*AccountView, error) {
	return &AccountView{Account: &domain.Account{ID: f.accountID}}, nil
}

type activitySnapshotFixture struct {
	repository.SnapshotRepository
	snapshot *domain.AccountSnapshot
}

func (f activitySnapshotFixture) ListSnapshots(context.Context, domain.ID,
	[]domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	return []*domain.AccountSnapshot{f.snapshot}, nil
}

func TestAggregationRefreshMarksIncompleteSummaryPartial(t *testing.T) {
	accountID := idgen.NewID()
	snapshots := &aggregationSnapshotFixture{}
	service := &aggregationService{accounts: aggregationAccountFixture{}, snapshots: snapshots}
	result := &domain.AggregateOverview{SourceErrors: map[string]string{}}
	summary := &connector.Summary{
		Source: connector.SourceAeroVault, Complete: false,
		Snapshots: []*domain.AccountSnapshot{{Dataset: domain.AccountDataset("aero-vault.buckets")}},
	}
	service.refreshSource(context.Background(), &domain.Account{ID: accountID}, connector.SourceAeroVault,
		aggregationConnectorFixture{summary: summary}, []domain.AccountDataset{"aero-vault.buckets"}, result, &sync.Mutex{})

	if got := result.SourceErrors[string(connector.SourceAeroVault)]; got != "source.summary_incomplete" {
		t.Fatalf("source error = %q, want source.summary_incomplete", got)
	}
	if len(snapshots.upserted) != 1 || snapshots.upserted[0].AccountID != accountID {
		t.Fatalf("upserted snapshots = %+v, want one snapshot owned by account", snapshots.upserted)
	}
}

func TestAggregationRefreshLeavesCompleteSummaryHealthy(t *testing.T) {
	snapshots := &aggregationSnapshotFixture{}
	service := &aggregationService{accounts: aggregationAccountFixture{}, snapshots: snapshots}
	result := &domain.AggregateOverview{SourceErrors: map[string]string{}}
	service.refreshSource(context.Background(), &domain.Account{ID: idgen.NewID()}, connector.SourceAeroVault,
		aggregationConnectorFixture{summary: &connector.Summary{Source: connector.SourceAeroVault, Complete: true}},
		nil, result, &sync.Mutex{})
	if len(result.SourceErrors) != 0 {
		t.Fatalf("complete summary produced source errors: %v", result.SourceErrors)
	}
}

func TestAggregationMarkFreshnessHonorsMaxAge(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	result := &domain.AggregateOverview{
		Snapshots: []*domain.AccountSnapshot{{Dataset: domain.AccountDataset("aero-vault.buckets"),
			Status: domain.SnapshotFresh, LastSyncedAt: &old}},
		SourceErrors: map[string]string{},
	}
	service := &aggregationService{}
	if !service.markFreshness(result, []domain.AccountDataset{"aero-vault.buckets"}, time.Minute) {
		t.Fatal("old projection must make the aggregate partial")
	}
	if result.Snapshots[0].Status != domain.SnapshotStale {
		t.Fatalf("snapshot status = %q, want stale", result.Snapshots[0].Status)
	}
	if len(result.StaleDatasets) != 1 || result.StaleDatasets[0] != "aero-vault.buckets" {
		t.Fatalf("stale datasets = %#v, want aero-vault.buckets", result.StaleDatasets)
	}
}

func TestAggregationMarkFreshnessRequiresLastSyncedAt(t *testing.T) {
	result := &domain.AggregateOverview{
		Snapshots:    []*domain.AccountSnapshot{{Dataset: domain.AccountDataset("aero-vault.buckets"), Status: domain.SnapshotFresh}},
		SourceErrors: map[string]string{},
	}
	if !(&aggregationService{}).markFreshness(result, []domain.AccountDataset{"aero-vault.buckets"}, time.Minute) {
		t.Fatal("projection without sync timestamp must be partial")
	}
	if result.Snapshots[0].Status != domain.SnapshotStale {
		t.Fatalf("snapshot status = %q, want stale", result.Snapshots[0].Status)
	}
}

func TestAggregationListMeActivityUsesEventIDKeyset(t *testing.T) {
	now := time.Now().UTC()
	svc := &aggregationService{accounts: activityAccountFixture{accountID: idgen.NewID()},
		snapshots: activitySnapshotFixture{snapshot: &domain.AccountSnapshot{
			Dataset: domain.DatasetAeroIMActivitySummary, Status: domain.SnapshotFresh, LastSyncedAt: &now,
			Data: map[string]any{"events": []map[string]any{{"event_id": "event-3"}, {"event_id": "event-2"}, {"event_id": "event-1"}}},
		}}, connectors: map[connector.Source]connector.Connector{}}
	first, err := svc.ListMeActivity(context.Background(), AggregationInput{}, domain.Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 1 || first.Events[0]["event_id"] != "event-3" || first.NextCursor != "event-3" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := svc.ListMeActivity(context.Background(), AggregationInput{}, domain.Page{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 2 || second.Events[0]["event_id"] != "event-2" || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestAggregationListMeActivityRejectsInvalidCursor(t *testing.T) {
	now := time.Now().UTC()
	svc := &aggregationService{accounts: activityAccountFixture{accountID: idgen.NewID()},
		snapshots: activitySnapshotFixture{snapshot: &domain.AccountSnapshot{
			Dataset: domain.DatasetAeroIMActivitySummary, Status: domain.SnapshotFresh, LastSyncedAt: &now,
			Data: map[string]any{"events": []any{map[string]any{"event_id": "event-1"}}},
		}}, connectors: map[connector.Source]connector.Connector{}}
	_, err := svc.ListMeActivity(context.Background(), AggregationInput{}, domain.Page{Cursor: "missing"})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "activity.invalid_cursor" {
		t.Fatalf("error = %v, want activity.invalid_cursor", err)
	}
}

func TestAggregationListMeActivityRejectsMalformedProjection(t *testing.T) {
	now := time.Now().UTC()
	svc := &aggregationService{accounts: activityAccountFixture{accountID: idgen.NewID()},
		snapshots: activitySnapshotFixture{snapshot: &domain.AccountSnapshot{
			Dataset: domain.DatasetAeroIMActivitySummary, Status: domain.SnapshotFresh, LastSyncedAt: &now,
			Data: map[string]any{"events": []any{map[string]any{"event_type": "message.sent"}}},
		}}, connectors: map[connector.Source]connector.Connector{}}
	_, err := svc.ListMeActivity(context.Background(), AggregationInput{}, domain.Page{})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "activity.projection_invalid" || appErr.HTTP != 503 {
		t.Fatalf("error = %v, want activity.projection_invalid/503", err)
	}
}
