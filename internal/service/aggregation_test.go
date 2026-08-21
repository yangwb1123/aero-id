package service

import (
	"context"
	"sync"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
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
