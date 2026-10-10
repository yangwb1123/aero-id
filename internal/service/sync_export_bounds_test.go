package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/storage"
)

func TestExportPaginatorRejectsStalledOrOversizedPages(t *testing.T) {
	for _, page := range []exportReferencePage{
		{scanned: 0, next: "cursor", lastID: "cursor"},
		{scanned: 1, next: "wrong", lastID: "actual"},
		{scanned: domain.MaxPageLimit + 1},
	} {
		_, err := readExportPages(&exportBudget{limit: 1 << 20}, func(domain.Page) (exportReferencePage, error) { return page, nil })
		if got := requireAppError(t, err).Code; got != "export.incomplete" {
			t.Fatalf("invalid page error = %s", got)
		}
	}
	calls := 0
	_, err := readExportPages(&exportBudget{limit: 1 << 20}, func(domain.Page) (exportReferencePage, error) {
		calls++
		return exportReferencePage{scanned: 1, next: "same", lastID: "same"}, nil
	})
	if got := requireAppError(t, err).Code; got != "export.incomplete" || calls != 2 {
		t.Fatalf("repeated cursor = %s, calls = %d", got, calls)
	}
}

func TestExportByteBudgetDoesNotPublishTruncatedArtifact(t *testing.T) {
	svc, job, _, _, store := newScopedExportFixture(t, 205)
	svc.maxExport = 512
	result, err := svc.executeExport(t.Context(), job)
	if err != nil || result.Status != domain.SyncJobPartiallyComplete || result.ErrorCode != "export.too_large" {
		t.Fatalf("bounded export = %v/%v", result, err)
	}
	if len(store.objects) != 0 || len(result.Result) != 0 {
		t.Fatal("oversized/truncated export was published or plaintext persisted")
	}
}

func TestExportRowBudgetFailsInsteadOfSilentlyTruncating(t *testing.T) {
	budget := &exportBudget{limit: 1 << 20, scanned: maxInlineExportReferences}
	_, err := readExportPages(budget, func(domain.Page) (exportReferencePage, error) {
		return exportReferencePage{scanned: 1}, nil
	})
	if got := requireAppError(t, err).Code; got != "export.too_large" {
		t.Fatalf("row budget error = %s", got)
	}
}

type erasingExportStore struct {
	*memoryStore
	account   *exportAccountFixture
	deleteErr error
}

func (s *erasingExportStore) Put(ctx context.Context, owner, name string, reader io.Reader) (string, int64, error) {
	key, size, err := s.memoryStore.Put(ctx, owner, name, reader)
	s.account.account.Status = domain.AccountErased
	return key, size, err
}

func (s *erasingExportStore) Delete(ctx context.Context, key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memoryStore.Delete(ctx, key)
}

func TestExportEraseDuringObjectWriteDiscardsArtifact(t *testing.T) {
	svc, job, account, _, store := newScopedExportFixture(t, 1)
	svc.store = &erasingExportStore{memoryStore: store, account: account}
	_, err := svc.executeExport(t.Context(), job)
	if got := requireAppError(t, err).Code; got != "export.account_erased" {
		t.Fatalf("erase race error = %s", got)
	}
	if len(store.objects) != 0 || job.Status != domain.SyncJobFailed {
		t.Fatal("erased export artifact was retained or completed")
	}
}

func TestExportEraseCleanupUncertaintyKeepsUnknownAndTTLMetadata(t *testing.T) {
	svc, job, account, _, store := newScopedExportFixture(t, 1)
	svc.store = &erasingExportStore{memoryStore: store, account: account, deleteErr: errors.New("injected deletion uncertainty")}
	result, err := svc.executeExport(t.Context(), job)
	if err != nil || result.Status != domain.SyncJobUnknown || result.ErrorCode != "export.cleanup_unknown" {
		t.Fatalf("cleanup uncertainty = %v/%v", result, err)
	}
	if result.Result["_storage_key"] == nil || result.Result["expires_at"] == nil {
		t.Fatal("unknown cleanup lost durable retention metadata")
	}
	if _, ok := result.Result["profile"]; ok {
		t.Fatal("unknown cleanup persisted unencrypted content")
	}
	// A later claimed reconciliation retries deletion, not export generation,
	// and must not lose the prior attempt's artifact key on an erased account.
	svc.store.(*erasingExportStore).deleteErr = nil
	_, err = svc.executeExport(t.Context(), result)
	if got := requireAppError(t, err).Code; got != "export.invalidated" {
		t.Fatalf("cleanup reconciliation = %s", got)
	}
	finished, loadErr := svc.repo.Get(t.Context(), result.ID)
	if loadErr != nil || len(store.objects) != 0 || finished.Status != domain.SyncJobFailed {
		t.Fatal("reconciliation failed to finalize artifact disposal")
	}
	if _, ok := finished.Result["_storage_key"]; ok {
		t.Fatal("successful cleanup retained a downloadable artifact reference")
	}
}

var _ storage.FileStorage = (*erasingExportStore)(nil)
