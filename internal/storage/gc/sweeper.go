// Package gc implements a storage reconciliation sweep that closes two leaks in the
// file vertical:
//
//  1. Soft-deleted file rows are never purged — GORM hides them but they accumulate
//     forever, and so do the blobs they reference. The sweep hard-deletes rows whose
//     deleted_at is older than a retention window, removing each blob only when no
//     remaining row (live or soft-deleted) still references its storage key.
//  2. The create/upload path is best-effort about blob cleanup, so a blob can end up
//     with no referencing row at all (orphan). The sweep enumerates stored objects
//     (where the backend supports listing) and deletes those with no referencing row,
//     guarded by a minimum age so an in-flight upload is never reaped.
//
// The whole job is OPT-IN and disabled by default (see Config / app wiring) so it can
// never surprise an operator. Run performs a single, unit-testable pass; Start runs Run
// on an interval and returns a stop func mirroring outbox.Relay / TokenCache.RunCleaner.
package gc

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/storage"
)

// FileRepo is the subset of repository.FileRepository the sweep needs. Declaring it
// here keeps the sweep unit-testable with a tiny fake and free of a GORM dependency.
type FileRepo interface {
	ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error)
	HardDelete(ctx context.Context, ids []domain.ID) error
	// CountAnyByStorageKey counts rows (live + soft-deleted) referencing a storage key.
	CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error)
}

// RefCounter is the subset of StorageRefRepository used by the sweeper.
// When wired, the sweeper checks this instead of the legacy CountAnyByStorageKey
// so that concurrent content-dedup uploads do not race with blob deletion.
type RefCounter interface {
	GetCount(ctx context.Context, storageKey string) (int, error)
}

// Config tunes a Sweeper. The zero value is effectively "do nothing safely": with a zero
// retention every just-deleted row would be eligible, so callers must set sensible
// values — the app supplies these from config and refuses to start a disabled sweep.
type Config struct {
	// Retention is how long a soft-deleted row is kept before it may be purged.
	Retention time.Duration
	// MinOrphanAge guards orphan-blob deletion: an object younger than this is assumed to
	// be an in-flight upload and is never swept.
	MinOrphanAge time.Duration
	// PurgeBatch caps how many soft-deleted rows a single pass purges (<=0 => default).
	PurgeBatch int
	// SweepOrphans enables the orphan-blob phase. When false (or the backend can't list)
	// only row purging runs.
	SweepOrphans bool
	// PausePurge suspends the destructive phases (row purge + orphan sweep) so
	// maintenance flows such as dead-letter replay cannot race blob
	// reclamation. When true, Run performs no purge work — no rows deleted, no
	// blobs reclaimed, no audit facts minted.
	PausePurge bool
}

// AuditWriter records durable audit facts for the destructive sweep phases
// (purge and orphan reclamation). The composition root wires the ledger-backed
// writer; a nil writer disables auditing (unit-test construction only — the
// production wiring always supplies one so a purge without its fact is
// impossible).
type AuditWriter interface {
	// PurgeWithAudit hard-deletes the purged file rows and appends the fact in
	// ONE transaction: a failed fact append rolls the row purge back (never a
	// purge without its fact — the in-tx contract of the audit migration).
	// The fact is built by the caller with action "storage.gc.purged".
	PurgeWithAudit(ctx context.Context, ids []domain.ID, fact *domain.AuditFact) error
	// RecordBlobReclaim records blob reclamation that happened AFTER the
	// in-tx purge committed (blob deletion is an external storage side effect
	// with no domain row to join; the observation is a checked, durable fact).
	RecordBlobReclaim(ctx context.Context, blobs int, keys []string) error
	// RecordOrphanSweep records orphan-blob deletion (external side effect).
	RecordOrphanSweep(ctx context.Context, keys []string) error
}

const defaultPurgeBatch = 500

// Sweeper runs the reconciliation sweep. Construct with New and either call Run for a
// single pass or Start for periodic passes.
type Sweeper struct {
	repo   FileRepo
	refs   RefCounter // nil => uses legacy CountAnyByStorageKey
	store  storage.FileStorage
	now    func() time.Time
	log    *zap.Logger
	cfg    Config
	audits AuditWriter // nil => auditing disabled (unit tests)
}

// New builds a Sweeper. log may be nil. The clock defaults to time.Now and can be
// overridden in tests via WithClock.
func New(repo FileRepo, refs RefCounter, store storage.FileStorage, cfg Config, log *zap.Logger) *Sweeper {
	if log == nil {
		log = zap.NewNop()
	}
	if cfg.PurgeBatch <= 0 {
		cfg.PurgeBatch = defaultPurgeBatch
	}
	return &Sweeper{repo: repo, refs: refs, store: store, now: time.Now, log: log, cfg: cfg}
}

// WithAuditWriter attaches the durable audit surface (composition root; safe
// before Start). The writer's RecordPurge/RecordOrphanSweep are called only
// after the corresponding destructive phase committed.
func (s *Sweeper) WithAuditWriter(audits AuditWriter) *Sweeper {
	if audits != nil {
		s.audits = audits
	}
	return s
}

// WithClock overrides the time source (tests).
func (s *Sweeper) WithClock(now func() time.Time) *Sweeper {
	if now != nil {
		s.now = now
	}
	return s
}

// Stats summarizes one pass.
type Stats struct {
	RowsPurged     int
	BlobsPurged    int // blobs removed alongside purged rows
	OrphansSwept   int
	OrphansScanned int
}

// Run performs a single reconciliation pass: purge soft-deleted rows past retention,
// then (if enabled and supported) sweep orphaned blobs. Errors from individual phases
// are logged and do not abort the other phase; Run returns the first phase error so a
// caller can observe failures, but Start ignores it and retries next tick.
func (s *Sweeper) Run(ctx context.Context) (Stats, error) {
	var stats Stats
	var firstErr error

	if s.cfg.PausePurge {
		// Maintenance pause (mirrors the drain-loop purge_pause contract): no
		// SELECT, no DELETE, no audit facts — a replay run cannot race blob
		// reclamation.
		s.log.Info("gc: reconciliation pass skipped (purge paused)")
		return stats, nil
	}

	purged, blobs, reclaimedKeys, err := s.purgeSoftDeleted(ctx)
	stats.RowsPurged = purged
	stats.BlobsPurged = blobs
	if err != nil {
		s.log.Warn("gc: purge phase failed", zap.Error(err))
		firstErr = err
	} else {
		s.observePurgeAudit(ctx, blobs, reclaimedKeys)
	}

	if s.cfg.SweepOrphans {
		scanned, swept, sweptKeys, oerr := s.sweepOrphans(ctx)
		stats.OrphansScanned = scanned
		stats.OrphansSwept = swept
		if oerr != nil {
			s.log.Warn("gc: orphan sweep phase failed", zap.Error(oerr))
			if firstErr == nil {
				firstErr = oerr
			}
		} else {
			s.observeOrphanAudit(ctx, swept, sweptKeys)
		}
	}

	s.log.Info("gc: reconciliation pass complete",
		zap.Int("rows_purged", stats.RowsPurged),
		zap.Int("blobs_purged", stats.BlobsPurged),
		zap.Int("orphans_scanned", stats.OrphansScanned),
		zap.Int("orphans_swept", stats.OrphansSwept))
	return stats, firstErr
}

// observePurgeAudit records the blob-reclamation observation that happens
// AFTER the in-tx purge committed (blob deletion is an external storage side
// effect with no domain row to join). The in-tx storage.gc.purged fact is
// written by PurgeWithAudit inside purgeSoftDeleted.
func (s *Sweeper) observePurgeAudit(ctx context.Context, blobs int, reclaimedKeys []string) {
	if s.audits == nil || blobs <= 0 {
		return
	}
	if aerr := s.audits.RecordBlobReclaim(ctx, blobs, reclaimedKeys); aerr != nil {
		s.log.Warn("gc: blob reclaim audit observation failed", zap.Error(aerr))
	}
}

// observeOrphanAudit records orphan-blob deletion (external side effect).
func (s *Sweeper) observeOrphanAudit(ctx context.Context, swept int, sweptKeys []string) {
	if s.audits == nil || swept <= 0 {
		return
	}
	if aerr := s.audits.RecordOrphanSweep(ctx, sweptKeys); aerr != nil {
		s.log.Warn("gc: orphan sweep audit observation failed", zap.Error(aerr))
	}
}

// purgeSoftDeleted hard-deletes soft-deleted rows older than the retention window and
// removes each blob that no remaining row references (refcount-safe, mirroring the
// service Delete path). The row is hard-deleted first so the sweeper no longer counts
// it, letting a blob shared only by purged rows be reclaimed. When a RefCounter is
// wired the sweeper uses GetCount instead of CountAnyByStorageKey, eliminating the
// race window between a concurrent content-dedup Create and this purge pass.
//
//nolint:cyclop // row purge and refcount-safe blob reclamation form one reconciliation phase.
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, reclaimedKeys []string, err error) {
	before := s.now().Add(-s.cfg.Retention)
	rows, lerr := s.repo.ListSoftDeletedBefore(ctx, before, s.cfg.PurgeBatch)
	if lerr != nil {
		return 0, 0, nil, lerr
	}
	if len(rows) == 0 {
		return 0, 0, nil, nil
	}

	ids := make([]domain.ID, len(rows))
	for i, f := range rows {
		ids[i] = f.ID
	}
	if s.audits != nil {
		// In-tx audit: the row purge and its storage.gc.purged fact commit in
		// one transaction — an append failure rolls the purge back.
		fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "legacy:gc",
			TenantID: "legacy", ActorUID: "storage-gc", Action: "storage.gc.purged",
			ResourceType: "file", OccurredAt: s.now().UTC(), Payload: map[string]any{"rows": len(ids)}}
		if derr := s.audits.PurgeWithAudit(ctx, ids, fact); derr != nil {
			return 0, 0, nil, derr
		}
	} else if derr := s.repo.HardDelete(ctx, ids); derr != nil {
		return 0, 0, nil, derr
	}
	rowsPurged = len(ids)
	blobsPurged, reclaimedKeys = s.reclaimUnreferencedBlobs(ctx, rows)
	return rowsPurged, blobsPurged, reclaimedKeys, nil
}

// reclaimUnreferencedBlobs de-dups storage keys and deletes each blob that no
// remaining row references. After HardDelete, a still-referenced key (by a
// live row or another soft-deleted row not yet purged) reports count > 0 and
// is left alone.
func (s *Sweeper) reclaimUnreferencedBlobs(ctx context.Context, rows []*domain.File) (int, []string) {
	blobsPurged := 0
	reclaimedKeys := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, f := range rows {
		if f.StorageKey == "" {
			continue
		}
		if _, ok := seen[f.StorageKey]; ok {
			continue
		}
		seen[f.StorageKey] = struct{}{}
		n, cerr := s.countLive(ctx, f.StorageKey)
		if cerr != nil {
			s.log.Warn("gc: count by storage key failed; leaving blob",
				zap.String("storage_key", f.StorageKey), zap.Error(cerr))
			continue
		}
		if n > 0 {
			continue // still referenced; not ours to delete
		}
		if delErr := s.store.Delete(ctx, f.StorageKey); delErr != nil && !errors.Is(delErr, storage.ErrNotFound) {
			s.log.Warn("gc: blob delete failed",
				zap.String("storage_key", f.StorageKey), zap.Error(delErr))
			continue
		}
		blobsPurged++
		reclaimedKeys = append(reclaimedKeys, f.StorageKey)
	}
	return blobsPurged, reclaimedKeys
}

// countLive returns how many live references exist for a storage key. When a
// RefCounter is wired (the new atomic ref-count path) it uses GetCount; otherwise
// it falls back to CountAnyByStorageKey (the legacy live+soft-deleted scan).
func (s *Sweeper) countLive(ctx context.Context, storageKey string) (int, error) {
	if s.refs != nil {
		return s.refs.GetCount(ctx, storageKey)
	}
	n, err := s.repo.CountAnyByStorageKey(ctx, storageKey)
	return int(n), err
}

// sweepOrphans enumerates stored objects (best-effort) and deletes those with no
// referencing row, skipping objects younger than MinOrphanAge so an in-flight upload is
// never reaped. If the backend can't list objects the phase is a logged no-op.
func (s *Sweeper) sweepOrphans(ctx context.Context) (scanned, swept int, sweptKeys []string, err error) {
	lister, ok := s.store.(storage.Lister)
	if !ok {
		s.log.Info("gc: storage backend does not support listing; skipping orphan sweep")
		return 0, 0, nil, nil
	}
	cutoff := s.now().Add(-s.cfg.MinOrphanAge)
	listErr := lister.List(ctx, func(obj storage.ObjectInfo) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		scanned++
		// Age guard: a freshly written object may be an upload whose row isn't committed
		// yet. Modified is the object's last-write time.
		if !obj.Modified.IsZero() && obj.Modified.After(cutoff) {
			return true, nil
		}
		n, cerr := s.countLive(ctx, obj.Key)
		if cerr != nil {
			s.log.Warn("gc: count by storage key failed; leaving object",
				zap.String("storage_key", obj.Key), zap.Error(cerr))
			return true, nil
		}
		if n > 0 {
			return true, nil // referenced; keep
		}
		if delErr := s.store.Delete(ctx, obj.Key); delErr != nil && !errors.Is(delErr, storage.ErrNotFound) {
			s.log.Warn("gc: orphan delete failed",
				zap.String("storage_key", obj.Key), zap.Error(delErr))
			return true, nil
		}
		swept++
		sweptKeys = append(sweptKeys, obj.Key)
		return true, nil
	})
	if listErr != nil {
		return scanned, swept, sweptKeys, listErr
	}
	return scanned, swept, sweptKeys, nil
}

// Start launches a goroutine that runs a pass every interval and returns a stop func
// that terminates it (mirrors outbox.Relay.Start / TokenCache.RunCleaner). A pass error
// is already logged inside Run and otherwise ignored; the next tick retries.
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		for {
			select {
			case <-t.C:
				_, _ = s.Run(ctx)
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
