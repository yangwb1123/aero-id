package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type syncGateRepo struct {
	*minimalSyncJobRepo
	repository.SyncJobRecoveryRepository
	mu       sync.Mutex
	job      *domain.SyncJob
	facts    []*domain.AuditFact
	deferErr error
	replays  []repository.SummaryReplay
	outcomes []repository.EraseOutcome
	checks   int
}

func (r *syncGateRepo) CheckExecutionLease(_ context.Context, job *domain.SyncJob, _ string, _ bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks++
	if r.job.Status != domain.SyncJobRunning || job.LeaseOwner != r.job.LeaseOwner || job.LeaseKind != r.job.LeaseKind || r.job.LeaseUntil == nil || !r.job.LeaseUntil.After(time.Now()) {
		return repository.ErrSyncJobLeaseLost
	}
	return nil
}
func (r *syncGateRepo) DeferExecutionWithAudit(_ context.Context, input repository.SyncJobExecutionDeferral, fact *domain.AuditFact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deferErr != nil {
		return r.deferErr
	}
	if r.job.LeaseOwner != input.Job.LeaseOwner || r.job.Status != domain.SyncJobRunning {
		return repository.ErrSyncJobLeaseLost
	}
	r.job.Status = domain.SyncJobUnknown
	if input.BeforeEffects && input.Job.LeaseKind == "execute" && len(r.job.Result) == 0 && len(r.replays) == 0 && len(r.outcomes) == 0 {
		r.job.Status = domain.SyncJobPending
	}
	r.job.LeaseOwner = ""
	r.job.LeaseKind = ""
	r.job.LeaseUntil = nil
	r.job.ErrorCode = input.Code
	r.job.ErrorMessage = input.Message
	if input.Result != nil {
		r.job.Result = input.Result
	}
	r.facts = append(r.facts, fact)
	return nil
}
func (r *syncGateRepo) SaveSummaryReplay(_ context.Context, input repository.SummaryReplayInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replays = append(r.replays, repository.SummaryReplay{JobID: input.JobID, Source: input.Source, ExecutionAttempt: input.ExecutionAttempt, Request: input.Request, State: "pending"})
	return nil
}
func (r *syncGateRepo) ListSummaryReplays(context.Context, domain.ID) ([]repository.SummaryReplay, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]repository.SummaryReplay(nil), r.replays...), nil
}
func (r *syncGateRepo) ResolveSummaryReplay(_ context.Context, _ domain.ID, source string, attempt int, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.replays {
		if r.replays[i].Source == source && r.replays[i].ExecutionAttempt == attempt {
			r.replays[i].State = "resolved"
		}
	}
	return nil
}
func (r *syncGateRepo) EnsureEraseOutcomes(_ context.Context, id domain.ID, _ string, inputs []repository.EraseOutcomeInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.outcomes) == 0 {
		for _, input := range inputs {
			r.outcomes = append(r.outcomes, repository.EraseOutcome{JobID: id, Source: input.Source, RequestedDatasets: input.RequestedDatasets, Status: "pending", Result: map[string]any{}})
		}
	}
	return nil
}
func (r *syncGateRepo) ListEraseOutcomes(context.Context, domain.ID) ([]repository.EraseOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]repository.EraseOutcome(nil), r.outcomes...), nil
}
func (r *syncGateRepo) CheckpointEraseOutcome(_ context.Context, input repository.EraseOutcomeCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.outcomes {
		if r.outcomes[i].Source == input.Source {
			r.outcomes[i].Status = input.Status
			r.outcomes[i].SourceOperationID = input.SourceOperationID
			r.outcomes[i].Result = input.Result
			r.outcomes[i].ErrorCode = input.ErrorCode
			return nil
		}
	}
	return repository.ErrSyncOutcomeConflict
}

type syncGateAccount struct {
	*exportAccountFixture
	erased int
}

func (a *syncGateAccount) EraseAccountWithSyncJobLease(context.Context, string, domain.ID, domain.ID, string) error {
	a.erased++
	a.account.Status = domain.AccountErased
	return nil
}

type syncGateSource struct {
	summaries, erases, queries            atomic.Int32
	afterSummary, afterErase, afterStatus func()
	status                                string
}

func (*syncGateSource) Source() connector.Source     { return connector.SourceSnaplink }
func (*syncGateSource) Health(context.Context) error { return nil }
func (c *syncGateSource) Summary(context.Context, connector.SummaryRequest) (*connector.Summary, error) {
	c.summaries.Add(1)
	if c.afterSummary != nil {
		c.afterSummary()
	}
	return &connector.Summary{Source: connector.SourceSnaplink, SourceRegion: "local", Version: 9, Complete: true, Coverage: domain.SummaryCoverage{Datasets: []domain.AccountDataset{"snaplink.identity"}, Explicit: true}}, nil
}
func (c *syncGateSource) Erase(_ context.Context, request connector.EraseRequest) (*connector.CommandResult, error) {
	c.erases.Add(1)
	if c.afterErase != nil {
		c.afterErase()
	}
	return &connector.CommandResult{OperationID: request.OperationID, SourceOperationID: "original-source-op", Status: "completed", Result: map[string]any{"version": 7}}, nil
}
func (c *syncGateSource) OperationStatus(_ context.Context, request connector.OperationStatusRequest) (*connector.CommandResult, error) {
	c.queries.Add(1)
	if c.afterStatus != nil {
		c.afterStatus()
	}
	return &connector.CommandResult{OperationID: request.OperationID, SourceOperationID: request.SourceOperationID, Status: c.status}, nil
}

type syncGateApplication struct{ applied, reconciled int }

func (a *syncGateApplication) ApplySummary(context.Context, domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	a.applied++
	return domain.SummaryApplicationResult{Applied: true, Outcome: domain.SummaryApplied}, nil
}
func (a *syncGateApplication) ReconcileSummary(context.Context, domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	a.reconciled++
	return domain.SummaryApplicationResult{Applied: true, Outcome: domain.SummaryApplied}, nil
}
func armSyncGateJob(job *domain.SyncJob, kind string) *domain.SyncJob {
	job.Status = domain.SyncJobRunning
	job.LeaseOwner = idgen.NewID().String()
	job.LeaseKind = kind
	job.Attempt++
	until := time.Now().Add(time.Minute)
	job.LeaseUntil = &until
	copy := *job
	return &copy
}
func syncExecutionFixture(t *testing.T, kind domain.SyncJobType) (*syncJobService, *domain.SyncJob, *syncGateRepo, *syncGateSource, *syncGateAccount, *atomic.Bool) {
	t.Helper()
	svc, job, account, _, _ := newScopedExportFixture(t, 0)
	job.JobType = kind
	job.IdempotencyKey = "original-intent"
	job.Request = map[string]any{"datasets": []domain.AccountDataset{"snaplink.identity"}}
	claimed := armSyncGateJob(job, "execute")
	repo := &syncGateRepo{minimalSyncJobRepo: svc.repo.(*minimalSyncJobRepo), job: job}
	source := &syncGateSource{status: "completed"}
	allowed := &atomic.Bool{}
	allowed.Store(true)
	acct := &syncGateAccount{exportAccountFixture: account}
	svc.repo = repo
	svc.accountRepo = acct
	svc.connectors = map[connector.Source]connector.Connector{connector.SourceSnaplink: source}
	svc.application = &syncGateApplication{}
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return allowed.Load(), nil })
	return svc, claimed, repo, source, acct, allowed
}
