package gc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/storage"
)

// fakeRepo is a tiny in-memory FileRepo for sweeper tests.
type fakeRepo struct {
	softDeleted []*domain.File
	// liveRefs maps storage key -> count of rows (live or still-soft-deleted) that the
	// repo reports for CountAnyByStorageKey. The sweep decrements these as it purges.
	refs map[string]int64

	hardDeleted []domain.ID
}

func (r *fakeRepo) ListSoftDeletedBefore(_ context.Context, before time.Time, limit int) ([]*domain.File, error) {
	var out []*domain.File
	for _, f := range r.softDeleted {
		out = append(out, f)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeRepo) HardDelete(_ context.Context, ids []domain.ID) error {
	r.hardDeleted = append(r.hardDeleted, ids...)
	// Removing a row decrements that storage key's reference count, modelling the real
	// repo where CountAnyByStorageKey no longer counts a hard-deleted row.
	gone := make(map[domain.ID]struct{}, len(ids))
	for _, id := range ids {
		gone[id] = struct{}{}
	}
	for _, f := range r.softDeleted {
		if _, ok := gone[f.ID]; ok && r.refs != nil {
			if r.refs[f.StorageKey] > 0 {
				r.refs[f.StorageKey]--
			}
		}
	}
	return nil
}

func (r *fakeRepo) CountAnyByStorageKey(_ context.Context, key string) (int64, error) {
	if r.refs == nil {
		return 0, nil
	}
	return r.refs[key], nil
}

// fakeStore records deletes and optionally enumerates objects for the orphan phase.
type fakeStore struct {
	mu           sync.Mutex
	deleted      []string
	deleteErrors map[string]error
	objects      []storage.ObjectInfo // enumerated by listableStore.List
}

func (s *fakeStore) Put(context.Context, string, string, io.Reader) (string, int64, error) {
	return "", 0, nil
}
func (s *fakeStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deleteErrors[key]; err != nil {
		return err
	}
	s.deleted = append(s.deleted, key)
	return nil
}
func (s *fakeStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (s *fakeStore) Stat(context.Context, string) (*storage.ObjectInfo, error) { return nil, nil }
func (s *fakeStore) NewKey(string) string                                      { return "" }
func (s *fakeStore) PresignPut(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (s *fakeStore) wasDeleted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.deleted {
		if k == key {
			return true
		}
	}
	return false
}

// listableStore wraps fakeStore and implements storage.Lister.
type listableStore struct {
	*fakeStore
}

func (s *listableStore) List(ctx context.Context, fn func(storage.ObjectInfo) (bool, error)) error {
	for _, o := range s.objects {
		cont, err := fn(o)
		if err != nil {
			return err
		}
		if !cont {
			return nil
		}
	}
	return nil
}

var _ storage.FileStorage = (*fakeStore)(nil)
var _ storage.Lister = (*listableStore)(nil)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestRun_PurgesOldRowAndDeletesUnreferencedBlob(t *testing.T) {
	f := &domain.File{ID: idgen.NewID(), StorageKey: "owner/blob-a"}
	repo := &fakeRepo{
		softDeleted: []*domain.File{f},
		refs:        map[string]int64{"owner/blob-a": 1}, // only this (soon-purged) row references it
	}
	store := &fakeStore{}
	sw := New(repo, nil, store, Config{Retention: time.Hour}, nil)

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.RowsPurged != 1 {
		t.Fatalf("RowsPurged = %d, want 1", stats.RowsPurged)
	}
	if len(repo.hardDeleted) != 1 || repo.hardDeleted[0] != f.ID {
		t.Fatalf("hardDeleted = %v, want [%v]", repo.hardDeleted, f.ID)
	}
	if !store.wasDeleted("owner/blob-a") {
		t.Fatalf("expected unreferenced blob to be deleted; deletes=%v", store.deleted)
	}
	if stats.BlobsPurged != 1 {
		t.Fatalf("BlobsPurged = %d, want 1", stats.BlobsPurged)
	}
}

func TestRun_PurgesRowButKeepsBlobSharedByLiveRow(t *testing.T) {
	// Two rows share one storage key (dedup). One is soft-deleted and purged; a live row
	// still references the blob, so the blob must survive.
	shared := "owner/shared-blob"
	deleted := &domain.File{ID: idgen.NewID(), StorageKey: shared}
	repo := &fakeRepo{
		softDeleted: []*domain.File{deleted},
		refs:        map[string]int64{shared: 2}, // the soft-deleted row + one live row
	}
	store := &fakeStore{}
	sw := New(repo, nil, store, Config{Retention: time.Hour}, nil)

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.RowsPurged != 1 {
		t.Fatalf("RowsPurged = %d, want 1", stats.RowsPurged)
	}
	if store.wasDeleted(shared) {
		t.Fatalf("shared blob must NOT be deleted while a live row references it; deletes=%v", store.deleted)
	}
	if stats.BlobsPurged != 0 {
		t.Fatalf("BlobsPurged = %d, want 0", stats.BlobsPurged)
	}
}

func TestRun_PurgeAuditReportsActualReclaimedStorageKeys(t *testing.T) {
	shared := "owner/shared-blob"
	first := &domain.File{ID: idgen.NewID(), StorageKey: shared}
	second := &domain.File{ID: idgen.NewID(), StorageKey: shared}
	repo := &fakeRepo{
		softDeleted: []*domain.File{first, second},
		refs:        map[string]int64{shared: 2},
	}
	store := &fakeStore{}
	audits := &recordingAuditWriter{repo: repo}
	sw := New(repo, nil, store, Config{Retention: time.Hour}, nil).WithAuditWriter(audits)

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BlobsPurged != 1 {
		t.Fatalf("BlobsPurged = %d, want 1", stats.BlobsPurged)
	}
	if len(audits.blobReclaim) != 1 || audits.blobReclaim[0] != shared {
		t.Fatalf("blob reclaim keys = %v, want [%q]", audits.blobReclaim, shared)
	}
	if len(store.deleted) != 1 || store.deleted[0] != shared {
		t.Fatalf("deleted keys = %v, want [%q]", store.deleted, shared)
	}
}

func TestRun_PurgeAuditOmitsStorageKeysWhoseDeleteFailed(t *testing.T) {
	key := "owner/delete-fails"
	file := &domain.File{ID: idgen.NewID(), StorageKey: key}
	repo := &fakeRepo{softDeleted: []*domain.File{file}, refs: map[string]int64{key: 1}}
	store := &fakeStore{deleteErrors: map[string]error{key: errors.New("storage unavailable")}}
	audits := &recordingAuditWriter{repo: repo}
	sw := New(repo, nil, store, Config{Retention: time.Hour}, nil).WithAuditWriter(audits)

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BlobsPurged != 0 {
		t.Fatalf("BlobsPurged = %d, want 0", stats.BlobsPurged)
	}
	if len(audits.blobReclaim) != 0 {
		t.Fatalf("failed deletion must not be audited as reclaimed: %v", audits.blobReclaim)
	}
}

func TestRun_OrphanYoungerThanMinAgeIsNotSwept(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{refs: map[string]int64{}} // no row references either object
	base := &fakeStore{
		objects: []storage.ObjectInfo{
			{Key: "owner/young", Modified: now.Add(-1 * time.Minute)},    // in-flight: too young
			{Key: "owner/old-orphan", Modified: now.Add(-2 * time.Hour)}, // genuine orphan
		},
	}
	store := &listableStore{fakeStore: base}
	sw := New(repo, nil, store, Config{
		Retention:    time.Hour,
		MinOrphanAge: time.Hour,
		SweepOrphans: true,
	}, nil).WithClock(fixedClock(now))

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.wasDeleted("owner/young") {
		t.Fatalf("young orphan must not be swept (in-flight guard); deletes=%v", base.deleted)
	}
	if !base.wasDeleted("owner/old-orphan") {
		t.Fatalf("old orphan should be swept; deletes=%v", base.deleted)
	}
	if stats.OrphansSwept != 1 {
		t.Fatalf("OrphansSwept = %d, want 1", stats.OrphansSwept)
	}
	if stats.OrphansScanned != 2 {
		t.Fatalf("OrphansScanned = %d, want 2", stats.OrphansScanned)
	}
}

func TestRun_OrphanSweepSkippedWhenBackendCannotList(t *testing.T) {
	repo := &fakeRepo{refs: map[string]int64{}}
	store := &fakeStore{} // does NOT implement Lister
	sw := New(repo, nil, store, Config{Retention: time.Hour, SweepOrphans: true}, nil)

	stats, err := sw.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.OrphansScanned != 0 || stats.OrphansSwept != 0 {
		t.Fatalf("non-listable backend should no-op orphan phase; stats=%+v", stats)
	}
}

func TestRun_ReferencedObjectNotSwept(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{refs: map[string]int64{"owner/live": 1}}
	base := &fakeStore{
		objects: []storage.ObjectInfo{{Key: "owner/live", Modified: now.Add(-2 * time.Hour)}},
	}
	store := &listableStore{fakeStore: base}
	sw := New(repo, nil, store, Config{Retention: time.Hour, MinOrphanAge: time.Hour, SweepOrphans: true}, nil).
		WithClock(fixedClock(now))

	if _, err := sw.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.wasDeleted("owner/live") {
		t.Fatalf("object with a referencing row must not be swept; deletes=%v", base.deleted)
	}
}

// recordingAuditWriter records the audit surface invocations. When repo is
// set, PurgeWithAudit also performs the domain write (modelling the real
// in-tx HardDeleteWithAudit) so blob-refcount semantics stay truthful.
type recordingAuditWriter struct {
	mu          sync.Mutex
	repo        *fakeRepo
	purged      []domain.ID
	purgeFacts  []*domain.AuditFact
	blobReclaim []string // keys
	orphans     []string
	failPurge   error // when set, PurgeWithAudit fails (rollback simulation)
}

func (w *recordingAuditWriter) PurgeWithAudit(ctx context.Context, ids []domain.ID, fact *domain.AuditFact) error {
	if w.failPurge != nil {
		return w.failPurge
	}
	w.mu.Lock()
	w.purged = append(w.purged, ids...)
	w.purgeFacts = append(w.purgeFacts, fact)
	w.mu.Unlock()
	if w.repo != nil {
		return w.repo.HardDelete(ctx, ids)
	}
	return nil
}

func (w *recordingAuditWriter) RecordBlobReclaim(_ context.Context, blobs int, keys []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.blobReclaim = append(w.blobReclaim, keys...)
	return nil
}

func (w *recordingAuditWriter) RecordOrphanSweep(_ context.Context, keys []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.orphans = append(w.orphans, keys...)
	return nil
}

// TestRun_PurgePausedSkipsEverything (storage GC pause): with PausePurge set,
// Run performs no purge work — no rows deleted, no audit facts, no orphans.
func TestRun_PurgePausedSkipsEverything(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	old := &domain.File{ID: idgen.NewID(), OwnerUID: "u-1", StorageKey: "k-1"}
	repo := &fakeRepo{softDeleted: []*domain.File{old}, refs: map[string]int64{"k-1": 1}}
	audits := &recordingAuditWriter{}
	s := New(repo, nil, &fakeStore{}, Config{
		Retention: 24 * time.Hour, MinOrphanAge: time.Hour, PurgeBatch: 10, SweepOrphans: true, PausePurge: true,
	}, nil).WithClock(func() time.Time { return now }).WithAuditWriter(audits)

	stats, err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.RowsPurged != 0 || stats.OrphansSwept != 0 {
		t.Fatalf("paused pass stats = %+v, want zero purges", stats)
	}
	if len(repo.hardDeleted) != 0 {
		t.Fatalf("paused pass hard-deleted %d rows, want 0", len(repo.hardDeleted))
	}
	if len(audits.purged) != 0 || len(audits.blobReclaim) != 0 || len(audits.orphans) != 0 {
		t.Fatalf("paused pass minted audit facts: purged=%d reclaim=%d orphans=%d, want 0",
			len(audits.purged), len(audits.blobReclaim), len(audits.orphans))
	}
}

// TestRun_PurgeWritesInTxAuditAndBlobReclaimObservation: with the audit writer
// wired, the row purge goes through PurgeWithAudit (in-tx fact) and the blob
// reclamation is observed as a separate checked fact.
func TestRun_PurgeWritesInTxAuditAndBlobReclaimObservation(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	old := &domain.File{ID: idgen.NewID(), OwnerUID: "u-1", StorageKey: "k-1"}
	repo := &fakeRepo{softDeleted: []*domain.File{old}, refs: map[string]int64{"k-1": 1}}
	audits := &recordingAuditWriter{repo: repo}
	s := New(repo, nil, &fakeStore{}, Config{
		Retention: 24 * time.Hour, MinOrphanAge: time.Hour, PurgeBatch: 10,
	}, nil).WithClock(func() time.Time { return now }).WithAuditWriter(audits)

	stats, err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.RowsPurged != 1 {
		t.Fatalf("rows purged = %d, want 1", stats.RowsPurged)
	}
	if len(audits.purged) != 1 || audits.purged[0] != old.ID {
		t.Fatalf("PurgeWithAudit ids = %v, want [%s]", audits.purged, old.ID)
	}
	if len(audits.purgeFacts) != 1 || audits.purgeFacts[0].Action != "storage.gc.purged" {
		t.Fatalf("purge facts = %v, want one storage.gc.purged", audits.purgeFacts)
	}
	if audits.purgeFacts[0].TenantID != "legacy" {
		t.Fatalf("purge fact tenant = %q, want legacy (system worker, excluded from binding coverage)", audits.purgeFacts[0].TenantID)
	}
	// The blob was reclaimed after the purge (refcount 1 -> 0, blob deleted).
	if stats.BlobsPurged != 1 || len(audits.blobReclaim) != 1 {
		t.Fatalf("blob reclaim = %d / %v, want 1 observation", stats.BlobsPurged, audits.blobReclaim)
	}
}

// TestRun_PurgeAuditFailurePreventsPurge: a failed PurgeWithAudit surfaces the
// error and the rows are NOT hard-deleted (the in-tx contract: never a purge
// without its fact).
func TestRun_PurgeAuditFailurePreventsPurge(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	old := &domain.File{ID: idgen.NewID(), OwnerUID: "u-1", StorageKey: "k-1"}
	repo := &fakeRepo{softDeleted: []*domain.File{old}, refs: map[string]int64{"k-1": 1}}
	audits := &recordingAuditWriter{failPurge: errAuditDown}
	s := New(repo, nil, &fakeStore{}, Config{
		Retention: 24 * time.Hour, MinOrphanAge: time.Hour, PurgeBatch: 10,
	}, nil).WithClock(func() time.Time { return now }).WithAuditWriter(audits)

	stats, err := s.Run(ctx)
	if err == nil {
		t.Fatal("Run must surface the purge audit failure")
	}
	if stats.RowsPurged != 0 {
		t.Fatalf("rows purged = %d, want 0 (in-tx rollback)", stats.RowsPurged)
	}
	if len(repo.hardDeleted) != 0 {
		t.Fatalf("rows hard-deleted despite the audit failure = %d, want 0", len(repo.hardDeleted))
	}
}

var errAuditDown = errors.New("audit ledger down")
