package app

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/storage"
	storagegc "github.com/aero/aero-id/internal/storage/gc"
)

// storagegcAuditWriter adapts the storage GC sweep's destructive phases to the
// durable audit ledger (the last un-audited destructive path in the live
// tree). Row purge is IN-TX via the ledger-backed HardDeleteWithAudit (a
// failed fact append rolls the purge back); blob/orphan reclamation happens
// AFTER the row purge committed (external storage side effects with no domain
// row to join), so it is observed as checked out-of-tx facts via Record — the
// standalone counter in the ledger service counts them, and check-audit-swallow
// keeps every write error-checked.
type storagegcAuditWriter struct {
	fileRepo repository.FileRepository
	ledger   service.AuditLedgerService
}

var errGCFileAuditUnavailable = errors.New("file repository does not implement the storage GC audit surface")

// PurgeWithAudit hard-deletes the purged rows and appends the fact in one
// transaction (storage.gc.purged). The tenant "legacy" is excluded from the
// governance binding-coverage gate by design (system worker, no tenant).
func (w storagegcAuditWriter) PurgeWithAudit(ctx context.Context, ids []domain.ID, fact *domain.AuditFact) error {
	repo, ok := w.fileRepo.(repository.FileAuditRepository)
	if !ok {
		return errGCFileAuditUnavailable
	}
	return repo.HardDeleteWithAudit(ctx, ids, fact)
}

// RecordBlobReclaim observes blob reclamation after the in-tx purge committed.
func (w storagegcAuditWriter) RecordBlobReclaim(ctx context.Context, blobs int, keys []string) error {
	return w.ledger.Record(ctx, service.AuditFactInput{Source: "aero-id", ActorUID: "storage-gc",
		TenantID: "legacy", Action: "storage.gc.blobs_reclaimed", ResourceType: "file",
		Payload: map[string]any{"blobs": blobs, "storage_keys": keys}})
}

// RecordOrphanSweep observes orphan-blob deletion.
func (w storagegcAuditWriter) RecordOrphanSweep(ctx context.Context, keys []string) error {
	return w.ledger.Record(ctx, service.AuditFactInput{Source: "aero-id", ActorUID: "storage-gc",
		TenantID: "legacy", Action: "storage.gc.orphans_swept", ResourceType: "file",
		Payload: map[string]any{"storage_keys": keys}})
}

// buildStorageGCSweeper constructs the storage GC sweep with the durable audit
// surface attached (extracted from new.go to keep the composition root under
// the file-size gate). Returns the stop function.
func buildStorageGCSweeper(cfg *config.Config, log *zap.Logger, fileRepo repository.FileRepository,
	storageRefRepo repository.StorageRefRepository, store storage.FileStorage,
	auditLedger service.AuditLedgerService) func() {
	sweeper := storagegc.New(fileRepo, storageRefRepo, store, storagegc.Config{
		Retention:    cfg.Storage.GC.Retention,
		MinOrphanAge: cfg.Storage.GC.MinOrphanAge,
		PurgeBatch:   cfg.Storage.GC.PurgeBatch,
		SweepOrphans: cfg.Storage.GC.SweepOrphans,
		PausePurge:   cfg.Storage.GC.PurgePaused,
	}, log)
	sweeper.WithAuditWriter(storagegcAuditWriter{fileRepo: fileRepo, ledger: auditLedger})
	log.Info("storage gc sweep enabled",
		zap.Duration("interval", cfg.Storage.GC.Interval),
		zap.Duration("retention", cfg.Storage.GC.Retention),
		zap.Bool("sweep_orphans", cfg.Storage.GC.SweepOrphans),
		zap.Bool("purge_paused", cfg.Storage.GC.PurgePaused))
	return sweeper.Start(cfg.Storage.GC.Interval)
}
