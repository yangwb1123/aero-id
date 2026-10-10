package service

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type gatedArtifactRepo struct {
	*artifactRecoveryFixture
	gate            *syncGateRepo
	afterCheckpoint func()
}

func (r *gatedArtifactRepo) CheckExecutionLease(ctx context.Context, job *domain.SyncJob, region string, allowErased bool) error {
	return r.gate.CheckExecutionLease(ctx, job, region, allowErased)
}
func (r *gatedArtifactRepo) DeferExecutionWithAudit(ctx context.Context, input repository.SyncJobExecutionDeferral, fact *domain.AuditFact) error {
	return r.gate.DeferExecutionWithAudit(ctx, input, fact)
}
func (r *gatedArtifactRepo) CheckpointArtifact(ctx context.Context, input repository.ArtifactCheckpoint) error {
	err := r.artifactRecoveryFixture.CheckpointArtifact(ctx, input)
	if r.afterCheckpoint != nil {
		r.afterCheckpoint()
	}
	return err
}

type revokingArtifactStore struct {
	*jobArtifactFixture
	afterPut, afterRead func()
	reads               int
}

func (s *revokingArtifactStore) PutJobArtifact(ctx context.Context, owner string, id domain.ID, kind string, reader io.Reader) (string, int64, error) {
	key, size, err := s.jobArtifactFixture.PutJobArtifact(ctx, owner, id, kind, reader)
	if s.afterPut != nil {
		s.afterPut()
	}
	return key, size, err
}
func (s *revokingArtifactStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads++
	reader, err := s.memoryStore.Get(ctx, key)
	if s.afterRead != nil {
		s.afterRead()
	}
	return reader, err
}

func TestSyncExportRevocationKeepsOriginalCheckpointAndNeverRegenerates(t *testing.T) {
	for _, phase := range []string{"before put", "after put", "during recovery read"} {
		t.Run(phase, func(t *testing.T) {
			svc, job, base, writer, account := recoverableExportFixture(t)
			claimed := armSyncGateJob(job, "execute")
			gate := &syncGateRepo{minimalSyncJobRepo: base.minimalSyncJobRepo, job: job}
			repo := &gatedArtifactRepo{artifactRecoveryFixture: base, gate: gate}
			store := &revokingArtifactStore{jobArtifactFixture: writer}
			svc.repo = repo
			svc.store = store
			allowed := &atomic.Bool{}
			allowed.Store(true)
			svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return allowed.Load(), nil })
			switch phase {
			case "before put":
				repo.afterCheckpoint = func() { allowed.Store(false) }
			case "after put":
				store.afterPut = func() { allowed.Store(false) }
			case "during recovery read":
				writer.writeErr = io.ErrUnexpectedEOF
			}
			_, err := svc.executeExport(t.Context(), claimed)
			if phase == "during recovery read" {
				if err != nil || job.Status != domain.SyncJobUnknown {
					t.Fatal("fixture did not retain original Put", err)
				}
				store.afterRead = func() { allowed.Store(false) }
				claimed = armSyncGateJob(job, "reconcile")
				_, err = svc.executeExport(t.Context(), claimed)
			}
			if requireAppError(t, err).Code != "sync.authorization_denied" || job.Status != domain.SyncJobUnknown || len(gate.facts) != 1 {
				t.Fatal("revoked artifact was published or receipt lost", err)
			}
			key, expiry, digest := job.Result["_storage_key"], job.Result["expires_at"], job.Result["manifest_digest"]
			if key != base.metadata.StorageKey || expiry == nil || digest == nil {
				t.Fatal("original checkpoint metadata lost")
			}
			if _, hasContent := job.Result["profile"]; hasContent {
				t.Fatal("deferral persisted content")
			}
			puts, reads := writer.puts, store.reads
			claimed = armSyncGateJob(job, "reconcile")
			_, err = svc.executeExport(t.Context(), claimed)
			if requireAppError(t, err).Code != "sync.authorization_denied" || writer.puts != puts || store.reads != reads {
				t.Fatal("revoked recovery performed IO", err)
			}
			allowed.Store(true)
			repo.afterCheckpoint = nil
			store.afterPut = nil
			store.afterRead = nil
			writer.writeErr = nil
			claimed = armSyncGateJob(job, "reconcile")
			result, err := svc.executeExport(t.Context(), claimed)
			if err != nil || writer.puts != puts || account.sourcePages != 1 || result.Result["_storage_key"] != key || result.Result["expires_at"] != expiry || result.Result["manifest_digest"] != digest {
				t.Fatal("restored grant regenerated artifact, changed key/TTL or lost receipt", err)
			}
			want := domain.SyncJobCompleted
			if phase == "before put" {
				want = domain.SyncJobUnknown
			}
			if result.Status != want {
				t.Fatalf("state %s want %s", result.Status, want)
			}
		})
	}
}

type revokingAuditLedger struct {
	*pagedAuditLedgerRepo
	calls     int
	afterPage func()
}

func (l *revokingAuditLedger) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	l.calls++
	rows, next, err := l.pagedAuditLedgerRepo.List(ctx, filter)
	if l.afterPage != nil {
		l.afterPage()
	}
	return rows, next, err
}
func TestSyncAuditExportRechecksReadPermissionAtEveryPage(t *testing.T) {
	svc, job, repo, _, _, _ := syncExecutionFixture(t, domain.SyncJobAuditExport)
	facts := make([]*domain.AuditFact, domain.MaxPageLimit+1)
	for i := range facts {
		facts[i] = auditExportFact(job.TenantID)
	}
	canRead := &atomic.Bool{}
	canRead.Store(true)
	ledger := &revokingAuditLedger{pagedAuditLedgerRepo: &pagedAuditLedgerRepo{facts: facts}, afterPage: func() { canRead.Store(false) }}
	svc.auditLedger = ledger
	svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, permission string) (bool, error) {
		if actor != job.ActorUID {
			t.Fatal("audit page used operator grant")
		}
		return permission != domain.PermAuditRead || canRead.Load(), nil
	})
	_, err := svc.executeAuditExport(t.Context(), job)
	if requireAppError(t, err).Code != "sync.authorization_denied" || ledger.calls != 1 || repo.job.Status != domain.SyncJobUnknown || len(svc.store.(*memoryStore).objects) != 0 {
		t.Fatal("continued reading pages or exported without audit-read grant", err)
	}
	if _, ok := repo.job.Result["events"]; ok {
		t.Fatal("blocked audit export persisted events")
	}
}

func TestSyncExportDeferralDoesNotExtendExpiry(t *testing.T) {
	_, job, repo, _, _, _ := syncExecutionFixture(t, domain.SyncJobExport)
	job.LeaseKind = "reconcile"
	repo.job.LeaseKind = "reconcile"
	expiry := time.Now().UTC().Add(time.Hour)
	repo.job.Result = map[string]any{"_storage_key": "original", "expires_at": expiry, "manifest_digest": "digest"}
	job.Result = repo.job.Result
	// Exact metadata is asserted by the recovery tests; this catches mutable
	// result aliasing across the standalone audited deferral path as well.
	svc := newAuditExportService(nil, &memoryStore{objects: map[string][]byte{}})
	svc.repo = repo
	_, err := svc.pauseSyncExecution(t.Context(), job, invalidExportScope(), false, nil)
	if err == nil || repo.job.Result["expires_at"] != expiry || repo.job.Result["_storage_key"] != "original" {
		t.Fatal("deferral replaced retention metadata")
	}
}
