//go:build integration

package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

// reconcilerNoOp keeps the operation reconciler loop inert (no remote calls).
type reconcilerNoOp struct{}

func (reconcilerNoOp) Reconcile(context.Context, *domain.Operation, *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	return domain.StepSucceeded, "", nil, nil
}

const standaloneSiteKey = "standalone-site-audit-key-0123456789abcdef"

// standaloneHarness wires a real ledger + recording metrics spy.
type standaloneHarness struct {
	db      *gorm.DB
	ledger  AuditLedgerService
	metrics *metricSpy
	repo    repository.FullAuditLedgerRepository
}

func startStandaloneHarness(t *testing.T) *standaloneHarness {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: standaloneSiteKey})
	ledger := NewAuditLedgerService(repo)
	metrics := &metricSpy{}
	if setter, ok := ledger.(ControlPlaneMetricsSetter); ok {
		setter.SetControlPlaneMetrics(metrics)
	}
	return &standaloneHarness{db: db, ledger: ledger, metrics: metrics, repo: repo}
}

func standaloneFactCount(t *testing.T, h *standaloneHarness, eventID domain.ID) int64 {
	t.Helper()
	var n int64
	if eventID == (domain.ID{}) {
		if err := h.db.Raw(`SELECT count(*) FROM audit_ledger`).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func standaloneCount(t *testing.T, h *standaloneHarness, action string) int {
	t.Helper()
	n := 0
	for _, a := range h.metrics.standaloneActions {
		if a == action {
			n++
		}
	}
	return n
}

// TestStandaloneSiteEventIngestQuarantine (REQ-4): the event-ingest quarantine
// fact is anchored on the envelope event_id — two executions (retry) record
// the counter twice but dedup to exactly one ledger row.
func TestStandaloneSiteEventIngestQuarantine(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	svc := &eventIngestService{receipts: nil, audit: h.ledger, metrics: h.metrics}
	envelope := &event.Envelope{EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.object.created", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: 1, ActorUID: "u-1", TenantID: "tenant-a",
		OperationID: idgen.NewID(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"k": "v"}}
	for i := 0; i < 2; i++ {
		if err := svc.recordAudit(ctx, envelope, nil, "quarantined"); err != nil {
			t.Fatalf("recordAudit #%d: %v", i, err)
		}
	}
	if n := standaloneCount(t, h, "source.event"); n != 2 {
		t.Fatalf("standalone counter for source.event = %d, want 2 (one per execution)", n)
	}
	if n := standaloneFactCount(t, h, envelope.EventID); n != 1 {
		t.Fatalf("ledger rows for anchored event = %d, want exactly 1 (retry dedup)", n)
	}
}

// TestStandaloneSiteOperationReconciled (REQ-4): operation.reconciled is the
// documented A1 standalone fact with an anchored event_id — retries dedup.
func TestStandaloneSiteOperationReconciled(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	opRepo := repository.NewOperationRepository(h.db, h.repo)
	svc := &operationService{repo: opRepo, audit: h.ledger, region: "local", metrics: h.metrics}
	identityCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})
	accountSvc := &accountService{repo: repository.NewAccountRepository(h.db, h.repo), audit: h.ledger,
		issuer: "snaplink", region: "local", permission: allowAllPermission{}}
	account, err := accountSvc.ensure(identityCtx)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	operation := &domain.Operation{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: account.ID,
		ActorUID: "u-1", Status: domain.OperationRunning, OperationType: "test.op",
		Steps: []*domain.OperationStep{{ID: idgen.NewID(), StepKey: "s", Status: domain.StepRunning}}}
	if err := opRepo.Create(ctx, operation); err != nil {
		t.Fatalf("seed operation: %v", err)
	}

	reconciler := reconcilerNoOp{}
	for i := 0; i < 2; i++ {
		if _, err := svc.reconcileClaimed(ctx, operation, "worker-1", reconciler); err != nil {
			t.Fatalf("reconcileClaimed #%d: %v", i, err)
		}
	}
	if n := standaloneCount(t, h, "operation.reconciled"); n != 2 {
		t.Fatalf("standalone counter = %d, want 2", n)
	}
	// The anchored fact derives from (action, tenant, op, step, status); both
	// executions derived the same event_id, so exactly ONE operation.reconciled
	// row exists (other facts from the harness — account provision, terminal
	// operation writes — are not counted).
	var n int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'operation.reconciled'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("operation.reconciled rows after retry = %d, want exactly 1 (anchored dedup)", n)
	}
}

// TestStandaloneSiteMessageDeleteAlreadyGone (REQ-4): the deleteAlreadyGone
// residual re-records the anchored message.delete fact — a retry dedups to the
// first row while the counter counts each execution.
func TestStandaloneSiteMessageDeleteAlreadyGone(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	msgRepo := repository.NewMessageRepository(h.db, nil, h.repo)
	svc := NewMessageService(msgRepo, nil, &mocksUserDir{}, WithMessageAuditLogger(h.ledger)).(*messageService)

	// First delete commits via the in-tx path (SoftDeleteWithEventAudit).
	id := idgen.NewID()
	if err := msgRepo.Create(ctx, &domain.Message{ID: id, FromUID: "u-1", ToUID: "u-2", Content: "hi", Kind: domain.MessageKindText}, nil); err != nil {
		t.Fatal(err)
	}
	ctxActor := WithIdentity(ctx, &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})
	if err := svc.Delete(ctxActor, id); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	// Second delete reads NotFound and re-records the fact via
	// deleteAlreadyGone (anchored on the message ID).
	if err := svc.Delete(ctxActor, id); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if n := standaloneFactCount(t, h, id); n != 1 {
		t.Fatalf("ledger rows for message id = %d, want exactly 1 (anchored dedup across the in-tx + residual paths)", n)
	}
}

// TestStandaloneSiteAccountViewedAndListed (REQ-4): sensitive-read access
// facts are fresh-ID — each execution appends exactly one row, never two.
func TestStandaloneSiteAccountViewedAndListed(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	accountRepo := repository.NewAccountRepository(h.db, h.repo)
	svc := &accountService{repo: accountRepo, audit: h.ledger, issuer: "snaplink", region: "local",
		home: nil, permission: allowAllPermission{}}
	tenantCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink", Subject: "admin-1", TenantID: "tenant-a"})

	account, err := svc.ensure(tenantCtx)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.GetAccount(tenantCtx, account.ID); err != nil {
			t.Fatalf("GetAccount #%d: %v", i, err)
		}
		if _, err := svc.ListAccounts(tenantCtx, ListAccountsInput{}); err != nil {
			t.Fatalf("ListAccounts #%d: %v", i, err)
		}
	}
	if n := standaloneCount(t, h, "account.viewed"); n != 2 {
		t.Fatalf("account.viewed counter = %d, want 2", n)
	}
	if n := standaloneCount(t, h, "account.listed"); n != 2 {
		t.Fatalf("account.listed counter = %d, want 2", n)
	}
	// Fresh-ID sites: exactly one row per execution (2+2 executions, no dedup
	// by design). The seed ensure() wrote its own provision fact; count only
	// the sensitive-read actions.
	var n int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action IN ('account.viewed', 'account.listed')`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("sensitive-read ledger rows = %d, want 4 (one per execution, fresh IDs)", n)
	}
}

// mocksUserDir is a minimal UserDirectory for message construction.
type mocksUserDir struct{}

func (mocksUserDir) GetUsers(context.Context, []string) (map[string]sso.User, error) { return nil, nil }

// failingLedgerRepo delegates every ledger operation to the wrapped repo but
// fails AppendInTx, so an in-tx audit append failure can be exercised against
// the real transaction machinery (region promotion rollback).
type failingLedgerRepo struct {
	repository.FullAuditLedgerRepository
}

func (failingLedgerRepo) AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error {
	return errors.New("append region promotion audit: ledger down")
}

// TestStandaloneSiteFileCreateFacts: both file materialization paths emit
// exactly one anchored file.create fact. A presigned retry with the same
// FileID conflicts on the row insert before the audit site, so the anchored
// count stays exactly one (never a duplicate row).
func TestStandaloneSiteFileCreateFacts(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	store := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	fileRepo := repository.NewFileRepository(h.db, h.repo)
	svc := NewFileService(fileRepo, store, "local", WithFileAuditLogger(h.ledger))
	actorCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "owner", Issuer: "snaplink", Subject: "owner", TenantID: "tenant-a"})

	f, err := svc.Create(actorCtx, CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
	if err != nil {
		t.Fatalf("direct create: %v", err)
	}
	if n := standaloneFactCount(t, h, f.ID); n != 1 {
		t.Fatalf("direct-create rows for file id = %d, want exactly 1 (anchored)", n)
	}

	key, _, err := store.Put(ctx, "owner", "b.txt", strings.NewReader("direct upload bytes"))
	if err != nil {
		t.Fatal(err)
	}
	fileID := idgen.NewID()
	if _, err := svc.CompleteUpload(actorCtx, CompleteUploadInput{FileID: fileID, Name: "b.txt", StorageKey: key}); err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	if n := standaloneFactCount(t, h, fileID); n != 1 {
		t.Fatalf("presigned rows for file id = %d, want exactly 1 (anchored on the presigned FileID)", n)
	}

	if _, err := svc.CompleteUpload(actorCtx, CompleteUploadInput{FileID: fileID, Name: "b.txt", StorageKey: key}); err == nil {
		t.Fatal("re-materializing the same FileID must fail (row already exists)")
	}
	if n := standaloneFactCount(t, h, fileID); n != 1 {
		t.Fatalf("presigned rows after retry = %d, want exactly 1", n)
	}
	var fileFacts int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'file.create'`).Scan(&fileFacts).Error; err != nil {
		t.Fatal(err)
	}
	if fileFacts != 2 {
		t.Fatalf("file.create rows = %d, want exactly 2 (one per materialized file)", fileFacts)
	}
}

// TestStandaloneSiteMessageSendFacts: message.send is anchored on the message
// ID — two sends produce two rows with distinct anchors.
func TestStandaloneSiteMessageSendFacts(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	msgRepo := repository.NewMessageRepository(h.db, nil, h.repo)
	svc := NewMessageService(msgRepo, nil, &mocksUserDir{}, WithMessageAuditLogger(h.ledger))
	actorCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})

	m1, err := svc.Send(actorCtx, SendMessageInput{ToUID: "u-2", Content: "first"})
	if err != nil {
		t.Fatalf("send #1: %v", err)
	}
	m2, err := svc.Send(actorCtx, SendMessageInput{ToUID: "u-2", Content: "second"})
	if err != nil {
		t.Fatalf("send #2: %v", err)
	}
	if n := standaloneFactCount(t, h, m1.ID); n != 1 {
		t.Fatalf("rows for message %s = %d, want exactly 1 (anchored)", m1.ID, n)
	}
	if n := standaloneFactCount(t, h, m2.ID); n != 1 {
		t.Fatalf("rows for message %s = %d, want exactly 1 (anchored)", m2.ID, n)
	}
	var sendFacts int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'message.send'`).Scan(&sendFacts).Error; err != nil {
		t.Fatal(err)
	}
	if sendFacts != 2 {
		t.Fatalf("message.send rows = %d, want 2 (distinct anchors)", sendFacts)
	}
}

// TestStandaloneSitePreferenceDedup: the digest anchor makes a same-payload
// retry dedup to one row while a distinct payload appends a second row (no
// silent drop of a later legitimate Set).
func TestStandaloneSitePreferenceDedup(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	prefRepo := repository.NewPreferenceRepository(h.db)
	svc := NewPreferenceService(prefRepo, WithPreferenceAuditLogger(h.ledger))
	actorCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})

	same := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: true, Timezone: "UTC"}}
	for i := 0; i < 2; i++ {
		if err := svc.Set(actorCtx, same); err != nil {
			t.Fatalf("set #%d: %v", i, err)
		}
	}
	var n int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'preference.set'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("same-payload preference.set rows = %d, want exactly 1 (anchored retry dedup)", n)
	}
	different := []*domain.NotificationPreference{{NotificationType: "message.sent", Enabled: false, Timezone: "UTC"}}
	if err := svc.Set(actorCtx, different); err != nil {
		t.Fatalf("distinct set: %v", err)
	}
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'preference.set'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("distinct-payload preference.set rows = %d, want 2 (no silent drop)", n)
	}
}

// TestStandaloneSiteRegionPromoteInTx: the promotion CAS and its fact commit
// in ONE transaction; a forced append failure rolls the promotion back
// (directory and epoch untouched, zero new rows).
func TestStandaloneSiteRegionPromoteInTx(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	accounts := repository.NewAccountRepository(h.db, h.repo)
	account, err := accounts.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-promote", "u-promote", "local")
	if err != nil {
		t.Fatal(err)
	}
	directory := repository.NewRegionDirectoryRepository(h.db, h.repo)
	if err := directory.EnsureDirectory(ctx, "tenant-a", account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	svc := NewRegionFencingService(directory, WithRegionFencingAuditLogger(h.ledger))
	actorCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink", Subject: "admin-1", TenantID: "tenant-a"})

	newEpoch, err := svc.Promote(actorCtx, "tenant-a", account.ID, "us-east", 0)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if newEpoch != 1 {
		t.Fatalf("epoch = %d, want 1", newEpoch)
	}
	var n int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'region.promote'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("region.promote rows = %d, want exactly 1 (in-tx with the CAS)", n)
	}

	// Forced append failure: the CAS rolls back — directory untouched, no row.
	failing := &failingLedgerRepo{FullAuditLedgerRepository: h.repo}
	svc2 := NewRegionFencingService(repository.NewRegionDirectoryRepository(h.db, failing),
		WithRegionFencingAuditLogger(h.ledger))
	_, err = svc2.Promote(actorCtx, "tenant-a", account.ID, "eu-west", 1)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "region.promotion_failed" {
		t.Fatalf("promote with failing append = %v, want region.promotion_failed", err)
	}
	entry, gerr := directory.GetDirectory(ctx, "tenant-a", account.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if entry.HomeRegion != "us-east" || entry.FencedEpoch != 1 {
		t.Fatalf("rolled-back promotion must leave the directory untouched: %+v", entry)
	}
	account, aerr := accounts.GetByID(ctx, "tenant-a", account.ID)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if account.HomeRegion != "us-east" {
		t.Fatalf("rolled-back promotion changed account routing region: %s", account.HomeRegion)
	}
	if epoch, eerr := directory.RegionEpoch(ctx, "eu-west"); eerr != nil || epoch != 0 {
		t.Fatalf("eu-west epoch = %d, %v; want 0 (rollback)", epoch, eerr)
	}
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'region.promote'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("region.promote rows after failed attempt = %d, want exactly 1 (rollback appended nothing)", n)
	}
}

// TestStandaloneSiteReplicationDedup: two runs publishing the same snapshot
// version derive the same deterministic envelope EventID, so the ledger
// dedups to exactly one projection.replication.publish row.
func TestStandaloneSiteReplicationDedup(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	accounts := repository.NewAccountRepository(h.db, h.repo)
	account, err := accounts.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-repl", "u-repl", "local")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &domain.AccountSnapshot{ID: idgen.NewID(), AccountID: account.ID, Source: "aero-vault",
		SourceRegion: "us-west", Dataset: "aero-vault.buckets", Status: domain.SnapshotFresh, Version: 1,
		GeneratedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Data: map[string]any{"usage_bytes": float64(10)}}
	snapshots := repository.NewAccountSnapshotRepository(h.db)
	if err := snapshots.UpsertSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	// UpsertSnapshot creates the first durable change-log revision, which is
	// part of the event identity used by the sequence-aware worker.
	envelope := replicationEnvelopeAtRevision("local", "tenant-a", snapshot, 1)
	for i := 0; i < 2; i++ {
		// A fresh cursor each run so both runs publish the same snapshot.
		svc := NewProjectionReplicationService("local", snapshots, repository.NewReplicationCursorRepository(h.db),
			&fakeReplicationPublisher{}, WithProjectionReplicationAudit(h.ledger))
		if _, err := svc.ReplicateOnce(ctx); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if n := standaloneFactCount(t, h, envelope.EventID); n != 1 {
		t.Fatalf("ledger rows for deterministic envelope id = %d, want exactly 1 (dedup)", n)
	}
	var pub int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'projection.replication.publish'`).Scan(&pub).Error; err != nil {
		t.Fatal(err)
	}
	if pub != 1 {
		t.Fatalf("projection.replication.publish rows = %d, want exactly 1 (deterministic EventID dedup)", pub)
	}
}

// TestStandaloneSiteReplayRun: projection.replay.run is fresh per-execution —
// two runs produce two rows (no retry-dedup contract).
func TestStandaloneSiteReplayRun(t *testing.T) {
	ctx := context.Background()
	h := startStandaloneHarness(t)
	accounts := repository.NewAccountRepository(h.db, h.repo)
	account, err := accounts.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-replay", "u-replay", "local")
	if err != nil {
		t.Fatalf("seed replay account: %v", err)
	}
	svc := NewProjectionReplayService(repository.NewEventStoreRepository(h.db),
		repository.NewAccountSnapshotRepository(h.db), accounts, allowAllPermission{},
		WithProjectionReplayAudit(h.ledger))
	actorCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink", Subject: "admin-1", TenantID: "tenant-a"})
	for i := 0; i < 2; i++ {
		if _, err := svc.ReplayProjection(actorCtx, ReplayProjectionInput{AccountID: account.ID, Source: "aero-vault"}); err != nil {
			t.Fatalf("replay run %d: %v", i, err)
		}
	}
	var n int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'projection.replay.run'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("projection.replay.run rows = %d, want 2 (fresh per-execution)", n)
	}
}
