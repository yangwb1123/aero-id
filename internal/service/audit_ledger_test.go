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
	listErr   error
	verifyErr error
}

func (r *auditLedgerRepoSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	r.appended = fact
	return r.appendErr
}

func (r *auditLedgerRepoSpy) List(_ context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	r.filter = filter
	return nil, "", r.listErr
}

func (r *auditLedgerRepoSpy) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return nil, nil
}

func (r *auditLedgerRepoSpy) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	return nil, repository.ErrNotFound
}

func (r *auditLedgerRepoSpy) VerifyChain(_ context.Context, source, partition string) error {
	r.source, r.partition = source, partition
	return r.verifyErr
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
func TestAuditLedgerDuplicateMapsToSuccess(t *testing.T) {
	dupe := &auditLedgerRepoSpy{}
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

// TestLogAdapterCarriesTargetUIDIntoPayload (REQ-1 / REQ-6): the standalone
// adapter is the delivery path for message send/edit/react/unreact, and the
// ledger row is the sealed artifact that leaves this process — so the
// counterparty must survive into the payload exactly as the in-tx converter
// (buildLegacyAuditFact) stores it. The rest of the mapping and the anchored
// EventID forwarding must be untouched by that addition.
func TestLogAdapterCarriesTargetUIDIntoPayload(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
	messageID := idgen.NewID()
	event := &domain.AuditEvent{
		ID:          messageID,
		ActorUID:    "user-1",
		Action:      domain.AuditMessageSend,
		SubjectType: "message",
		SubjectID:   messageID.String(),
		TargetUID:   "user-2",
		Meta:        map[string]any{"comment_id": "c-1"},
		CreatedAt:   time.Now().UTC(),
	}

	if err := ledger.Log(ctx, event); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if repo.appended == nil {
		t.Fatal("Log must append exactly one fact")
	}
	if got := repo.appended.Payload["target_uid"]; got != "user-2" {
		t.Fatalf("Payload[target_uid] = %v, want user-2 (the counterparty is the fact's sealed peer record)", got)
	}
	if got := repo.appended.Payload["comment_id"]; got != "c-1" {
		t.Fatalf("Payload[comment_id] = %v, want c-1 (carrying TargetUID must not displace the caller's meta)", got)
	}
	if repo.appended.ActorUID != "user-1" || repo.appended.Action != domain.AuditMessageSend ||
		repo.appended.ResourceType != "message" || repo.appended.ResourceID != messageID.String() {
		t.Fatalf("appended mapping drifted: %#v", repo.appended)
	}
	if repo.appended.EventID != messageID {
		t.Fatalf("EventID = %s, want %s (anchoring is unchanged by this change)", repo.appended.EventID, messageID)
	}
}

// TestLogAdapterOmitsEmptyTargetUID (REQ-5): a fact with no counterparty (the
// file/preference sites) must keep today's payload byte-for-byte — no
// target_uid key at all, not an empty one. An empty key would change the payload
// hash and chain input for every unrelated standalone site.
func TestLogAdapterOmitsEmptyTargetUID(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if err := ledger.Log(ctx, &domain.AuditEvent{ActorUID: "user-1", Action: "file.uploaded",
		SubjectType: "file", SubjectID: "f-1", Meta: map[string]any{"storage_type": "s3"}}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if _, ok := repo.appended.Payload["target_uid"]; ok {
		t.Fatalf("payload = %#v, want no target_uid key when TargetUID is empty", repo.appended.Payload)
	}
	if len(repo.appended.Payload) != 1 {
		t.Fatalf("payload keys = %#v, want only the caller meta", repo.appended.Payload)
	}
}

// TestLogAdapterEmptyTargetUIDPayloadMatchesRedaction (REQ-5, the no-regression
// guard for the six non-message sites): with no TargetUID the appended payload
// must be exactly the redacted caller meta.
func TestLogAdapterEmptyTargetUIDPayloadMatchesRedaction(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
	meta := map[string]any{"storage_type": "s3", "bytes": 42, "target_uid": "from-meta"}

	if err := ledger.Log(ctx, &domain.AuditEvent{ActorUID: "user-1", Action: "file.updated",
		SubjectType: "file", SubjectID: "f-1", Meta: meta}); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	want := redactAuditPayload(meta)
	if !reflect.DeepEqual(repo.appended.Payload, want) {
		t.Fatalf("payload = %#v, want the redaction of meta %#v", repo.appended.Payload, want)
	}
}

// TestLogAdapterDoesNotMutateCallerMeta (REQ-4): the key is inserted after
// redactAuditPayload, which always returns a fresh map. That is a non-obvious
// property of the redaction implementation — a future "nothing to redact, return
// the input" fast path would silently start writing target_uid into maps owned
// by the calling service (and across executions of it), so pin it here.
func TestLogAdapterDoesNotMutateCallerMeta(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
	event := &domain.AuditEvent{ActorUID: "user-1", Action: domain.AuditMessageSend,
		SubjectType: "message", SubjectID: "m-1", TargetUID: "user-2",
		Meta: map[string]any{"comment_id": "c-1"}, CreatedAt: time.Now().UTC()}

	if err := ledger.Log(ctx, event); err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if _, ok := event.Meta["target_uid"]; ok {
		t.Fatalf("caller meta mutated: %#v (the adapter must not write into the caller's map)", event.Meta)
	}
	if len(event.Meta) != 1 {
		t.Fatalf("caller meta = %#v, want the single original key", event.Meta)
	}
}

// TestLogAdapterMatchesInTxConverterPayload (REQ-3): the standalone and in-tx
// converters must emit the same sealed fact for the same event, otherwise the
// two arms' payload hashes and chain records diverge for the same action.
// OccurredAt is deliberately excluded: buildLegacyAuditFact passes no
// OccurredAt, so it stamps time.Now() while the adapter forwards
// AuditEvent.CreatedAt — equality there is unachievable by construction.
// Case (c) pins the precedence: a non-empty TargetUID overwrites a target_uid
// already present in meta, in both arms, because both set the key post-redaction.
func TestLogAdapterMatchesInTxConverterPayload(t *testing.T) {
	cases := []struct {
		name      string
		targetUID string
		meta      func() map[string]any
	}{
		{name: "counterparty present", targetUID: "user-2",
			meta: func() map[string]any { return map[string]any{"comment_id": "c-1", "status": "resolved"} }},
		{name: "no counterparty", targetUID: "",
			meta: func() map[string]any { return map[string]any{"comment_id": "c-1", "status": "resolved"} }},
		{name: "target uid already in meta", targetUID: "user-2",
			meta: func() map[string]any { return map[string]any{"comment_id": "c-1", "target_uid": "stale"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &auditLedgerRepoSpy{}
			ledger := NewAuditLedgerService(repo)
			ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})
			anchoredID := idgen.NewID()

			legacy := &domain.AuditEvent{ID: anchoredID, ActorUID: "user-1", Action: domain.AuditMessageSend,
				SubjectType: "message", SubjectID: anchoredID.String(), TargetUID: tc.targetUID,
				Meta: tc.meta(), CreatedAt: time.Now().UTC()}
			inTxMeta := tc.meta()
			inTx := buildLegacyAuditFact(ctx, "user-1", domain.AuditMessageSend, "message",
				anchoredID.String(), tc.targetUID, inTxMeta, anchoredID)

			if err := ledger.Log(ctx, legacy); err != nil {
				t.Fatalf("Log() error = %v", err)
			}
			got := repo.appended
			if !reflect.DeepEqual(got.Payload, inTx.Payload) {
				t.Fatalf("standalone payload = %#v, in-tx payload = %#v (the two converters must agree byte-for-byte)",
					got.Payload, inTx.Payload)
			}
			if got.ActorUID != inTx.ActorUID || got.Action != inTx.Action ||
				got.ResourceType != inTx.ResourceType || got.ResourceID != inTx.ResourceID ||
				got.EventID != inTx.EventID || got.TenantID != inTx.TenantID || got.Source != inTx.Source {
				t.Fatalf("standalone fact %#v drifted from in-tx fact %#v", got, inTx)
			}
			if got.OccurredAt.IsZero() || inTx.OccurredAt.IsZero() {
				t.Fatal("both arms must stamp OccurredAt")
			}
		})
	}
}

// TestLogAdapterObservesIngestFailureWithFactSource (REQ-2): the shared append
// tail labels both counters from the persisted fact, so a standalone Log that
// cannot be recorded observes the source it actually failed to write
// ("aero-id", filled by normalizeAuditInput) instead of the empty raw input.
// A duplicate fold is still not an ingest failure.
func TestLogAdapterObservesIngestFailureWithFactSource(t *testing.T) {
	metrics := &metricSpy{}
	repo := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	ledger := NewAuditLedgerService(repo)
	ledger.(*auditLedgerService).SetControlPlaneMetrics(metrics)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if err := ledger.Log(ctx, &domain.AuditEvent{ActorUID: "user-1", Action: domain.AuditMessageSend,
		SubjectType: "message", SubjectID: "m-1", TargetUID: "user-2"}); err == nil {
		t.Fatal("Log must propagate the append failure — standalone ingest is fail-closed")
	}
	if len(metrics.ingestFailures) != 1 || metrics.ingestFailures[0] != "aero-id" {
		t.Fatalf("ingest failures = %v, want [aero-id] (the persisted fact source)", metrics.ingestFailures)
	}

	dupe := &auditLedgerRepoSpy{appendErr: repository.ErrAuditDuplicate}
	folded := NewAuditLedgerService(dupe)
	folded.(*auditLedgerService).SetControlPlaneMetrics(metrics)
	if err := folded.Log(ctx, &domain.AuditEvent{ActorUID: "user-1", Action: domain.AuditMessageSend,
		SubjectType: "message", SubjectID: "m-1", TargetUID: "user-2"}); err != nil {
		t.Fatalf("duplicate fold must succeed, got %v", err)
	}
	if len(metrics.ingestFailures) != 1 {
		t.Fatalf("ingest failures after duplicate fold = %d, want still 1", len(metrics.ingestFailures))
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

func TestAuditLedgerReadsRecordMetadataAudit(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-a"})
	filter := domain.AuditFactFilter{Action: "account.viewed", Page: domain.Page{Limit: 20, Cursor: "cursor"}}
	if _, _, err := ledger.List(ctx, filter); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repo.appended == nil || repo.appended.Action != "audit.viewed" {
		t.Fatalf("audit append = %#v, want audit.viewed", repo.appended)
	}
	if repo.appended.Payload["action_filter"] != "account.viewed" ||
		repo.appended.Payload["cursor_present"] != true || repo.appended.Payload["page_limit"] != 20 {
		t.Fatalf("audit payload = %#v, want allow-listed query metadata", repo.appended.Payload)
	}
}

func TestAuditLedgerReadAuditFailureIsFailClosed(t *testing.T) {
	repo := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-a"})
	if _, _, err := ledger.List(ctx, domain.AuditFactFilter{}); err == nil {
		t.Fatal("List() must fail when the access audit cannot be appended")
	}
	if err := ledger.VerifyChain(ctx, "aero-id", "tenant-a"); err == nil {
		t.Fatal("VerifyChain() must fail when the access audit cannot be appended")
	}
}
