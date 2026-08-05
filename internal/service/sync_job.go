package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
	"gorm.io/gorm"
)

type SyncJobService interface {
	Create(context.Context, SyncJobInput) (*domain.SyncJob, error)
	Get(context.Context, domain.ID) (*domain.SyncJob, error)
	List(context.Context, domain.Page) ([]*domain.SyncJob, string, error)
	Run(context.Context, domain.ID, string) (*domain.SyncJob, error)
	Reconcile(context.Context, domain.ID, string) (*domain.SyncJob, error)
	DownloadExport(context.Context, domain.ID) ([]byte, error)
	Start(context.Context, time.Duration) func()
}

type SyncJobInput struct {
	AccountID      domain.ID
	JobType        domain.SyncJobType
	Datasets       []domain.AccountDataset
	IdempotencyKey string
}

type syncJobService struct {
	repo         repository.SyncJobRepository
	accounts     AccountService
	accountRepo  repository.AccountRepository
	snapshots    repository.SnapshotRepository
	store        storage.FileStorage
	connectors   map[connector.Source]connector.Connector
	permission   PermissionChecker
	audit        AuditLedgerService
	auditLedger  repository.AuditLedgerRepository // optional; enables audit_export jobs (P4-08)
	exportKey    []byte
	exportTTL    time.Duration
	maxExport    int64
	region       string
	metrics      ControlPlaneMetrics
}

type SyncJobOption func(*syncJobService)

func WithSyncJobPermission(checker PermissionChecker) SyncJobOption {
	return func(s *syncJobService) { s.permission = checker }
}

func WithSyncJobAudit(audit AuditLedgerService) SyncJobOption {
	return func(s *syncJobService) { s.audit = audit }
}

func WithSyncExportConfig(encryptionKey string, maxBytes int64, ttl time.Duration) SyncJobOption {
	return func(s *syncJobService) {
		s.exportKey = deriveExportKey(encryptionKey)
		if maxBytes > 0 {
			s.maxExport = maxBytes
		}
		if ttl > 0 {
			s.exportTTL = ttl
		}
	}
}

// WithSyncJobMetrics attaches the control-plane observability sink.
func WithSyncJobMetrics(metrics ControlPlaneMetrics) SyncJobOption {
	return func(s *syncJobService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

// WithSyncJobAuditLedger wires the durable ledger for audit_export jobs (P4-08).
func WithSyncJobAuditLedger(ledger repository.AuditLedgerRepository) SyncJobOption {
	return func(s *syncJobService) { s.auditLedger = ledger }
}

func WithSyncJobRegion(region string) SyncJobOption {
	return func(s *syncJobService) { s.region = region }
}

func NewSyncJobService(repo repository.SyncJobRepository, accounts AccountService, accountRepo repository.AccountRepository,
	snapshots repository.SnapshotRepository, store storage.FileStorage, connectors ...connector.Connector) SyncJobService {
	return NewSyncJobServiceWithOptions(repo, accounts, accountRepo, snapshots, store, connectors)
}

func NewSyncJobServiceWithOptions(repo repository.SyncJobRepository, accounts AccountService, accountRepo repository.AccountRepository,
	snapshots repository.SnapshotRepository, store storage.FileStorage, connectors []connector.Connector, opts ...SyncJobOption) SyncJobService {
	bySource := make(map[connector.Source]connector.Connector, len(connectors))
	for _, item := range connectors {
		if item != nil {
			bySource[item.Source()] = item
		}
	}
	service := &syncJobService{repo: repo, accounts: accounts, accountRepo: accountRepo, snapshots: snapshots, store: store,
		connectors: bySource, exportTTL: 15 * time.Minute, maxExport: 64 << 20, region: "local"}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

//nolint:cyclop,funlen // sync creation validates tenant scope, idempotency, datasets, and home-region routing.
func (s *syncJobService) Create(ctx context.Context, input SyncJobInput) (*domain.SyncJob, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if !validSyncJobType(input.JobType) {
		return nil, apperr.BadRequest("sync.invalid_type", "sync job type is not supported")
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 200 {
		return nil, apperr.BadRequest("sync.invalid_idempotency_key", "idempotency key is required")
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	if existing, err := s.repo.FindByIdempotency(ctx, actor, tenantID, input.IdempotencyKey); err == nil {
		return existing, nil
	}
	if input.AccountID == (domain.ID{}) {
		view, err := s.accounts.GetMe(ctx)
		if err != nil {
			return nil, err
		}
		input.AccountID = view.Account.ID
	}
	if err := authorizeAccountTarget(ctx, s.accountRepo, s.permission, input.AccountID, actor, syncPermission(input.JobType)); err != nil {
		return nil, err
	}
	if err := requireHomeRegion(ctx, s.accountRepo, input.AccountID, s.region); err != nil {
		return nil, err
	}
	datasets, err := normalizeDatasets(input.Datasets)
	if err != nil {
		return nil, err
	}
	request := map[string]any{"datasets": datasets}
	job := &domain.SyncJob{AccountID: input.AccountID, ActorUID: actor, TenantID: tenantID,
		RequestID: RequestIDFrom(ctx), JobType: input.JobType, Status: domain.SyncJobPending,
		IdempotencyKey: input.IdempotencyKey, Request: request, Result: map[string]any{}}
	job.ID = idgen.NewID()
	atomicAudit, err := s.createWithAudit(ctx, job)
	if err != nil {
		if existing, findErr := s.repo.FindByIdempotency(ctx, actor, tenantID, input.IdempotencyKey); findErr == nil {
			return existing, nil
		}
		return nil, apperr.Internal("sync.create_failed", "failed to create sync job").Wrap(err)
	}
	if s.audit != nil && !atomicAudit {
		if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: job.ID, RequestID: job.RequestID, ActorUID: actor,
			AccountID: job.AccountID, TenantID: job.TenantID, Action: "sync.job.requested", ResourceType: "sync_job", ResourceID: job.ID.String(),
			Payload: map[string]any{"job_type": job.JobType, "datasets": datasets}}); err != nil {
			return nil, apperr.Internal("sync.audit_failed", "failed to record sync job audit").Wrap(err)
		}
	}
	return job, nil
}

func (s *syncJobService) Get(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("sync.not_found", "sync job not found").Wrap(err)
	}
	if job.TenantID != tenantID {
		return nil, apperr.NotFound("sync.not_found", "sync job not found")
	}
	if job.ActorUID != actor {
		if s.permission == nil {
			return nil, apperr.Forbidden("sync.forbidden", "sync job is not owned by actor")
		}
		allowed, permissionErr := s.permission.HasPermission(ctx, actor, syncPermission(job.JobType))
		if permissionErr != nil {
			return nil, apperr.Internal("sync.permission_check_failed", "failed to verify sync permission").Wrap(permissionErr)
		}
		if !allowed {
			return nil, apperr.Forbidden("sync.forbidden", "actor cannot access this sync job")
		}
	}
	return job, nil
}

func (s *syncJobService) List(ctx context.Context, page domain.Page) ([]*domain.SyncJob, string, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	items, next, err := s.repo.List(ctx, actor, tenantID, page)
	if err != nil {
		return nil, "", apperr.Internal("sync.list_failed", "failed to list sync jobs").Wrap(err)
	}
	return items, next, nil
}

func (s *syncJobService) Run(ctx context.Context, id domain.ID, worker string) (*domain.SyncJob, error) {
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := requireHomeRegion(ctx, s.accountRepo, job.AccountID, s.region); err != nil {
		return nil, err
	}
	claimed, err := s.repo.Claim(ctx, job.ID, workerOrDefault(worker), 5*time.Minute)
	if err != nil {
		return nil, apperr.Conflict("sync.not_runnable", "sync job is already running or completed").Wrap(err)
	}
	return s.execute(ctx, claimed)
}

//nolint:cyclop // the worker loop coordinates normal execution and unknown erase reconciliation.
func (s *syncJobService) Start(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		lastExportCleanup := time.Time{}
		for {
			select {
			case <-ticker.C:
				if lastExportCleanup.IsZero() || time.Since(lastExportCleanup) >= time.Minute {
					s.cleanupExpiredExports(workerCtx)
					lastExportCleanup = time.Now()
				}
				for i := 0; i < 4; i++ {
					job, err := s.claimNext(workerCtx, "sync-worker", 5*time.Minute)
					if err != nil {
						if errors.Is(err, repository.ErrSyncJobUnknown) {
							continue
						}
						if errors.Is(err, gorm.ErrRecordNotFound) {
							break
						}
						break
					}
					if _, err := s.execute(workerCtx, job); err != nil {
						break
					}
				}
				for i := 0; i < 4; i++ {
					job, err := s.claimUnknownNext(workerCtx, "sync-reconciler", 5*time.Minute)
					if errors.Is(err, gorm.ErrRecordNotFound) {
						break
					}
					if err != nil {
						break
					}
					if _, err := s.reconcileClaimed(workerCtx, job); err != nil {
						break
					}
				}
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *syncJobService) execute(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	if job.JobType == domain.SyncJobExport {
		return s.executeExport(ctx, job)
	}
	if job.JobType == domain.SyncJobAuditExport {
		return s.executeAuditExport(ctx, job)
	}
	if job.JobType == domain.SyncJobErase {
		return s.executeErase(ctx, job)
	}
	return s.executeSync(ctx, job)
}

//nolint:cyclop,funlen // sync execution aggregates concurrent source outcomes into a durable partial result.
func (s *syncJobService) executeSync(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	datasets := datasetsFromRequest(job.Request)
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		_ = s.finishJob(ctx, job, domain.SyncJobFailed, "sync.account_load_failed", "failed to load account", nil)
		return nil, apperr.Internal("sync.account_load_failed", "failed to load account").Wrap(err)
	}
	result := map[string]any{"mode": string(job.JobType), "sources": map[string]any{}}
	var wait sync.WaitGroup
	var mu sync.Mutex
	var failures int
	for source, client := range s.connectors {
		requested := datasetsForSource(datasets, string(source))
		if len(requested) == 0 {
			continue
		}
		wait.Add(1)
		go func(source connector.Source, client connector.Connector, requested []domain.AccountDataset) {
			defer wait.Done()
			before, listErr := s.snapshots.ListSnapshots(ctx, account.ID, requested)
			if listErr != nil {
				mu.Lock()
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "projection_read_failed"}
				mu.Unlock()
				return
			}
			summary, callErr := client.Summary(ctx, syncSummaryRequest(account, job, requested))
			mu.Lock()
			defer mu.Unlock()
			if callErr != nil {
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "unavailable", "error_code": "source.unavailable"}
				return
			}
			if summary == nil {
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "invalid_response", "error_code": "source.empty_summary"}
				return
			}
			if persistErr := s.persistSyncSummary(ctx, job, account, source, requested, summary); persistErr != nil {
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "projection_error"}
				return
			}
			status := map[string]any{"status": "synced", "count": len(summary.Snapshots), "complete": summary.Complete}
			if job.JobType == domain.SyncJobReconcile {
				drift := snapshotDrift(before, summary.Snapshots)
				status["drift_count"] = drift
				if summary.Complete {
					status["repaired_count"] = drift
				} else {
					status["repaired_count"] = 0
				}
			}
			result["sources"].(map[string]any)[string(source)] = status
		}(source, client, requested)
	}
	wait.Wait()
	status := domain.SyncJobCompleted
	if len(s.connectors) == 0 || failures > 0 {
		status = domain.SyncJobPartiallyComplete
	}
	if err := s.finishJob(ctx, job, status, "", "", result); err != nil {
		return nil, apperr.Internal("sync.finish_failed", "failed to finish sync job").Wrap(err)
	}
	return s.repo.Get(ctx, job.ID)
}

//nolint:cyclop,funlen // erase execution preserves unknown remote outcomes before local deletion.
func (s *syncJobService) executeErase(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		_ = s.finishJob(ctx, job, domain.SyncJobFailed, "erase.account_load_failed", "failed to load account", nil)
		return nil, apperr.Internal("erase.account_load_failed", "failed to load account").Wrap(err)
	}
	datasets := datasetsFromRequest(job.Request)
	result := map[string]any{"sources": map[string]any{}}
	statuses := result["sources"].(map[string]any)
	unknown := false
	failures := 0
	var wait sync.WaitGroup
	var mu sync.Mutex
	required := make(map[connector.Source][]domain.AccountDataset)
	for _, dataset := range datasets {
		if descriptor, ok := domain.DatasetDescriptorOf(dataset); ok {
			source := connector.Source(descriptor.Source)
			required[source] = append(required[source], dataset)
		}
	}
	for source, requested := range required {
		client, configured := s.connectors[source]
		if !configured {
			statuses[string(source)] = map[string]any{"status": "not_configured"}
			unknown = true
			continue
		}
		command, ok := client.(connector.CommandConnector)
		if !ok {
			statuses[string(source)] = map[string]any{"status": "not_configured"}
			unknown = true
			continue
		}
		wait.Add(1)
		go func(source connector.Source, command connector.CommandConnector, requested []domain.AccountDataset) {
			defer wait.Done()
			commandResult, callErr := command.Erase(ctx, eraseRequestForJob(account, job, source, requested))
			mu.Lock()
			defer mu.Unlock()
			if callErr != nil {
				failures++
				if isUnknownRemote(callErr) {
					unknown = true
				}
				statuses[string(source)] = map[string]any{"status": "failed", "error_code": remoteErrorCode(callErr)}
				return
			}
			if commandResult == nil {
				unknown = true
				statuses[string(source)] = map[string]any{"status": "unknown", "error_code": "source.empty_command_result"}
				return
			}
			commandStatus := normalizedCommandStatus(commandResult.Status)
			statuses[string(source)] = map[string]any{"status": commandStatus, "source_operation_id": commandResult.SourceOperationID}
			if !commandSucceeded(commandStatus) {
				if eraseFailureStatus(commandStatus) {
					failures++
				} else {
					unknown = true
				}
			}
		}(source, command, requested)
	}
	wait.Wait()
	status := domain.SyncJobCompleted
	code, message := "", ""
	if unknown {
		status = domain.SyncJobUnknown
		code, message = "erase.unknown_outcome", "one or more source deletion outcomes require reconciliation"
	} else if failures > 0 {
		status = domain.SyncJobPartiallyComplete
		code, message = "erase.partial_failure", "one or more source deletion commands failed"
	} else if err := s.accountRepo.EraseAccount(ctx, job.TenantID, job.AccountID); err != nil {
		status = domain.SyncJobPartiallyComplete
		code, message = "erase.local_projection_failed", "source deletion completed but local erasure failed"
	}
	if err := s.finishJob(ctx, job, status, code, message, result); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, job.ID)
}

func (s *syncJobService) finishJob(ctx context.Context, job *domain.SyncJob, status domain.SyncJobStatus, code, message string, result map[string]any) error {
	if s.audit != nil {
		if repo, ok := s.repo.(repository.SyncJobAuditRepository); ok {
			return repo.FinishWithAudit(ctx, job.ID, job.LeaseOwner, status, code, message, result,
				syncJobFinishedAudit(ctx, s.region, job, status, code, result))
		}
	}
	if err := s.repo.Finish(ctx, job.ID, job.LeaseOwner, status, code, message, result); err != nil {
		return err
	}
	if s.audit == nil {
		return nil
	}
	return s.audit.Record(ctx, AuditFactInput{Source: "aero-id", SourceRegion: s.region, EventID: idgen.NewID(), RequestID: job.RequestID,
		ActorUID: job.ActorUID, AccountID: job.AccountID, TenantID: job.TenantID, Action: "sync.job.finished",
		ResourceType: "sync_job", ResourceID: job.ID.String(), Payload: map[string]any{"job_type": job.JobType,
			"status": status, "error_code": code}})
}

func remoteErrorCode(err error) string {
	if remote, ok := connector.IsRemote(err); ok && remote.Code != "" {
		return remote.Code
	}
	return "source.command_failed"
}
