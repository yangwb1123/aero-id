package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type bindingLoader struct {
	account *domain.Account
	err     error
	calls   atomic.Int32
	mu      sync.Mutex
	keys    [][2]any
	mutate  func()
}

func (l *bindingLoader) GetByID(_ context.Context, tenant string, id domain.ID) (*domain.Account, error) {
	l.calls.Add(1)
	l.mu.Lock()
	l.keys = append(l.keys, [2]any{tenant, id})
	l.mu.Unlock()
	if l.mutate != nil {
		l.mutate()
	}
	return l.account, l.err
}

type bindingConnector struct {
	source connector.Source
	calls  atomic.Int32
	mu     sync.Mutex
	last   connector.SummaryRequest
	err    error
}

func (c *bindingConnector) Source() connector.Source     { return c.source }
func (c *bindingConnector) Health(context.Context) error { return nil }
func (c *bindingConnector) Summary(_ context.Context, request connector.SummaryRequest) (*connector.Summary, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.last = request
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return &connector.Summary{Source: c.source, SourceRegion: "west-1", Version: 1, Complete: true}, nil
}

func (c *bindingConnector) request() connector.SummaryRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

type bindingApplication struct {
	calls atomic.Int32
	mu    sync.Mutex
	last  domain.SummaryApplicationRequest
}

func (a *bindingApplication) ApplySummary(_ context.Context, request domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	a.calls.Add(1)
	a.mu.Lock()
	a.last = request
	a.mu.Unlock()
	return domain.SummaryApplicationResult{Applied: true, Outcome: domain.SummaryApplied}, nil
}

func (a *bindingApplication) request() domain.SummaryApplicationRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func bindingAccount(id byte, tenant, uid, region string) *domain.Account {
	return &domain.Account{ID: domain.ID{id}, TenantID: tenant, CanonicalUID: uid, HomeRegion: region, Status: domain.AccountActive}
}

func TestSummaryTargetBindingUsesAuthoritativeVaultIdentityAcrossServicePaths(t *testing.T) {
	authoritative := bindingAccount(21, "tenant-a", "uid-a", "west-1")
	caller := bindingAccount(22, "tenant-b", "uid-b", "east-1")
	datasets := []domain.AccountDataset{"aero-vault.tenants"}
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "actor-a", TenantID: "tenant-a"})

	t.Run("aggregation", func(t *testing.T) {
		loader := &bindingLoader{account: authoritative}
		app := &bindingApplication{}
		client := &bindingConnector{source: connector.SourceAeroVault}
		svc := &aggregationService{authoritativeAccounts: loader, application: app}
		result := &domain.AggregateOverview{SourceErrors: map[string]string{}}
		held := *authoritative
		loader.mutate = func() { held = *caller }
		svc.refreshSource(ctx, &held, connector.SourceAeroVault, client, datasets, result, &sync.Mutex{})
		if client.calls.Load() != 1 || app.calls.Load() != 1 {
			t.Fatalf("source/application calls = %d/%d, want 1/1", client.calls.Load(), app.calls.Load())
		}
		assertBindingRequest(t, client.request(), authoritative, "actor-a", "")
		if got := app.request().AccountID; got != authoritative.ID {
			t.Fatalf("projection account = %v, want authoritative %v", got, authoritative.ID)
		}
		if len(result.SourceErrors) != 0 {
			t.Fatalf("source errors = %v", result.SourceErrors)
		}
	})

	t.Run("sync request", func(t *testing.T) {
		job := &domain.SyncJob{ID: domain.ID{31}, AccountID: authoritative.ID, TenantID: authoritative.TenantID,
			ActorUID: "job-actor", RequestID: "job-request", JobType: domain.SyncJobReconcile}
		request, err := syncSummaryRequest(ctx, &bindingLoader{account: authoritative}, caller, job, connector.SourceAeroVault, datasets)
		if err != nil {
			t.Fatal(err)
		}
		assertBindingRequest(t, request, authoritative, job.ActorUID, job.ID.String())
		if !request.Reconcile || request.RequestID != job.RequestID {
			t.Fatalf("workflow metadata = %#v", request)
		}
	})

	t.Run("operation runner", func(t *testing.T) {
		client := &bindingConnector{source: connector.SourceAeroVault}
		app := &bindingApplication{}
		runner := &connectorOperationRunner{accounts: &migrationAccountRepository{account: authoritative}, application: app}
		op := &domain.Operation{ID: domain.ID{41}, AccountID: authoritative.ID, TenantID: authoritative.TenantID,
			ActorUID: "operation-actor", RequestID: "operation-request"}
		step := &domain.OperationStep{Source: string(connector.SourceAeroVault), Action: "summary"}
		_, _, err := runner.sync(ctx, op, step, client, caller, datasets)
		if err != nil {
			t.Fatal(err)
		}
		assertBindingRequest(t, client.request(), authoritative, op.ActorUID, op.ID.String())
		if app.request().AccountID != authoritative.ID {
			t.Fatalf("operation projection account = %v, want %v", app.request().AccountID, authoritative.ID)
		}
	})

	t.Run("legacy migration", func(t *testing.T) {
		client := &bindingConnector{source: connector.SourceAeroVault}
		repo := &bindingMigrationRepository{migrationAccountRepository: migrationAccountRepository{account: authoritative}}
		svc := &legacyMigrationService{legacy: &fakeLegacyMigrationRepo{}, connectors: map[connector.Source]connector.Connector{
			connector.SourceAeroVault: client}, accountRepo: repo, accountIssuer: "issuer-a", defaultLimit: 1000}
		result := &LegacyCompareResult{SourceErrors: map[string]string{}}
		if err := svc.compareWithSource(ctx, LegacyCompareInput{OwnerUID: "owner-input", Source: connector.SourceAeroVault}, result); err != nil {
			t.Fatal(err)
		}
		if repo.tenant != "tenant-a" || repo.issuer != "issuer-a" || repo.subject != "owner-input" {
			t.Fatalf("identity lookup = %q/%q/%q", repo.tenant, repo.issuer, repo.subject)
		}
		assertBindingRequest(t, client.request(), authoritative, "actor-a", "")
	})
}

func assertBindingRequest(t *testing.T, request connector.SummaryRequest, account *domain.Account, actor, operation string) {
	t.Helper()
	if request.AccountID != account.ID || request.CanonicalUID != account.CanonicalUID || request.TenantID != account.TenantID || request.Region != account.HomeRegion {
		t.Fatalf("bound identity = %#v, want %v/%q/%q/%q", request, account.ID, account.CanonicalUID, account.TenantID, account.HomeRegion)
	}
	if request.ActorUID != actor || request.OperationID != operation {
		t.Fatalf("workflow identity = %#v, want actor=%q operation=%q", request, actor, operation)
	}
}

type bindingMigrationRepository struct {
	migrationAccountRepository
	mu      sync.Mutex
	tenant  string
	issuer  string
	subject string
}

func (r *bindingMigrationRepository) FindByIdentity(ctx context.Context, tenant, issuer, subject string) (*domain.Account, error) {
	r.mu.Lock()
	r.tenant, r.issuer, r.subject = tenant, issuer, subject
	r.mu.Unlock()
	return r.migrationAccountRepository.FindByIdentity(ctx, tenant, issuer, subject)
}

func TestBoundServicePathsRejectMismatchedAuthoritativeAccountsWithoutSourceCalls(t *testing.T) {
	requested := bindingAccount(51, "tenant-a", "uid-a", "west-1")
	wrong := bindingAccount(52, "tenant-b", "uid-b", "east-1")
	datasets := []domain.AccountDataset{"aero-vault.tenants"}

	t.Run("aggregation", func(t *testing.T) {
		client := &bindingConnector{source: connector.SourceAeroVault}
		result := &domain.AggregateOverview{SourceErrors: map[string]string{}}
		svc := &aggregationService{authoritativeAccounts: &bindingLoader{account: wrong}, application: &bindingApplication{}}
		svc.refreshSource(context.Background(), requested, connector.SourceAeroVault, client, datasets, result, &sync.Mutex{})
		if result.SourceErrors[string(connector.SourceAeroVault)] != "source.target_binding_invalid" || client.calls.Load() != 0 {
			t.Fatalf("result=%v source calls=%d", result.SourceErrors, client.calls.Load())
		}
	})

	t.Run("sync", func(t *testing.T) {
		job := &domain.SyncJob{ID: domain.ID{61}, AccountID: requested.ID, TenantID: requested.TenantID}
		_, err := syncSummaryRequest(context.Background(), &bindingLoader{account: wrong}, wrong, job, connector.SourceAeroVault, datasets)
		if err == nil || err.Error() != "trusted summary account binding is unavailable" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("operation", func(t *testing.T) {
		runner := &connectorOperationRunner{accounts: &migrationAccountRepository{account: wrong}, application: &bindingApplication{}}
		client := &bindingConnector{source: connector.SourceAeroVault}
		op := &domain.Operation{ID: domain.ID{71}, AccountID: requested.ID, TenantID: requested.TenantID}
		_, _, err := runner.sync(context.Background(), op, &domain.OperationStep{Source: string(connector.SourceAeroVault), Action: "summary"}, client, wrong, datasets)
		if err == nil || err.Error() != "trusted summary account binding is unavailable" || client.calls.Load() != 0 {
			t.Fatalf("error=%v source calls=%d", err, client.calls.Load())
		}
	})

	t.Run("legacy", func(t *testing.T) {
		client := &bindingConnector{source: connector.SourceAeroVault}
		repo := &bindingMigrationRepository{migrationAccountRepository: migrationAccountRepository{account: wrong}}
		svc := &legacyMigrationService{legacy: &fakeLegacyMigrationRepo{}, connectors: map[connector.Source]connector.Connector{
			connector.SourceAeroVault: client}, accountRepo: repo, accountIssuer: "issuer-a", defaultLimit: 1000}
		result := &LegacyCompareResult{SourceErrors: map[string]string{}}
		ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "actor", TenantID: requested.TenantID})
		if err := svc.compareWithSource(ctx, LegacyCompareInput{OwnerUID: "owner", Source: connector.SourceAeroVault}, result); err == nil || client.calls.Load() != 0 {
			t.Fatalf("error=%v source calls=%d", err, client.calls.Load())
		}
	})
}

var _ connector.AuthoritativeAccountLoader = (*bindingLoader)(nil)
var _ repository.AccountRepository = (*bindingMigrationRepository)(nil)
