package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type exportAccountFixture struct {
	repository.AccountRepository
	account     *domain.Account
	sources     []*domain.AccountSource
	memberships []*domain.AccountMembership
	sourcePages int
	memberPages int
}

func (r *exportAccountFixture) GetByID(context.Context, string, domain.ID) (*domain.Account, error) {
	copy := *r.account
	return &copy, nil
}

func (r *exportAccountFixture) GetProfile(context.Context, domain.ID) (*domain.AccountProfile, error) {
	return &domain.AccountProfile{AccountID: r.account.ID, DisplayName: "Export owner", Timezone: "UTC"}, nil
}

func exportFixturePage[T any](values []T, id func(T) string, page domain.Page) ([]T, string) {
	start := 0
	for start < len(values) && page.Cursor != "" && id(values[start]) >= page.Cursor {
		start++
	}
	end := min(len(values), start+page.Limit)
	next := ""
	if end < len(values) {
		next = id(values[end-1])
	}
	return values[start:end], next
}

func (r *exportAccountFixture) ListSources(_ context.Context, _ domain.ID, page domain.Page) ([]*domain.AccountSource, string, error) {
	r.sourcePages++
	rows, next := exportFixturePage(r.sources, func(row *domain.AccountSource) string { return row.ID.String() }, page)
	return rows, next, nil
}

func (r *exportAccountFixture) ListMemberships(_ context.Context, _ domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error) {
	r.memberPages++
	rows, next := exportFixturePage(r.memberships, func(row *domain.AccountMembership) string { return row.ID.String() }, page)
	return rows, next, nil
}

type exportSnapshotFixture struct {
	repository.SnapshotRepository
	selection []domain.AccountDataset
	values    []*domain.AccountSnapshot
}

func (r *exportSnapshotFixture) ListSnapshots(_ context.Context, _ domain.ID, datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	r.selection = append([]domain.AccountDataset(nil), datasets...)
	return r.values, nil
}

func newScopedExportFixture(t *testing.T, count int) (*syncJobService, *domain.SyncJob, *exportAccountFixture, *exportSnapshotFixture, *memoryStore) {
	t.Helper()
	accountID := idgen.NewID()
	account := &exportAccountFixture{account: &domain.Account{ID: accountID, TenantID: "tenant-a", CanonicalUID: "actor-1",
		HomeRegion: "local", Status: domain.AccountActive}}
	for i := 0; i < count; i++ {
		account.sources = append(account.sources, &domain.AccountSource{ID: idgen.NewID(), AccountID: accountID,
			Source: "aero-vault", SourceAccountID: fmt.Sprintf("native-%d", i), Status: "active", Version: 1})
		account.memberships = append(account.memberships, &domain.AccountMembership{ID: idgen.NewID(), AccountID: accountID,
			Source: "aero-vault", ScopeType: "tenant", ScopeID: fmt.Sprintf("scope-%d", i), SourceMemberID: "member", Status: "active", Version: 1})
	}
	sort.Slice(account.sources, func(i, j int) bool { return account.sources[i].ID.String() > account.sources[j].ID.String() })
	sort.Slice(account.memberships, func(i, j int) bool { return account.memberships[i].ID.String() > account.memberships[j].ID.String() })
	snapshots := &exportSnapshotFixture{values: []*domain.AccountSnapshot{{ID: idgen.NewID(), AccountID: accountID,
		Source: "aero-vault", Dataset: "aero-vault.usage", Status: domain.SnapshotFresh, Version: 5,
		Data: map[string]any{"usage_bytes": 42}}}}
	store := &memoryStore{objects: map[string][]byte{}}
	svc := newAuditExportService(nil, store)
	svc.accountRepo, svc.snapshots = account, snapshots
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: accountID, ActorUID: "actor-1", TenantID: "tenant-a",
		JobType: domain.SyncJobExport, Status: domain.SyncJobRunning,
		Request: map[string]any{"datasets": []domain.AccountDataset{"aero-vault.usage"}}}
	svc.repo.(*minimalSyncJobRepo).SeedJob(job)
	return svc, job, account, snapshots, store
}

func TestAccountExportHonorsScopeAndTraversesAllReferencePages(t *testing.T) {
	svc, job, account, snapshots, store := newScopedExportFixture(t, 205)
	result, err := svc.executeExport(fingerprintContext(t, "console"), job)
	if err != nil || result.Status != domain.SyncJobCompleted {
		t.Fatalf("execute export = %v/%v", result, err)
	}
	if !reflect.DeepEqual(snapshots.selection, []domain.AccountDataset{"aero-vault.usage"}) {
		t.Fatalf("snapshot query expanded scope: %v", snapshots.selection)
	}
	if account.sourcePages != 3 || account.memberPages != 3 {
		t.Fatalf("source/member pages = %d/%d, want 3/3", account.sourcePages, account.memberPages)
	}
	if _, ok := result.Result["profile"]; ok {
		t.Fatal("plaintext export contents persisted in job result")
	}
	reader, err := store.Get(t.Context(), result.Result["_storage_key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	encrypted, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptExport(encrypted, svc.exportKey)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(plain, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest["sources"].([]any)) != 205 || len(manifest["memberships"].([]any)) != 205 {
		t.Fatal("artifact silently truncated reference pagination")
	}
	if manifest["traversal_complete"] != true || manifest["complete"] != true || manifest["point_in_time"] != false {
		t.Fatalf("completeness/consistency = %v", manifest)
	}
	if result.Result["manifest_digest"] != manifest["manifest_digest"] {
		t.Fatal("artifact and metadata have different content digests")
	}
}

func TestAccountExportRejectsReturnedSnapshotOutsideSelection(t *testing.T) {
	svc, job, _, snapshots, store := newScopedExportFixture(t, 1)
	snapshots.values = append(snapshots.values, &domain.AccountSnapshot{ID: idgen.NewID(), AccountID: job.AccountID,
		Source: "aero-im", Dataset: "aero-im.profile", Status: domain.SnapshotFresh, Version: 9})
	_, err := svc.executeExport(fingerprintContext(t, "console"), job)
	if got := requireAppError(t, err).Code; got != "export.scope_mismatch" {
		t.Fatalf("scope rejection = %s", got)
	}
	if len(store.objects) != 0 || job.Status != domain.SyncJobFailed {
		t.Fatal("out-of-scope export was stored or not durably failed")
	}
}

func TestAccountExportReportsMissingRequestedDatasets(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 0)
	job.Request["datasets"] = []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"}
	manifest, err := svc.buildAccountExportManifest(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if manifest["complete"] != false || manifest["traversal_complete"] != true ||
		!reflect.DeepEqual(manifest["missing_datasets"], []string{"aero-vault.tenants"}) {
		t.Fatalf("incomplete data was advertised as complete: %v", manifest)
	}
}

func TestAccountExportLegacyJobReadIsMetadataOnly(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 0)
	job.Result = map[string]any{"_storage_key": "retained-for-cleanup", "expires_at": "metadata",
		"profile": map[string]any{"display_name": "old private data"}, "datasets": []any{map[string]any{"data": "old projection"}}}
	result, err := svc.Get(fingerprintContext(t, "console"), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) != 2 || result.Result["_storage_key"] != "retained-for-cleanup" {
		t.Fatalf("historical export leaked contents or lost cleanup metadata: %v", result.Result)
	}
	if _, ok := job.Result["profile"]; !ok {
		t.Fatal("read mutated authoritative persistence in place")
	}
	result, err = svc.reloadSyncJob(t.Context(), job.ID)
	if err != nil || len(result.Result) != 2 {
		t.Fatalf("finish reload leaked historical contents: %v, %v", result, err)
	}
}

func TestAccountExportEveryFinishFiltersContent(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 0)
	result, err := svc.finishJobAndLoad(t.Context(), job, domain.SyncJobUnknown,
		"export.artifact_checkpoint_unknown", "metadata outcome is unknown",
		map[string]any{"profile": map[string]any{"display_name": "private"},
			"datasets": []any{"private"}, "_storage_key": "attempt-key", "expires_at": "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) != 2 || len(job.Result) != 2 || result.Result["_storage_key"] != "attempt-key" {
		t.Fatalf("generic finish persisted export content or lost artifact metadata: %v", job.Result)
	}
}
