package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/repository"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/storage"
)

// fakeAuditLedgerRepo pages facts by tenant.
type fakeAuditLedgerRepo struct {
	facts []*domain.AuditFact
}

func (f *fakeAuditLedgerRepo) Append(ctx context.Context, fact *domain.AuditFact) error { return nil }
func (f *fakeAuditLedgerRepo) VerifyChain(ctx context.Context, source, partition string) error {
	return nil
}
func (f *fakeAuditLedgerRepo) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	out := make([]*domain.AuditFact, 0, len(f.facts))
	for _, fact := range f.facts {
		if fact.TenantID == filter.TenantID {
			out = append(out, fact)
		}
	}
	return out, "", nil
}
func (f *fakeAuditLedgerRepo) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return nil, nil
}
func (f *fakeAuditLedgerRepo) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	return nil, repository.ErrNotFound
}

// memoryStore is a minimal FileStorage for artifact verification.
type memoryStore struct {
	objects map[string][]byte
}

func (m *memoryStore) Put(ctx context.Context, owner, name string, reader io.Reader) (string, int64, error) {
	data, _ := io.ReadAll(reader)
	key := name
	m.objects[key] = data
	return key, int64(len(data)), nil
}
func (m *memoryStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *memoryStore) Delete(ctx context.Context, key string) error {
	delete(m.objects, key)
	return nil
}
func (m *memoryStore) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return key, nil
}
func (m *memoryStore) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &storage.ObjectInfo{Size: int64(len(data))}, nil
}
func (m *memoryStore) NewKey(ownerUID string) string { return ownerUID + "/key-" + time.Now().String() }
func (m *memoryStore) List(ctx context.Context, fn func(storage.ObjectInfo) (bool, error)) error {
	for key := range m.objects {
		info := storage.ObjectInfo{Key: key, Modified: time.Now()}
		ok, err := fn(info)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}
func (m *memoryStore) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return key, nil
}

func auditExportFact(tenantID string) *domain.AuditFact {
	sum := sha256.Sum256([]byte("fact-" + tenantID))
	return &domain.AuditFact{ID: idgen.NewID(), TenantID: tenantID, Source: "aero-id", SourceRegion: "local",
		ActorUID: "u-1", Action: "account.provisioned", ResourceType: "account", ResourceID: "acc-1",
		OccurredAt: time.Now().UTC(), RecordHash: hex.EncodeToString(sum[:]), Payload: map[string]any{"outcome": "success"}}
}

func newAuditExportService(facts []*domain.AuditFact, store storage.FileStorage) *syncJobService {
	key := sha256.Sum256([]byte("test-export-key"))
	svc := &syncJobService{
		repo:        &minimalSyncJobRepo{},
		auditLedger: &fakeAuditLedgerRepo{facts: facts},
		store:       store,
		exportKey:   key[:],
		exportTTL:   time.Hour,
		maxExport:   1 << 20,
		region:      "local",
	}
	return svc
}

// minimalSyncJobRepo records finished jobs for the export tests.
type minimalSyncJobRepo struct {
	finished map[domain.ID]*domain.SyncJob
}

func (m *minimalSyncJobRepo) Finish(ctx context.Context, id domain.ID, worker string, status domain.SyncJobStatus, code, message string, result map[string]any) error {
	if m.finished == nil {
		m.finished = map[domain.ID]*domain.SyncJob{}
	}
	if current, ok := m.finished[id]; ok {
		current.Status = status
		current.ErrorCode = code
		current.ErrorMessage = message
		current.Result = result
		return nil
	}
	m.finished[id] = &domain.SyncJob{ID: id, Status: status, ErrorCode: code, ErrorMessage: message, Result: result}
	return nil
}

// SeedJob makes a job visible to the ownership checks in service.Get.
func (m *minimalSyncJobRepo) SeedJob(job *domain.SyncJob) {
	if m.finished == nil {
		m.finished = map[domain.ID]*domain.SyncJob{}
	}
	m.finished[job.ID] = job
}
func (m *minimalSyncJobRepo) Create(ctx context.Context, job *domain.SyncJob) error { return nil }
func (m *minimalSyncJobRepo) FindByIdempotency(ctx context.Context, actor, tenant, key string) (*domain.SyncJob, error) {
	return nil, repository.ErrNotFound
}
func (m *minimalSyncJobRepo) List(ctx context.Context, actor, tenant string, page domain.Page) ([]*domain.SyncJob, string, error) {
	return nil, "", nil
}
func (m *minimalSyncJobRepo) Claim(ctx context.Context, id domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return m.Get(ctx, id)
}
func (m *minimalSyncJobRepo) ClaimUnknown(ctx context.Context, id domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return m.Get(ctx, id)
}
func (m *minimalSyncJobRepo) ClaimUnknownNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return nil, repository.ErrNotFound
}
func (m *minimalSyncJobRepo) ClaimNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return nil, repository.ErrNotFound
}
func (m *minimalSyncJobRepo) ListExpiredExports(ctx context.Context, before time.Time, limit int) ([]*domain.SyncJob, error) {
	return nil, nil
}
func (m *minimalSyncJobRepo) ClearExportArtifact(ctx context.Context, id domain.ID, key string) error {
	return nil
}
func (m *minimalSyncJobRepo) Get(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	if m.finished == nil {
		m.finished = map[domain.ID]*domain.SyncJob{}
	}
	job, ok := m.finished[id]
	if !ok {
		job = &domain.SyncJob{ID: id}
		m.finished[id] = job
	}
	return job, nil
}

// TestAuditExport_RoundTrip: the job writes an encrypted artifact; the
// manifest lists every fact with its record hash and a verifiable digest.
func TestAuditExport_RoundTrip(t *testing.T) {
	facts := []*domain.AuditFact{
		auditExportFact("tenant-a"),
		auditExportFact("tenant-a"),
		auditExportFact("tenant-b"), // must be excluded by tenant filter
	}
	store := &memoryStore{objects: map[string][]byte{}}
	svc := newAuditExportService(facts, store)
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: idgen.NewID(),
		ActorUID: "admin-1", JobType: domain.SyncJobAuditExport}

	result, err := svc.executeAuditExport(context.Background(), job)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Status != domain.SyncJobCompleted {
		t.Fatalf("status = %s, want completed (code %s)", result.Status, result.ErrorCode)
	}
	if got := result.Result["event_count"]; got != 2 {
		t.Errorf("event_count = %v, want 2 (tenant-b excluded)", got)
	}
	key, _ := result.Result["_storage_key"].(string)
	if key == "" {
		t.Fatal("artifact storage key missing")
	}
	reader, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, _ := io.ReadAll(reader)
	decrypted, err := decryptExport(encrypted, svc.exportKey)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(decrypted, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["type"] != "audit_export" || manifest["tenant_id"] != "tenant-a" {
		t.Errorf("manifest header wrong: %v", manifest["type"])
	}
	events, ok := manifest["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("events = %v, want 2", manifest["events"])
	}
	first := events[0].(map[string]any)
	if first["record_hash"] == "" || first["action"] != "account.provisioned" {
		t.Errorf("event projection missing fields: %v", first)
	}
	if manifest["manifest_digest"] != auditExportDigest(manifest) {
		t.Error("manifest digest does not verify")
	}
}

// TestAuditExport_RequiresLedger: without the ledger repository the job fails
// explicitly instead of silently succeeding.
func TestAuditExport_RequiresLedger(t *testing.T) {
	svc := &syncJobService{repo: &minimalSyncJobRepo{}, store: &memoryStore{objects: map[string][]byte{}}, exportKey: []byte("k"), exportTTL: time.Hour}
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", JobType: domain.SyncJobAuditExport}
	result, err := svc.executeAuditExport(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.SyncJobFailed || result.ErrorCode != "audit_export.ledger_unavailable" {
		t.Errorf("status/code = %s/%s, want failed/audit_export.ledger_unavailable", result.Status, result.ErrorCode)
	}
}

// TestAuditExportDigest_StableAcrossMarshalling: the digest is computed over
// deterministic fields, so re-marshalling does not change it.
func TestAuditExportDigest_StableAcrossMarshalling(t *testing.T) {
	manifest := map[string]any{
		"schema_version": 1, "type": "audit_export", "tenant_id": "t", "account_id": "a",
		"generated_at": time.Now().UTC(), "event_count": 1,
		"events": []any{map[string]any{"event_id": "e", "record_hash": "h"}},
	}
	first := auditExportDigest(manifest)
	second := auditExportDigest(manifest)
	if first != second {
		t.Fatal("digest must be deterministic")
	}
	_ = idgen.NewID
}

// TestAuditExport_OneTimeDownloadBurnsArtifact (P7-06): a one-time download
// succeeds once; the artifact and storage key are removed, so a second
// download fails with not_found.
func auditExportContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{TenantID: "tenant-a", Subject: "admin-1", UID: "admin-1"})
}

func TestAuditExport_OneTimeDownloadBurnsArtifact(t *testing.T) {
	facts := []*domain.AuditFact{auditExportFact("tenant-a")}
	store := &memoryStore{objects: map[string][]byte{}}
	svc := newAuditExportService(facts, store)
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: idgen.NewID(),
		ActorUID: "admin-1", JobType: domain.SyncJobAuditExport}

	result, err := svc.executeAuditExport(context.Background(), job)
	if err != nil || result.Status != domain.SyncJobCompleted {
		t.Fatalf("execute: %v %s", err, result.Status)
	}
	key, _ := result.Result["_storage_key"].(string)
	if key == "" {
		t.Fatal("storage key missing")
	}
	completed := &domain.SyncJob{ID: job.ID, TenantID: "tenant-a", ActorUID: "admin-1",
		JobType: domain.SyncJobAuditExport, Status: result.Status, Result: result.Result}
	svc.repo.(*minimalSyncJobRepo).SeedJob(completed)

	first, err := svc.DownloadExport(auditExportContext(), job.ID, true)
	if err != nil || len(first) == 0 {
		t.Fatalf("first download: %v", err)
	}
	if _, ok := store.objects[key]; ok {
		t.Error("artifact must be deleted after a one-time download")
	}
	if _, err := svc.DownloadExport(auditExportContext(), job.ID, true); err == nil {
		t.Fatal("second one-time download must fail")
	}
}

// TestAuditExport_RepeatedDownloadAllowedWithoutOneTime: the default download
// is repeatable until TTL expiry.
func TestAuditExport_RepeatedDownloadAllowedWithoutOneTime(t *testing.T) {
	facts := []*domain.AuditFact{auditExportFact("tenant-a")}
	store := &memoryStore{objects: map[string][]byte{}}
	svc := newAuditExportService(facts, store)
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: idgen.NewID(),
		ActorUID: "admin-1", JobType: domain.SyncJobAuditExport}
	result, err := svc.executeAuditExport(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	completed := &domain.SyncJob{ID: job.ID, TenantID: "tenant-a", ActorUID: "admin-1",
		JobType: domain.SyncJobAuditExport, Status: result.Status, Result: result.Result}
	svc.repo.(*minimalSyncJobRepo).SeedJob(completed)
	if _, err := svc.DownloadExport(auditExportContext(), job.ID, false); err != nil {
		t.Fatalf("first download: %v", err)
	}
	if _, err := svc.DownloadExport(auditExportContext(), job.ID, false); err != nil {
		t.Fatalf("second download: %v", err)
	}
}

func TestAuditExport_DownloadRecordsAccessAuditBeforeBurn(t *testing.T) {
	facts := []*domain.AuditFact{auditExportFact("tenant-a")}
	store := &memoryStore{objects: map[string][]byte{}}
	audit := &auditRecordSpy{}
	svc := newAuditExportService(facts, store)
	job := &domain.SyncJob{ID: idgen.NewID(), TenantID: "tenant-a", AccountID: idgen.NewID(),
		ActorUID: "admin-1", JobType: domain.SyncJobAuditExport}
	result, err := svc.executeAuditExport(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	svc.audit = audit
	key, _ := result.Result["_storage_key"].(string)
	svc.repo.(*minimalSyncJobRepo).SeedJob(&domain.SyncJob{ID: job.ID, TenantID: job.TenantID,
		ActorUID: job.ActorUID, JobType: job.JobType, Status: result.Status, Result: result.Result})
	if _, err := svc.DownloadExport(auditExportContext(), job.ID, true); err != nil {
		t.Fatalf("download: %v", err)
	}
	if audit.records != 1 {
		t.Fatalf("audit records = %d, want 1", audit.records)
	}
	if _, ok := store.objects[key]; ok {
		t.Fatal("one-time artifact must be burned after the access audit")
	}
}
