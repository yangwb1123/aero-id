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
	DownloadExport(context.Context, domain.ID, bool) ([]byte, error) // oneTime=true burns the artifact after one successful read (P7-06)
	Start(context.Context, time.Duration) func()
}

type SyncJobInput struct {
	AccountID                 domain.ID
	JobType                   domain.SyncJobType
	Datasets                  []domain.AccountDataset
	IdempotencyKey            string
	AuditExportContinuationID domain.ID
}

type syncJobService struct {
	repo        repository.SyncJobRepository
	accounts    AccountService
	accountRepo repository.AccountRepository
	snapshots   repository.SnapshotRepository
	application repository.SummaryApplication
	store       storage.FileStorage
	connectors  map[connector.Source]connector.Connector
	permission  PermissionChecker
	audit       AuditLedgerService
	auditLedger repository.AuditLedgerRepository // optional; enables audit_export jobs (P4-08)
	exportKey   []byte
	exportTTL   time.Duration
	maxExport   int64
	region      string
	metrics     ControlPlaneMetrics
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
	snapshots repository.SnapshotRepository, application repository.SummaryApplication, store storage.FileStorage,
	connectors ...connector.Connector) SyncJobService {
	return NewSyncJobServiceWithOptions(repo, accounts, accountRepo, snapshots, application, store, connectors)
}

func NewSyncJobServiceWithOptions(repo repository.SyncJobRepository, accounts AccountService, accountRepo repository.AccountRepository,
	snapshots repository.SnapshotRepository, application repository.SummaryApplication, store storage.FileStorage,
	connectors []connector.Connector, opts ...SyncJobOption) SyncJobService {
	bySource := make(map[connector.Source]connector.Connector, len(connectors))
	for _, item := range connectors {
		if item != nil {
			bySource[item.Source()] = item
		}
	}
	service := &syncJobService{repo: repo, accounts: accounts, accountRepo: accountRepo, snapshots: snapshots,
		application: application, store: store, connectors: bySource, exportTTL: 15 * time.Minute, maxExport: 64 << 20, region: "local"}
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
	continuationRequested := input.AuditExportContinuationID != (domain.ID{})
	if len(input.IdempotencyKey) > 200 || (!continuationRequested && strings.TrimSpace(input.IdempotencyKey) == "") {
		return nil, apperr.BadRequest("sync.invalid_idempotency_key", "idempotency key is required")
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	var continuation *domain.SyncJob
	if input.AuditExportContinuationID != (domain.ID{}) {
		if input.JobType != domain.SyncJobAuditExport {
			return nil, apperr.BadRequest("sync.invalid_continuation", "audit export continuation is only valid for audit exports")
		}
		if input.AccountID == (domain.ID{}) {
			view, err := s.accounts.GetMe(ctx)
			if err != nil {
				return nil, err
			}
			input.AccountID = view.Account.ID
		}
		continuation, err = s.loadAuditExportContinuation(ctx, input.AuditExportContinuationID, actor, tenantID, input.AccountID)
		if err != nil {
			return nil, err
		}
		// A continuation is a one-use server-side reference. Deriving its
		// idempotency key from the parent makes retries return the same child
		// job instead of re-reading the same cursor into a second window.
		input.IdempotencyKey = auditExportContinuationKey(continuation.ID)
	}
	existing, findErr := s.repo.FindByIdempotency(ctx, actor, tenantID, input.IdempotencyKey)
	if findErr == nil {
		if continuation != nil && !auditExportJobReferences(existing, continuation.ID) {
			return nil, apperr.Conflict("sync.continuation_conflict", "audit export continuation has already been used")
		}
		return existing, nil
	}
	if !errors.Is(findErr, repository.ErrNotFound) && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return nil, apperr.Internal("sync.idempotency_lookup_failed", "failed to check sync idempotency").Wrap(findErr)
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
	var datasets []domain.AccountDataset
	if input.JobType == domain.SyncJobErase {
		// Preserve the public empty erase request as an explicit full-scope
		// operation, but persist the expanded registry so execution never has
		// to interpret an empty list as "erase everything".
		if len(input.Datasets) == 0 {
			datasets = allDatasets()
		} else {
			if err := validateDestructiveDatasetInput(input.Datasets); err != nil {
				return nil, err
			}
			datasets = append([]domain.AccountDataset(nil), input.Datasets...)
		}
	} else {
		datasets, err = normalizeDatasets(input.Datasets)
		if err != nil {
			return nil, err
		}
	}
	request := map[string]any{"datasets": datasets}
	if continuation != nil {
		request["audit_export_continuation_job_id"] = continuation.ID.String()
	}
	job := &domain.SyncJob{AccountID: input.AccountID, ActorUID: actor, TenantID: tenantID,
		RequestID: RequestIDFrom(ctx), JobType: input.JobType, Status: domain.SyncJobPending,
		IdempotencyKey: input.IdempotencyKey, Request: request, Result: map[string]any{}}
	job.ID = idgen.NewID()
	if err := s.createWithAudit(ctx, job); err != nil {
		existing, findErr := s.repo.FindByIdempotency(ctx, actor, tenantID, input.IdempotencyKey)
		if findErr == nil {
			return existing, nil
		}
		if !errors.Is(findErr, repository.ErrNotFound) && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil, apperr.Internal("sync.idempotency_lookup_failed", "failed to check sync idempotency").Wrap(findErr)
		}
		return nil, apperr.Internal("sync.create_failed", "failed to create sync job").Wrap(err)
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

//nolint:cyclop,funlen // the worker loop coordinates normal execution and unknown erase reconciliation.
func (s *syncJobService) Start(ctx context.Context, interval time.Duration) func() {
	if ctx == nil {
		ctx = context.Background()
	}
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

//nolint:cyclop,funlen // erase execution preserves unknown remote outcomes before local deletion.
func (s *syncJobService) executeErase(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	datasets, scopeErr := parsePersistedDatasets(job.Request, true)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
			"erase job requires a non-empty valid dataset scope", nil, scopeErr)
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		cause := apperr.Internal("erase.account_load_failed", "failed to load account").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.account_load_failed", "failed to load account", nil, cause)
	}
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
				status, outcomeUnknown, outcomeFailed := eraseCallOutcome(callErr)
				if outcomeUnknown {
					unknown = true
				}
				if outcomeFailed {
					failures++
				}
				statuses[string(source)] = map[string]any{"status": status, "error_code": remoteErrorCode(callErr)}
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
	return s.finishJobAndLoad(ctx, job, status, code, message, result)
}

func (s *syncJobService) finishJob(ctx context.Context, job *domain.SyncJob, status domain.SyncJobStatus, code, message string, result map[string]any) error {
	if s.audit != nil {
		// Single minting point (contract §2.3 / D-7): the fact is appended
		// in-tx via FinishWithAudit; a repo without the WithAudit surface fails
		// closed (B2-1 REQ-4) — no standalone fallback tail.
		fact := syncJobFinishedAudit(ctx, s.region, job, status, code, result)
		repo, ok := s.repo.(repository.SyncJobAuditRepository)
		if !ok {
			return apperr.Internal("sync.audit_unavailable", "sync audit persistence is unavailable")
		}
		return repo.FinishWithAudit(ctx, job.ID, job.LeaseOwner, status, code, message, result, fact)
	}
	return s.repo.Finish(ctx, job.ID, job.LeaseOwner, status, code, message, result)
}

func remoteErrorCode(err error) string {
	if remote, ok := connector.IsRemote(err); ok && remote.Code != "" {
		return remote.Code
	}
	return "source.command_failed"
}
