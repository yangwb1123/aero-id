//go:build integration

package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/sso"
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
