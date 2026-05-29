package gc

import (
	"context"
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
	mu      sync.Mutex
	deleted []string
	objects []storage.ObjectInfo // enumerated by listableStore.List
}

func (s *fakeStore) Put(context.Context, string, string, io.Reader) (string, int64, error) {
	return "", 0, nil
}
func (s *fakeStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	sw := New(repo, store, Config{Retention: time.Hour}, nil)

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
	sw := New(repo, store, Config{Retention: time.Hour}, nil)

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
	sw := New(repo, store, Config{
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
	sw := New(repo, store, Config{Retention: time.Hour, SweepOrphans: true}, nil)

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
	sw := New(repo, store, Config{Retention: time.Hour, MinOrphanAge: time.Hour, SweepOrphans: true}, nil).
		WithClock(fixedClock(now))

	if _, err := sw.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.wasDeleted("owner/live") {
		t.Fatalf("object with a referencing row must not be swept; deletes=%v", base.deleted)
	}
}
