package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type intentAccountRepo struct {
	repository.AccountRepository
	account *domain.Account
}

func (r *intentAccountRepo) GetByID(_ context.Context, tenant string, id domain.ID) (*domain.Account, error) {
	if r.account.ID != id || r.account.TenantID != tenant {
		return nil, repository.ErrNotFound
	}
	copy := *r.account
	return &copy, nil
}

type intentMemoryRepo struct {
	mu                  sync.Mutex
	intent              *domain.CommandIntent
	preconditions       domain.CommandPreconditions
	creates, executions int
	job                 *domain.SyncJob
}

func (r *intentMemoryRepo) Capture(context.Context, string, domain.ID, []domain.AccountDataset) (domain.CommandPreconditions, error) {
	return r.preconditions, nil
}
func (r *intentMemoryRepo) CreateWithAudit(_ context.Context, intent *domain.CommandIntent, fact *domain.AuditFact) error {
	if fact == nil {
		panic("intent create without audit")
	}
	r.creates++
	copy := *intent
	r.intent = &copy
	return nil
}
func (r *intentMemoryRepo) Get(_ context.Context, p domain.CommandPrincipal, id domain.ID) (*domain.CommandIntent, error) {
	if r.intent == nil || r.intent.Principal != p || r.intent.ID != id {
		return nil, repository.ErrNotFound
	}
	copy := *r.intent
	return &copy, nil
}
func (r *intentMemoryRepo) FindByKey(ctx context.Context, p domain.CommandPrincipal, key string) (*domain.CommandIntent, error) {
	if r.intent == nil || r.intent.IdempotencyKey != key {
		return nil, repository.ErrNotFound
	}
	return r.Get(ctx, p, r.intent.ID)
}
func (r *intentMemoryRepo) Abandon(_ context.Context, input repository.CommandIntentAbandonment) (*domain.CommandIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.intent.ConfirmationDigest != input.ConfirmationDigest || input.ExpectedVersion != 1 {
		return nil, repository.ErrCommandIntentConflict
	}
	if r.intent.Status == "prepared" {
		if input.Fact == nil {
			panic("abandonment without transactional audit")
		}
		now := time.Now().UTC()
		r.intent.Status, r.intent.Version, r.intent.AbandonedAt = "abandoned", 2, &now
	}
	copy := *r.intent
	return &copy, nil
}
func (r *intentMemoryRepo) Execute(_ context.Context, input repository.CommandIntentExecution) (*domain.CommandIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.intent.ConfirmationDigest != input.ConfirmationDigest {
		return nil, repository.ErrCommandIntentConflict
	}
	if r.intent.Status == "abandoned" {
		return nil, repository.ErrCommandIntentAbandoned
	}
	if r.intent.Status == "executed" {
		copy := *r.intent
		return &copy, nil
	}
	if r.intent.Version != input.ExpectedVersion {
		return nil, repository.ErrCommandIntentConflict
	}
	if !time.Now().Before(r.intent.ExpiresAt) {
		return nil, repository.ErrCommandIntentExpired
	}
	if !sameTestPreconditions(r.preconditions, r.intent.Preconditions) {
		return nil, repository.ErrCommandIntentStale
	}
	if input.RequestedFact == nil || input.ExecutedFact == nil {
		panic("execution without transactional facts")
	}
	r.executions++
	r.job = input.Job
	r.intent.Status, r.intent.Version, r.intent.ReceiptKind, r.intent.ReceiptID = "executed", 2, "sync_job", input.Job.ID
	copy := *r.intent
	return &copy, nil
}
func sameTestPreconditions(a, b domain.CommandPreconditions) bool {
	return a.WriterEpoch == b.WriterEpoch && a.AccountStatus == b.AccountStatus && a.HomeRegion == b.HomeRegion
}

func newIntentFixture(t *testing.T) (*commandIntentService, *intentMemoryRepo, *intentAccountRepo, CommandIntentInput) {
	t.Helper()
	accounts := &intentAccountRepo{account: &domain.Account{ID: domain.ID{1}, TenantID: "tenant-a", CanonicalUID: "actor-1", HomeRegion: "local", Status: domain.AccountActive}}
	repo := &intentMemoryRepo{preconditions: domain.CommandPreconditions{HomeRegion: "local", AccountStatus: domain.AccountActive,
		ResolverVersion: "v1", Snapshots: []domain.CommandSnapshotVersion{}}}
	jobs := NewSyncJobServiceWithOptions(&minimalSyncJobRepo{}, nil, accounts, nil, nil, nil, nil,
		WithSyncJobPermission(allowAllPermissions{}))
	service := NewCommandIntentService(repo, nil, accounts, nil, jobs, allowAllPermissions{}, CommandIntentConfig{Region: "local", ExportEnabled: true,
		Sources: map[string]SourceCapabilityConfig{"aero-vault": {SummaryEnabled: true}}}).(*commandIntentService)
	input := CommandIntentInput{AccountID: accounts.account.ID, IdempotencyKey: "preview-key", Plan: domain.CommandPlan{Kind: "sync",
		Datasets: []domain.AccountDataset{"aero-vault.usage", "aero-vault.buckets"}}}
	return service, repo, accounts, input
}

func TestCommandIntentPreviewIsScopedNormalizedAndHasNoReceipt(t *testing.T) {
	svc, repo, _, input := newIntentFixture(t)
	intent, err := svc.Create(fingerprintContext(t, "console"), input)
	if err != nil {
		t.Fatal(err)
	}
	if repo.executions != 0 || repo.job != nil || intent.Plan.Datasets[0] != "aero-vault.buckets" ||
		intent.AuthorizationVersion != "unsupported" || intent.ResourceAuthorization != "unknown" || intent.ConfirmationDigest == "" {
		t.Fatal("preview granted authority, executed or omitted its normalized plan")
	}
	input.Plan.Datasets[0], input.Plan.Datasets[1] = input.Plan.Datasets[1], input.Plan.Datasets[0]
	retry, err := svc.Create(fingerprintContext(t, "console"), input)
	if err != nil || retry.ID != intent.ID || repo.creates != 1 {
		t.Fatal("same preview intent was duplicated", err)
	}
	input.Plan.Datasets = []domain.AccountDataset{"aero-vault.usage"}
	if _, err := svc.Create(fingerprintContext(t, "console"), input); requireAppError(t, err).Code != "command.idempotency_conflict" {
		t.Fatal("changed request reused preview")
	}
	if _, err := svc.Get(fingerprintContext(t, "other-console"), intent.ID); requireAppError(t, err).Code != "command.not_found" {
		t.Fatal("cross-client preview access")
	}
}

func TestCommandIntentConfirmedExecutionReturnsOneDurableReceipt(t *testing.T) {
	svc, repo, _, input := newIntentFixture(t)
	ctx := fingerprintContext(t, "console")
	intent, err := svc.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, intent.ID, 1, "wrong"); requireAppError(t, err).Code != "command.confirmation_conflict" {
		t.Fatal("wrong confirmation")
	}
	first, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil || first.ReceiptID != retry.ReceiptID || repo.executions != 1 || repo.job.IdempotencyKey != commandIntentReceiptKey(intent.ID) {
		t.Fatal("execution duplicated receipt", err)
	}
}

func TestCommandIntentExecutionRejectsExpiryFencingAndCurrentAuthFailure(t *testing.T) {
	for _, condition := range []string{"expired", "writer-changed", "denied", "outage", "source-disabled"} {
		t.Run(condition, func(t *testing.T) {
			svc, repo, accounts, input := newIntentFixture(t)
			ctx := fingerprintContext(t, "console")
			intent, err := svc.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			switch condition {
			case "expired":
				repo.intent.ExpiresAt = time.Now().Add(-time.Second)
			case "writer-changed":
				repo.preconditions.WriterEpoch++
			case "source-disabled":
				svc.config.Sources = nil
			case "denied", "outage":
				accounts.account.CanonicalUID = "other-actor"
				svc.permission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
					if condition == "outage" {
						return false, errors.New("private authz endpoint")
					}
					return false, nil
				})
			}
			if _, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest); err == nil || repo.executions != 0 {
				t.Fatal("invalid preview executed", err)
			}
		})
	}
}

func TestCommandIntentRejectsUIDOnlyAndImplicitScope(t *testing.T) {
	svc, repo, _, input := newIntentFixture(t)
	if _, err := svc.Create(WithActor(t.Context(), "actor-1"), input); requireAppError(t, err).Code != "auth.claims_required" {
		t.Fatal("UID-only preview")
	}
	input.Plan.Datasets = nil
	if _, err := svc.Create(fingerprintContext(t, "console"), input); err == nil || repo.creates != 0 {
		t.Fatal("implicit all-dataset preview")
	}
}
