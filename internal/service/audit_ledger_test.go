package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type auditLedgerRepoSpy struct {
	appended  *domain.AuditFact
	filter    domain.AuditFactFilter
	source    string
	partition string
	appendErr error
}

func (r *auditLedgerRepoSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	r.appended = fact
	return r.appendErr
}

func (r *auditLedgerRepoSpy) List(_ context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	r.filter = filter
	return nil, "", nil
}

func (r *auditLedgerRepoSpy) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return nil, nil
}

func (r *auditLedgerRepoSpy) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	return nil, repository.ErrNotFound
}

func (r *auditLedgerRepoSpy) VerifyChain(_ context.Context, source, partition string) error {
	r.source, r.partition = source, partition
	return nil
}

// TestAuditLedgerServiceMethodSetFrozen (R-4): the ledger service must never
// gain a publisher surface — Record appends and the drain worker delivers, so
// a future Publish/EnqueueDelivery method (or any other widening of the
// interface) would silently add a second delivery path. Pin the interface's
// method set via reflection: any addition or removal breaks this test loudly.
// Behavior tests (TestAuditLedgerRecordEnqueuesOnly) prove what Record does;
// this test proves what the type may become.
func TestAuditLedgerServiceMethodSetFrozen(t *testing.T) {
	typ := reflect.TypeOf((*AuditLedgerService)(nil)).Elem()
	got := make([]string, 0, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	want := []string{"List", "Log", "Record", "VerifyChain"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AuditLedgerService method set = %v, want %v — "+
			"the ledger service must never grow a publisher surface (Record appends; "+
			"the drain worker is the sole delivery path)", got, want)
	}
}

// TestAuditLedgerRecordEnqueuesOnly (REQ-1 / T-10.2): Record appends exactly
// one ledger row and never publishes — the inline publisher loop is gone at
// compile time and the drain worker is the single delivery path.
func TestAuditLedgerRecordEnqueuesOnly(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if err := ledger.Record(ctx, AuditFactInput{Action: "account.viewed", AccountID: idgen.NewID()}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if repo.appended == nil {
		t.Fatal("Record() must append exactly one ledger row")
	}
	if repo.appended.Action != "account.viewed" {
		t.Fatalf("appended action = %q", repo.appended.Action)
	}
}

// TestAuditLedgerDuplicateMapsToSuccess (FM-7): a duplicate event_id from the
// repo (sequential retry of an idempotent command) surfaces as success at the
// service layer; every other error propagates.
func TestAuditLedgerDuplicateMapsToSuccess(t *testing.T) {	dupe := &auditLedgerRepoSpy{}
	dupe.appendErr = repository.ErrAuditDuplicate
	ledger := NewAuditLedgerService(dupe)
	if err := ledger.Record(t.Context(), AuditFactInput{Action: "account.provisioned"}); err != nil {
		t.Fatalf("ErrAuditDuplicate must map to success, got %v", err)
	}

	boom := &auditLedgerRepoSpy{}
	boom.appendErr = context.DeadlineExceeded
	ledger = NewAuditLedgerService(boom)
	if err := ledger.Record(t.Context(), AuditFactInput{Action: "account.viewed"}); err == nil {
		t.Fatal("non-duplicate errors must propagate")
	}
}

// dedupAuditRepoSpy simulates the real ledger dedup: appending a fact whose
// (tenant, source, event_id) triple was already appended returns
// ErrAuditDuplicate and records nothing (the uq_audit_ledger_tenant_event
// behavior).
type dedupAuditRepoSpy struct {
	auditLedgerRepoSpy
	triples map[string]bool
}

func (r *dedupAuditRepoSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	r.appended = fact
	key := fact.TenantID + "|" + fact.Source + "|" + fact.EventID.String()
	if r.triples[key] {
		return repository.ErrAuditDuplicate
	}
	r.triples[key] = true
	return nil
}

func (r *dedupAuditRepoSpy) appendedCount() int { return len(r.triples) }

// TestRecordAnchoredEventID_Dedup (T-5.1): a first Record with an anchored
// EventID appends once; an identical second Record returns nil and appends
// nothing — the service-level duplicate contract that deterministic event_ids
// make load-bearing (crash-retry, duplicate delivery, out-of-order arrival).
func TestRecordAnchoredEventID_Dedup(t *testing.T) {
	repo := &dedupAuditRepoSpy{triples: map[string]bool{}}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
	eventID := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acc-1", "evidence", "")

	if err := ledger.Record(ctx, AuditFactInput{Action: "audit_hold.set", TenantID: "tenant-a", EventID: eventID}); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if repo.appendedCount() != 1 {
		t.Fatalf("appends after first Record = %d, want 1", repo.appendedCount())
	}
	if err := ledger.Record(ctx, AuditFactInput{Action: "audit_hold.set", TenantID: "tenant-a", EventID: eventID}); err != nil {
		t.Fatalf("identical second Record must map to nil, got %v", err)
	}
	if repo.appendedCount() != 1 {
		t.Fatalf("appends after duplicate Record = %d, want still 1", repo.appendedCount())
	}
}

// TestLogAdapterForwardsEventID (T-10.1-C / REQ-2): the legacy Log adapter is
// the single delivery path from domain.AuditEvent to ledger fact, so it must
// map AuditEvent.ID into AuditFactInput.EventID — otherwise anchored resolve
// events never reach the dedup key and retries append duplicate rows. The
// dedup-count assertions alone false-pass without the mapping (the spy keys on
// the zero-EventID triple), so the received EventID capture is the explicit
// pin; the second identical Log must also map to nil with no new append.
func TestLogAdapterForwardsEventID(t *testing.T) {
	repo := &dedupAuditRepoSpy{triples: map[string]bool{}}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
	reportID := idgen.NewID()
	event := &domain.AuditEvent{
		ID:          reportID,
		ActorUID:    "user-1",
		Action:      domain.AuditReportResolve,
		SubjectType: "comment_report",
		SubjectID:   reportID.String(),
		TargetUID:   "author",
		Meta:        map[string]any{"comment_id": "c-1", "status": "resolved"},
		CreatedAt:   time.Now().UTC(),
	}

	if err := ledger.Log(ctx, event); err != nil {
		t.Fatalf("first Log: %v", err)
	}
	if repo.appended == nil {
		t.Fatal("Log must append exactly one fact")
	}
	if repo.appended.EventID != reportID {
		t.Fatalf("adapter EventID = %s, want %s (AuditEvent.ID must reach the dedup key)", repo.appended.EventID, reportID)
	}
	if err := ledger.Log(ctx, event); err != nil {
		t.Fatalf("identical second Log must map to nil, got %v", err)
	}
	if repo.appendedCount() != 1 {
		t.Fatalf("appends after duplicate Log = %d, want still 1", repo.appendedCount())
	}
}

// TestRecordObservesAuditIngestFailure (REQ-7): every failed Record append
// fires the audit ingest-failure counter exactly once with the trimmed fact
// source — the read-path sites (account.go) surface audit.ingest_failed and
// rely on this counter for the fail-closed observability contract. A
// duplicate fold is NOT an ingest failure: it maps to success and must not
// increment the counter.
func TestRecordObservesAuditIngestFailure(t *testing.T) {
	metrics := &metricSpy{}
	failing := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	ledger := NewAuditLedgerService(failing)
	ledger.(*auditLedgerService).SetControlPlaneMetrics(metrics)

	if err := ledger.Record(t.Context(), AuditFactInput{Source: " aero-id ", Action: "account.viewed"}); err == nil {
		t.Fatal("Record must propagate the append error")
	}
	if len(metrics.ingestFailures) != 1 || metrics.ingestFailures[0] != "aero-id" {
		t.Fatalf("ingest failures = %v, want [aero-id] (trimmed source)", metrics.ingestFailures)
	}

	dupe := &auditLedgerRepoSpy{appendErr: repository.ErrAuditDuplicate}
	ledger = NewAuditLedgerService(dupe)
	ledger.(*auditLedgerService).SetControlPlaneMetrics(metrics)
	if err := ledger.Record(t.Context(), AuditFactInput{Source: "aero-id", Action: "account.viewed"}); err != nil {
		t.Fatalf("duplicate fold must succeed, got %v", err)
	}
	if len(metrics.ingestFailures) != 1 {
		t.Fatalf("ingest failures after duplicate fold = %d, want still 1 (duplicates are not ingest failures)", len(metrics.ingestFailures))
	}
}

func TestAuditLedgerBindsTenantFromVerifiedIdentity(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if err := ledger.Record(ctx, AuditFactInput{TenantID: "tenant-b", Action: "account.viewed"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if repo.appended.TenantID != "tenant-a" || repo.appended.PartitionKey != "tenant-a:user-1" {
		t.Fatalf("appended tenant/partition = %q/%q", repo.appended.TenantID, repo.appended.PartitionKey)
	}
}

func TestAuditLedgerOverridesRequestedTenant(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if _, _, err := ledger.List(ctx, domain.AuditFactFilter{TenantID: "tenant-b"}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repo.filter.TenantID != "tenant-a" {
		t.Fatalf("filter tenant = %q, want tenant-a", repo.filter.TenantID)
	}
	if err := ledger.VerifyChain(ctx, "aero-id", "user-1"); err != nil {
		t.Fatalf("VerifyChain() error = %v", err)
	}
	if repo.source != "aero-id" || repo.partition != "tenant-a:user-1" {
		t.Fatalf("verified source/partition = %q/%q", repo.source, repo.partition)
	}
}
